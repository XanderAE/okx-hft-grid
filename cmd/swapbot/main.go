package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/internal/config"
	"github.com/yourname/okx-hft-grid/internal/execution"
	"github.com/yourname/okx-hft-grid/internal/strategy"
)

func main() {
	// Load credentials from environment
	creds := &config.Credentials{
		APIKey:     os.Getenv("OKX_API_KEY"),
		SecretKey:  os.Getenv("OKX_SECRET_KEY"),
		Passphrase: os.Getenv("OKX_PASSPHRASE"),
	}
	if creds.APIKey == "" {
		fmt.Println("FATAL: OKX_API_KEY not set")
		os.Exit(1)
	}

	// Create clients with production endpoint guard (allows www.okx.com)
	guard := &config.ProductionNetworkGuard{}
	apiClient := execution.NewAPIClientWithEndpointGuard("https://www.okx.com", creds, nil, guard)
	swapGw := execution.NewSwapGateway(apiClient)
	dirSignal := strategy.NewDirectionSignal(5, 20)
	posMgr := execution.NewSwapPositionManager()

	// Hardcoded params (from backtest optimization)
	spread := decimal.NewFromFloat(0.003)
	stopPct := decimal.NewFromFloat(0.02)
	maxHold := 12 * time.Hour
	notionalUSDT := 50.0
	confThreshold := 0.0

	var priceHistory []float64

	fmt.Println("SwapBot started. BTC-USDT-SWAP, spread=0.3%, stop=2%, maxLev=3x")
	fmt.Println("Waiting for signals...")

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-sigCh:
			fmt.Println("Shutdown signal received")
			if posMgr.HasPosition() {
				fmt.Println("WARNING: still holding a position!")
			}
			return
		case <-ticker.C:
			runSwapTick(apiClient, swapGw, dirSignal, posMgr, &priceHistory,
				spread, stopPct, maxHold, notionalUSDT, confThreshold)
		}
	}
}

func runSwapTick(
	apiClient *execution.APIClient,
	swapGw *execution.SwapGateway,
	dirSignal *strategy.DirectionSignal,
	posMgr *execution.SwapPositionManager,
	priceHistory *[]float64,
	spread, stopPct decimal.Decimal,
	maxHold time.Duration,
	notionalUSDT, confThreshold float64,
) {
	// Get price via REST ticker
	price, bestBid, bestAsk := getPrice(apiClient)
	if price <= 0 {
		return
	}

	// Update signal
	dirSignal.Update(decimal.NewFromFloat(price))
	*priceHistory = append(*priceHistory, price)
	if len(*priceHistory) > 100 {
		*priceHistory = (*priceHistory)[len(*priceHistory)-100:]
	}

	// Position management
	pos := posMgr.Get()
	if pos != nil {
		mark := decimal.NewFromFloat(price)
		elapsed := time.Since(pos.OpenedAt)

		// Hard stop
		if posMgr.ShouldHardStop(mark, stopPct) {
			fmt.Printf("[STOP] %s position stopped. Entry=%s Mark=%s\n", pos.Side, pos.Entry, mark)
			closeSide, closePosSide := closeSideFor(pos.Side)
			swapGw.PlaceSwapOrder(closeSide, closePosSide, mark, pos.Contracts, true, int(pos.Leverage))
			posMgr.Close()
			return
		}

		// Force close after max hold
		if posMgr.ShouldForceClose(maxHold) {
			fmt.Printf("[FORCE] %s position force-closed after %s\n", pos.Side, elapsed)
			closeSide, closePosSide := closeSideFor(pos.Side)
			swapGw.PlaceSwapOrder(closeSide, closePosSide, mark, pos.Contracts, true, int(pos.Leverage))
			posMgr.Close()
			return
		}

		// Adjust close order with time decay
		target, ok := posMgr.CloseTarget(spread, elapsed, mark)
		if ok {
			closeSide, closePosSide := closeSideFor(pos.Side)
			target = target.Round(1)
			swapGw.PlaceSwapOrder(closeSide, closePosSide, target, pos.Contracts, true, int(pos.Leverage))
		}
		return
	}

	// No position: evaluate direction
	dir, conf := dirSignal.Evaluate()
	if dir == strategy.Flat || conf < confThreshold {
		return
	}

	// Dynamic leverage
	vol := computeVol(*priceHistory)
	leverage := strategy.ComputeLeverage(vol, conf)

	// Verify liquidation safety
	safeLev, ok := execution.VerifyLiquidationDistance(leverage, 0.02)
	if !ok {
		leverage = safeLev
	}
	levInt := int(math.Floor(leverage))
	if levInt < 1 {
		levInt = 1
	}

	// Contracts
	effectiveNotional := notionalUSDT * leverage
	contracts := execution.ComputeContracts(effectiveNotional, price)
	if contracts < 1 {
		fmt.Printf("[SKIP] notional $%.0f too small for 1 contract at $%.0f\n", effectiveNotional, price)
		return
	}

	// Entry
	tickSize := decimal.NewFromFloat(0.1)
	var side, posSide string
	var entryPrice decimal.Decimal

	if dir == strategy.Long {
		side, posSide = "buy", "long"
		entryPrice = decimal.NewFromFloat(bestBid).Sub(tickSize).Round(1)
		swapGw.SetLeverage(levInt, "long")
	} else {
		side, posSide = "sell", "short"
		entryPrice = decimal.NewFromFloat(bestAsk).Add(tickSize).Round(1)
		swapGw.SetLeverage(levInt, "short")
	}

	result, err := swapGw.PlaceSwapOrder(side, posSide, entryPrice, contracts, false, levInt)
	if err != nil || !result.Success {
		errMsg := "unknown"
		if err != nil {
			errMsg = err.Error()
		} else {
			errMsg = result.Error
		}
		fmt.Printf("[FAIL] %s entry @ %s: %s\n", posSide, entryPrice, errMsg)
		return
	}

	// Record position
	var swapSide execution.SwapSide
	if dir == strategy.Long {
		swapSide = execution.SwapLong
	} else {
		swapSide = execution.SwapShort
	}
	posMgr.Open(swapSide, entryPrice, contracts, leverage)
	fmt.Printf("[OPEN] %s %d contracts @ %s (lev=%.1fx, conf=%.2f, vol=%.4f)\n",
		posSide, contracts, entryPrice, leverage, conf, vol)
}

func closeSideFor(side execution.SwapSide) (string, string) {
	if side == execution.SwapLong {
		return "sell", "long"
	}
	return "buy", "short"
}

func getPrice(client *execution.APIClient) (price, bid, ask float64) {
	resp, err := client.DoRequest("GET", "/api/v5/market/ticker?instId=BTC-USDT-SWAP", nil)
	if err != nil {
		return 0, 0, 0
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var r struct {
		Code string `json:"code"`
		Data []struct {
			Last  string `json:"last"`
			BidPx string `json:"bidPx"`
			AskPx string `json:"askPx"`
		} `json:"data"`
	}
	json.Unmarshal(body, &r)
	if r.Code != "0" || len(r.Data) == 0 {
		return 0, 0, 0
	}
	p, _ := decimal.NewFromString(r.Data[0].Last)
	b, _ := decimal.NewFromString(r.Data[0].BidPx)
	a, _ := decimal.NewFromString(r.Data[0].AskPx)
	price, _ = p.Float64()
	bid, _ = b.Float64()
	ask, _ = a.Float64()
	return
}

func computeVol(prices []float64) float64 {
	if len(prices) < 3 {
		return 0
	}
	window := 20
	if len(prices)-1 < window {
		window = len(prices) - 1
	}
	start := len(prices) - window - 1
	if start < 0 {
		start = 0
	}
	var returns []float64
	for i := start + 1; i < len(prices); i++ {
		if prices[i-1] > 0 {
			returns = append(returns, math.Log(prices[i]/prices[i-1]))
		}
	}
	if len(returns) < 2 {
		return 0
	}
	mean := 0.0
	for _, r := range returns {
		mean += r
	}
	mean /= float64(len(returns))
	variance := 0.0
	for _, r := range returns {
		variance += (r - mean) * (r - mean)
	}
	variance /= float64(len(returns) - 1)
	return math.Sqrt(variance)
}
