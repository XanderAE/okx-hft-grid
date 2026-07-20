package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"
)

// Candle represents a single 1-minute OHLCV candle from OKX.
type Candle struct {
	Timestamp int64
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Vol       float64
}

// Tick represents a simulated market tick derived from a candle.
type Tick struct {
	Price   float64
	BestBid float64
	BestAsk float64
	High    float64
	Low     float64
}

// Trade records a completed round-trip trade.
type Trade struct {
	BuyPrice    float64
	SellPrice   float64
	Quantity    float64
	QtyAfterFee float64
	PnL         float64
	HoldTicks   int
	ExitType    string
}

// BacktestParams holds the tunable parameters for a single backtest run.
type BacktestParams struct {
	Spread     float64 // e.g., 0.003 for 0.3%
	StopLoss   float64 // e.g., 0.015 for 1.5%
	MomentumOn bool
}

// BacktestResult holds the outcome of a single backtest run.
type BacktestResult struct {
	Params       BacktestParams
	TotalTrades  int
	WinRate      float64
	TotalPnL     float64
	MaxDrawdown  float64
	TradesPerDay float64
	StopCount    int
}

const (
	fee          = 0.0008
	tradeSize    = 50.0
	warmupTicks  = 4
	maxHoldTicks = 720 // 12h at 1-min ticks
	feeFloor     = 0.002 // minimum sell target margin (0.2%)
)

func main() {
	instID := "BTC-USDT"
	tickSize := 0.1

	fmt.Printf("Fetching candle data for %s...\n", instID)
	candles, err := fetchCandles(instID, 10080)
	if err != nil {
		fmt.Printf("Error fetching candles for %s: %v\n", instID, err)
		return
	}
	fmt.Printf("Fetched %d candles for %s\n\n", len(candles), instID)
	if len(candles) < 10 {
		fmt.Printf("Not enough candle data for %s\n", instID)
		return
	}

	// Generate ticks once
	ticks := make([]Tick, len(candles))
	for i, c := range candles {
		ticks[i] = Tick{
			Price:   c.Close,
			BestBid: c.Close,
			BestAsk: c.Close,
			High:    c.High,
			Low:     c.Low,
		}
	}

	startTime := time.UnixMilli(candles[0].Timestamp)
	endTime := time.UnixMilli(candles[len(candles)-1].Timestamp)
	days := endTime.Sub(startTime).Hours() / 24.0

	// Parameter grid
	spreads := []float64{0.002, 0.003, 0.004, 0.005, 0.007, 0.01, 0.015, 0.02}
	stops := []float64{0.005, 0.01, 0.015, 0.02, 0.03, 0.05, 9.99}
	momentums := []bool{true, false}

	totalCombinations := len(spreads) * len(stops) * len(momentums)
	fmt.Printf("Running %d parameter combinations...\n", totalCombinations)

	var results []BacktestResult
	done := 0

	for _, spread := range spreads {
		for _, stop := range stops {
			for _, mom := range momentums {
				params := BacktestParams{
					Spread:     spread,
					StopLoss:   stop,
					MomentumOn: mom,
				}
				result := runBacktest(params, ticks, tickSize, days)
				results = append(results, result)
				done++
				if done%20 == 0 {
					fmt.Printf("  ... %d/%d complete\n", done, totalCombinations)
				}
			}
		}
	}

	// Sort by TotalPnL descending (best first)
	sort.Slice(results, func(i, j int) bool {
		return results[i].TotalPnL > results[j].TotalPnL
	})

	// Print results
	fmt.Printf("\n=== PARAMETER OPTIMIZATION: %s (%.0f days) ===\n", instID, days)
	fmt.Printf("Period: %s to %s\n", startTime.UTC().Format("2006-01-02 15:04"), endTime.UTC().Format("2006-01-02 15:04"))
	fmt.Printf("Tested %d combinations\n\n", totalCombinations)

	fmt.Printf("TOP 10 RESULTS (sorted by P&L):\n")
	fmt.Printf("#   Spread  StopLoss  Momentum  Trades  WinRate  P&L       MaxDD     Trades/Day  Stops\n")
	top := 10
	if len(results) < top {
		top = len(results)
	}
	for i := 0; i < top; i++ {
		r := results[i]
		momStr := "ON "
		if !r.Params.MomentumOn {
			momStr = "OFF"
		}
		stopStr := fmt.Sprintf("%.2f%%", r.Params.StopLoss*100)
		if r.Params.StopLoss > 5.0 {
			stopStr = "never "
		}
		fmt.Printf("%-3d %5.2f%%  %6s    %s       %5d   %5.1f%%  %+8.4f  $%7.4f   %6.1f      %d\n",
			i+1,
			r.Params.Spread*100,
			stopStr,
			momStr,
			r.TotalTrades,
			r.WinRate,
			r.TotalPnL,
			r.MaxDrawdown,
			r.TradesPerDay,
			r.StopCount,
		)
	}

	// Print worst 3
	fmt.Printf("\nWORST 3 RESULTS (avoid these):\n")
	fmt.Printf("#   Spread  StopLoss  Momentum  Trades  WinRate  P&L       MaxDD     Trades/Day  Stops\n")
	worst := 3
	if len(results) < worst {
		worst = len(results)
	}
	for i := 0; i < worst; i++ {
		idx := len(results) - 1 - i
		r := results[idx]
		momStr := "ON "
		if !r.Params.MomentumOn {
			momStr = "OFF"
		}
		stopStr := fmt.Sprintf("%.2f%%", r.Params.StopLoss*100)
		if r.Params.StopLoss > 5.0 {
			stopStr = "never "
		}
		fmt.Printf("%-3d %5.2f%%  %6s    %s       %5d   %5.1f%%  %+8.4f  $%7.4f   %6.1f      %d\n",
			i+1,
			r.Params.Spread*100,
			stopStr,
			momStr,
			r.TotalTrades,
			r.WinRate,
			r.TotalPnL,
			r.MaxDrawdown,
			r.TradesPerDay,
			r.StopCount,
		)
	}
}

// runBacktest runs the full strategy simulation with the given parameters.
func runBacktest(params BacktestParams, ticks []Tick, tickSize float64, days float64) BacktestResult {
	var (
		trades       []Trade
		prices       []float64
		holding      bool
		buyPrice     float64
		quantity     float64
		qtyAfterFee  float64
		holdTicks    int
		balance      = 100.0
		maxBalance   = 100.0
		maxDrawdown  = 0.0
		pendingBuy   bool
		pendingBuyPx float64
		stopCount    int
	)

	for i, tick := range ticks {
		currentPrice := tick.Price
		prices = append(prices, currentPrice)

		if i < warmupTicks {
			continue
		}

		if holding {
			holdTicks++

			// Hard stop-loss
			if currentPrice <= buyPrice*(1-params.StopLoss) {
				sellPrice := currentPrice
				pnl := sellPrice*qtyAfterFee*(1-fee) - buyPrice*quantity
				balance += pnl
				trades = append(trades, Trade{
					BuyPrice:    buyPrice,
					SellPrice:   sellPrice,
					Quantity:    quantity,
					QtyAfterFee: qtyAfterFee,
					PnL:         pnl,
					HoldTicks:   holdTicks,
					ExitType:    "stopped",
				})
				stopCount++
				holding = false
				pendingBuy = false
				trackDrawdown(&balance, &maxBalance, &maxDrawdown)
				continue
			}

			// Sell-target decay schedule scaled with spread
			var sellTarget float64
			exitType := "profit"

			if holdTicks > maxHoldTicks {
				// Force market sell after 12h
				sellTarget = currentPrice
				exitType = "timedout"
			} else if currentPrice < buyPrice {
				// Price underwater: skewed exit at spread * 0.5
				skewedMargin := params.Spread * 0.5
				if skewedMargin < feeFloor {
					skewedMargin = feeFloor
				}
				sellTarget = buyPrice * (1 + skewedMargin)
				exitType = "skewed"
			} else if holdTicks <= 60 {
				// 0-1h: full spread
				sellTarget = buyPrice * (1 + params.Spread)
			} else if holdTicks <= 360 {
				// 1-6h: spread * 0.8
				margin := params.Spread * 0.8
				if margin < feeFloor {
					margin = feeFloor
				}
				sellTarget = buyPrice * (1 + margin)
			} else if holdTicks <= 720 {
				// 6-12h: spread * 0.67
				margin := params.Spread * 0.67
				if margin < feeFloor {
					margin = feeFloor
				}
				sellTarget = buyPrice * (1 + margin)
			} else {
				sellTarget = currentPrice
				exitType = "timedout"
			}

			// Check fill
			filled := false
			if exitType == "timedout" && holdTicks > maxHoldTicks {
				filled = true
			} else if tick.High >= sellTarget {
				filled = true
			}

			if filled {
				actualSellPrice := sellTarget
				if exitType == "timedout" {
					actualSellPrice = currentPrice
				}
				pnl := actualSellPrice*qtyAfterFee*(1-fee) - buyPrice*quantity
				balance += pnl
				trades = append(trades, Trade{
					BuyPrice:    buyPrice,
					SellPrice:   actualSellPrice,
					Quantity:    quantity,
					QtyAfterFee: qtyAfterFee,
					PnL:         pnl,
					HoldTicks:   holdTicks,
					ExitType:    exitType,
				})
				holding = false
				pendingBuy = false
				trackDrawdown(&balance, &maxBalance, &maxDrawdown)
			}
			continue
		}

		// Check pending buy fill
		if pendingBuy {
			if tick.Low <= pendingBuyPx {
				buyPrice = pendingBuyPx
				quantity = tradeSize / buyPrice
				qtyAfterFee = quantity * (1 - fee)
				holding = true
				holdTicks = 0
				pendingBuy = false
				continue
			}
		}

		// BUY decision: momentum filter
		if params.MomentumOn && len(prices) >= 3 {
			n := len(prices)
			if prices[n-1] < prices[n-2] && prices[n-2] < prices[n-3] {
				pendingBuy = false
				continue
			}
		}

		// Place BUY limit at bestBid - 1 tick
		pendingBuyPx = tick.BestBid - tickSize
		pendingBuy = true

		// Immediately check fill on this tick
		if tick.Low <= pendingBuyPx {
			buyPrice = pendingBuyPx
			quantity = tradeSize / buyPrice
			qtyAfterFee = quantity * (1 - fee)
			holding = true
			holdTicks = 0
			pendingBuy = false
		}
	}

	// Force close at end if still holding
	if holding {
		lastPrice := ticks[len(ticks)-1].Price
		pnl := lastPrice*qtyAfterFee*(1-fee) - buyPrice*quantity
		balance += pnl
		trades = append(trades, Trade{
			BuyPrice:    buyPrice,
			SellPrice:   lastPrice,
			Quantity:    quantity,
			QtyAfterFee: qtyAfterFee,
			PnL:         pnl,
			HoldTicks:   holdTicks,
			ExitType:    "timedout",
		})
		trackDrawdown(&balance, &maxBalance, &maxDrawdown)
	}

	// Compute metrics
	totalTrades := len(trades)
	winRate := 0.0
	if totalTrades > 0 {
		wins := 0
		for _, t := range trades {
			if t.PnL > 0 {
				wins++
			}
		}
		winRate = float64(wins) / float64(totalTrades) * 100
	}

	totalPnL := balance - 100.0
	tradesPerDay := 0.0
	if days > 0 {
		tradesPerDay = float64(totalTrades) / days
	}

	return BacktestResult{
		Params:       params,
		TotalTrades:  totalTrades,
		WinRate:      winRate,
		TotalPnL:     totalPnL,
		MaxDrawdown:  maxDrawdown,
		TradesPerDay: tradesPerDay,
		StopCount:    stopCount,
	}
}

func trackDrawdown(balance *float64, maxBalance *float64, maxDrawdown *float64) {
	if *balance > *maxBalance {
		*maxBalance = *balance
	}
	dd := *maxBalance - *balance
	if dd > *maxDrawdown {
		*maxDrawdown = dd
	}
}

// fetchCandles retrieves historical 1m candles from OKX public API.
func fetchCandles(instID string, target int) ([]Candle, error) {
	var allCandles []Candle
	client := &http.Client{Timeout: 30 * time.Second}

	// First: get recent candles from /market/candles
	recentCandles, err := fetchFromEndpoint(client, instID, "https://www.okx.com/api/v5/market/candles", "", 1440)
	if err != nil {
		return nil, err
	}
	allCandles = append(allCandles, recentCandles...)
	fmt.Printf("  ... got %d from /market/candles\n", len(recentCandles))

	// Then: get historical candles from /market/history-candles going further back
	if len(allCandles) > 0 && len(allCandles) < target {
		oldest := allCandles[0].Timestamp
		for _, c := range allCandles {
			if c.Timestamp < oldest {
				oldest = c.Timestamp
			}
		}
		afterTS := strconv.FormatInt(oldest, 10)

		histCandles, err := fetchFromEndpoint(client, instID, "https://www.okx.com/api/v5/market/history-candles", afterTS, target-len(allCandles))
		if err != nil {
			fmt.Printf("  ... history-candles error (non-fatal): %v\n", err)
		} else {
			allCandles = append(allCandles, histCandles...)
			fmt.Printf("  ... got %d from /market/history-candles\n", len(histCandles))
		}
	}

	// Sort chronologically (oldest first)
	sort.Slice(allCandles, func(i, j int) bool {
		return allCandles[i].Timestamp < allCandles[j].Timestamp
	})

	// Deduplicate by timestamp
	if len(allCandles) > 1 {
		deduped := []Candle{allCandles[0]}
		for i := 1; i < len(allCandles); i++ {
			if allCandles[i].Timestamp != allCandles[i-1].Timestamp {
				deduped = append(deduped, allCandles[i])
			}
		}
		allCandles = deduped
	}

	return allCandles, nil
}

func fetchFromEndpoint(client *http.Client, instID, baseURL, after string, target int) ([]Candle, error) {
	var allCandles []Candle

	for len(allCandles) < target {
		url := fmt.Sprintf("%s?instId=%s&bar=1m&limit=300", baseURL, instID)
		if after != "" {
			url += "&after=" + after
		}

		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetching candles: %w", err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("reading response: %w", err)
		}

		var result struct {
			Code string     `json:"code"`
			Msg  string     `json:"msg"`
			Data [][]string `json:"data"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("parsing JSON: %w", err)
		}
		if result.Code != "0" {
			return nil, fmt.Errorf("OKX API error: code=%s msg=%s", result.Code, result.Msg)
		}
		if len(result.Data) == 0 {
			break
		}

		batchSize := len(result.Data)
		for _, d := range result.Data {
			if len(d) < 9 {
				continue
			}
			ts, _ := strconv.ParseInt(d[0], 10, 64)
			o, _ := strconv.ParseFloat(d[1], 64)
			h, _ := strconv.ParseFloat(d[2], 64)
			l, _ := strconv.ParseFloat(d[3], 64)
			c, _ := strconv.ParseFloat(d[4], 64)
			vol, _ := strconv.ParseFloat(d[5], 64)

			allCandles = append(allCandles, Candle{
				Timestamp: ts,
				Open:      o,
				High:      h,
				Low:       l,
				Close:     c,
				Vol:       vol,
			})
		}

		lastTS := result.Data[len(result.Data)-1][0]
		after = lastTS

		if batchSize < 300 {
			break
		}

		time.Sleep(250 * time.Millisecond)
	}

	return allCandles, nil
}

// Suppress unused import warning for math
var _ = math.Abs
