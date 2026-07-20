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

// InstrumentConfig holds per-instrument parameters.
type InstrumentConfig struct {
	InstID   string
	TickSize float64
}

// Trade records a completed round-trip trade.
type Trade struct {
	BuyPrice    float64
	SellPrice   float64
	Quantity    float64
	QtyAfterFee float64
	PnL         float64
	HoldTicks   int
	ExitType    string // "profit", "stopped", "timedout", "skewed"
}

const (
	fee          = 0.0008
	tradeSize    = 50.0
	warmupTicks  = 4
	stopLossPct  = 0.015
	maxHoldTicks = 720 // >12h at 30s ticks
)

func main() {
	instruments := []InstrumentConfig{
		{InstID: "DOGE-USDT", TickSize: 0.00001},
		{InstID: "WIF-USDT", TickSize: 0.0001},
	}

	for _, inst := range instruments {
		fmt.Printf("Fetching candle data for %s...\n", inst.InstID)
		candles, err := fetchCandles(inst.InstID, 10080)
		if err != nil {
			fmt.Printf("Error fetching candles for %s: %v\n", inst.InstID, err)
			continue
		}
		fmt.Printf("Fetched %d candles for %s\n", len(candles), inst.InstID)
		if len(candles) < 10 {
			fmt.Printf("Not enough candle data for %s, skipping\n", inst.InstID)
			continue
		}
		runBacktest(inst, candles)
		fmt.Println()
	}
}

// fetchCandles retrieves historical 1m candles from OKX public API.
// Uses /market/history-candles for deeper history, then /market/candles for recent data.
// OKX returns newest first, so we paginate with "after" parameter (timestamp to get candles before).
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
		// Find oldest timestamp from what we have
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
			break // no more data available
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

		// OKX returns newest first; last element in batch is oldest.
		// "after" = oldest timestamp to get even older candles next iteration.
		lastTS := result.Data[len(result.Data)-1][0]
		after = lastTS

		// If we got fewer than requested, no more history
		if batchSize < 300 {
			break
		}

		// Rate limit: be polite to OKX
		time.Sleep(250 * time.Millisecond)
	}

	return allCandles, nil
}

// Tick represents a simulated market tick derived from a candle.
type Tick struct {
	Price   float64 // price at this moment (open or close)
	BestBid float64 // simulated best bid
	BestAsk float64 // simulated best ask
	High    float64 // candle high (for sell fill simulation)
	Low     float64 // candle low (for buy fill simulation)
}

// runBacktest runs the full strategy simulation on the given candles.
func runBacktest(inst InstrumentConfig, candles []Candle) {
	// Generate ticks: 1 per candle at close price.
	// High/Low retained for fill simulation.
	// This gives 1 tick per minute, approximately matching the 30s rebalancer interval.
	var ticks []Tick
	for _, c := range candles {
		ticks = append(ticks, Tick{
			Price:   c.Close,
			BestBid: c.Close,
			BestAsk: c.Close,
			High:    c.High,
			Low:     c.Low,
		})
	}

	startTime := time.UnixMilli(candles[0].Timestamp)
	endTime := time.UnixMilli(candles[len(candles)-1].Timestamp)

	// State
	var (
		trades         []Trade
		prices         []float64
		holding        bool
		buyPrice       float64
		quantity       float64
		qtyAfterFee   float64
		holdTicks      int
		downSkips      int
		balance        = 100.0
		maxBalance     = 100.0
		maxDrawdown    = 0.0
		totalHoldTicks int
		pendingBuy     bool
		pendingBuyPx   float64
	)

	for i, tick := range ticks {
		currentPrice := tick.Price

		// Track prices for momentum
		prices = append(prices, currentPrice)

		// Phase 1: Warmup
		if i < warmupTicks {
			continue
		}

		if holding {
			holdTicks++

			// Phase 5: Hard stop
			if currentPrice <= buyPrice*(1-stopLossPct) {
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
				totalHoldTicks += holdTicks
				holding = false
				pendingBuy = false
				trackDrawdown(&balance, &maxBalance, &maxDrawdown)
				continue
			}

			// Phase 4: SELL management
			var sellTarget float64
			exitType := "profit"

			if holdTicks > maxHoldTicks {
				// Force market sell after 12h
				sellTarget = currentPrice
				exitType = "timedout"
			} else if currentPrice < buyPrice {
				// Price underwater: skewed exit at +0.15%
				sellTarget = buyPrice * 1.0015
				exitType = "skewed"
			} else if holdTicks <= 60 {
				// 0-1h: +0.3%
				sellTarget = buyPrice * 1.003
			} else if holdTicks <= 360 {
				// 1-6h: +0.25%
				sellTarget = buyPrice * 1.0025
			} else if holdTicks <= 720 {
				// 6-12h: +0.2%
				sellTarget = buyPrice * 1.002
			} else {
				sellTarget = currentPrice
				exitType = "timedout"
			}

			// Check fill: for timed-out we force fill; otherwise candle high >= sellTarget
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
				totalHoldTicks += holdTicks
				holding = false
				pendingBuy = false
				trackDrawdown(&balance, &maxBalance, &maxDrawdown)
			}
			continue
		}

		// Check if pending buy fills on this tick
		if pendingBuy {
			// BUY fills if candle low <= our buy price
			if tick.Low <= pendingBuyPx {
				// Phase 3: BUY fill
				buyPrice = pendingBuyPx
				quantity = tradeSize / buyPrice
				qtyAfterFee = quantity * (1 - fee)
				holding = true
				holdTicks = 0
				pendingBuy = false
				continue
			}
		}

		// Phase 2: BUY decision (not holding)
		// Momentum filter: if last 3 prices are declining → skip
		if len(prices) >= 3 {
			n := len(prices)
			if prices[n-1] < prices[n-2] && prices[n-2] < prices[n-3] {
				downSkips++
				pendingBuy = false
				continue
			}
		}

		// Place BUY limit at bestBid - 1 tick
		pendingBuyPx = tick.BestBid - inst.TickSize
		pendingBuy = true

		// Immediately check if it fills on this same tick
		if tick.Low <= pendingBuyPx {
			buyPrice = pendingBuyPx
			quantity = tradeSize / buyPrice
			qtyAfterFee = quantity * (1 - fee)
			holding = true
			holdTicks = 0
			pendingBuy = false
		}
	}

	// If still holding at end, force close at last price
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
		totalHoldTicks += holdTicks
		trackDrawdown(&balance, &maxBalance, &maxDrawdown)
	}

	// Generate report
	printReport(inst.InstID, startTime, endTime, trades, downSkips, totalHoldTicks, balance, maxDrawdown)
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

func printReport(instID string, start, end time.Time, trades []Trade, downSkips int, totalHoldTicks int, finalBalance, maxDrawdown float64) {
	fmt.Printf("\n=== BACKTEST REPORT: %s (7 days) ===\n", instID)
	fmt.Printf("Period: %s to %s\n", start.UTC().Format("2006-01-02 15:04"), end.UTC().Format("2006-01-02 15:04"))
	fmt.Printf("Starting balance: $100.00\n\n")

	totalTrades := len(trades)
	var profitable, stopped, timedout, skewed int
	var profitSum, stoppedSum, timedoutSum float64

	for _, t := range trades {
		switch t.ExitType {
		case "profit":
			profitable++
			profitSum += t.PnL
		case "stopped":
			stopped++
			stoppedSum += t.PnL
		case "timedout":
			timedout++
			timedoutSum += t.PnL
		case "skewed":
			skewed++
			profitSum += t.PnL // count skewed P&L with profitable trades
		}
	}

	fmt.Printf("Total trades completed: %d\n", totalTrades)
	if profitable > 0 {
		fmt.Printf("  - Profitable: %d (avg +$%.4f per trade)\n", profitable, profitSum/float64(profitable))
	} else {
		fmt.Printf("  - Profitable: 0\n")
	}
	if stopped > 0 {
		fmt.Printf("  - Stopped out (1.5%%): %d (avg -$%.4f per trade)\n", stopped, math.Abs(stoppedSum/float64(stopped)))
	} else {
		fmt.Printf("  - Stopped out (1.5%%): 0\n")
	}
	if timedout > 0 {
		avgTO := timedoutSum / float64(timedout)
		sign := "+"
		if avgTO < 0 {
			sign = "-"
			avgTO = math.Abs(avgTO)
		}
		fmt.Printf("  - Timed out (12h): %d (avg %s$%.4f per trade)\n", timedout, sign, avgTO)
	} else {
		fmt.Printf("  - Timed out (12h): 0\n")
	}
	fmt.Printf("  - Skewed exit (+0.15%%): %d\n\n", skewed)

	fmt.Printf("Momentum filter:\n")
	fmt.Printf("  - Downtrend skips: %d times\n", downSkips)
	fmt.Printf("  - Trades avoided by filter: ~%d\n\n", downSkips/3)

	totalPnL := finalBalance - 100.0
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

	avgHoldMin := 0.0
	if totalTrades > 0 {
		// Each tick = 1 minute (one candle per tick)
		avgHoldMin = float64(totalHoldTicks) / float64(totalTrades)
	}

	days := end.Sub(start).Hours() / 24.0
	tradesPerDay := 0.0
	if days > 0 {
		tradesPerDay = float64(totalTrades) / days
	}

	fmt.Printf("Performance:\n")
	fmt.Printf("  - Total P&L: $%.4f\n", totalPnL)
	fmt.Printf("  - Win rate: %.1f%%\n", winRate)
	fmt.Printf("  - Max drawdown: $%.4f\n", maxDrawdown)
	fmt.Printf("  - Avg holding time: %.1f minutes\n", avgHoldMin)
	fmt.Printf("  - Trades per day: %.1f\n\n", tradesPerDay)

	fmt.Printf("Final balance: $%.4f\n", finalBalance)
	fmt.Printf("Return: %.2f%%\n", (finalBalance-100.0)/100.0*100)
}
