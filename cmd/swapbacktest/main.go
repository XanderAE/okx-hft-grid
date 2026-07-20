package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"
)

// Candle is one completed OKX one-minute candle. Prices are denominated in USDT.
type Candle struct {
	Timestamp int64
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Vol       float64
}

type Direction int

const (
	Flat  Direction = 0
	Long  Direction = 1
	Short Direction = -1
)

func (d Direction) String() string {
	switch d {
	case Long:
		return "long"
	case Short:
		return "short"
	default:
		return "flat"
	}
}

const (
	instrument              = "BTC-USDT-SWAP"
	warmup                  = 20
	maxHoldCandles          = 720 // 12 hours of one-minute candles
	contractBTC             = 0.01
	paperEquity             = 10_000.0
	requestedNotional       = 1_000.0
	maxMarginFraction       = 0.20
	makerFeeRate            = 0.0002
	takerFeeRate            = 0.0005
	marketExitSlippage      = 0.0005
	limitOffset             = 0.1
	fundingIntervalMillis   = int64(8 * time.Hour / time.Millisecond)
	fallbackFundingRate     = 0.0001
	selectionFraction       = 0.70
	minimumCloseMargin      = 0.0005
	maximumCloseMargin      = 0.0008
	minimumHardStop         = 0.015
	maximumHardStop         = 0.030
	minimumOOSTrades        = 3
	minimumOOSFills         = 3
	requiredSevenDayCandles = 7 * 24 * 60
	liquidationSafetyFactor = 0.90
)

// Config contains all execution and risk assumptions used by the local paper backtest.
type Config struct {
	Spread              float64
	Stop                float64
	ConfidenceThreshold float64
	PaperEquity         float64
	RequestedNotional   float64
	MaxMarginFraction   float64
	MakerFee            float64
	TakerFee            float64
	MarketSlippage      float64
	LimitOffset         float64
	MaxHold             int
}

func defaultConfig(spread, stop, confidence float64) Config {
	return Config{
		Spread:              spread,
		Stop:                stop,
		ConfidenceThreshold: confidence,
		PaperEquity:         paperEquity,
		RequestedNotional:   requestedNotional,
		MaxMarginFraction:   maxMarginFraction,
		MakerFee:            makerFeeRate,
		TakerFee:            takerFeeRate,
		MarketSlippage:      marketExitSlippage,
		LimitOffset:         limitOffset,
		MaxHold:             maxHoldCandles,
	}
}

// validateConfig requires a hard stop and initial close/TP spread in their independently safe ranges.
func validateConfig(cfg Config) error {
	if math.IsNaN(cfg.Stop) || math.IsInf(cfg.Stop, 0) || cfg.Stop < minimumHardStop || cfg.Stop > maximumHardStop {
		return fmt.Errorf("hard stop must be finite and within %.2f%%-%.2f%% inclusive; got %v", minimumHardStop*100, maximumHardStop*100, cfg.Stop)
	}
	if math.IsNaN(cfg.Spread) || math.IsInf(cfg.Spread, 0) || cfg.Spread < minimumCloseMargin || cfg.Spread > maximumCloseMargin {
		return fmt.Errorf("initial close/TP spread must be finite and within %.2f%%-%.2f%% inclusive; got %v", minimumCloseMargin*100, maximumCloseMargin*100, cfg.Spread)
	}
	return nil
}

// PendingOrder models a one-candle post-only entry. It is not a fill until a later candle trades through it.
type PendingOrder struct {
	Direction     Direction
	Limit         float64
	Contracts     int
	Leverage      float64
	SubmittedAt   int64
	DecisionIndex int
}

// Position only exists after a passive entry is confirmed filled.
type Position struct {
	Direction     Direction
	Entry         float64
	Contracts     int
	Leverage      float64
	OpenedAt      int64
	OpenedIndex   int
	EntryFee      float64
	Funding       float64
	LastFundingAt int64
	HoldCandles   int
}

type Trade struct {
	Direction Direction
	Entry     float64
	Exit      float64
	Contracts int
	GrossPnL  float64
	Fees      float64
	Funding   float64
	PnL       float64
	Hold      int
	ExitType  string
}

type Result struct {
	Config                 Config
	Trades                 []Trade
	Longs                  int
	Shorts                 int
	EntryFills             int
	EntryCancels           int
	SkippedSignals         int
	OpeningContracts       int
	ClosingContracts       int
	PerContractNotionalMin float64
	PerContractNotionalMax float64
	RealizedPnL            float64
	Fees                   float64
	Funding                float64
	FundingFallbackEvents  int
	HardStops              int
	HardStopRealizedPnL    float64
	MaxDrawdown            float64
	MaxDrawdownPct         float64
	FinalEquity            float64
	ApproxLiquidations     int
	Rejected               bool
	InvalidConfig          bool
	ConfigError            string
	WinRate                float64
	TradesPerDay           float64
}

type candidateEvaluation struct {
	Spread    float64
	Stop      float64
	Selection Result
	OOS       Result
	Passes    bool
	Reason    string
}

type FundingBook struct {
	Rates  map[int64]float64
	Source string
}

func main() {
	fmt.Printf("Fetching local-backtest inputs for %s...\n", instrument)
	candles, err := fetchCandles(instrument, requiredSevenDayCandles)
	if err != nil {
		fmt.Printf("Candle fetch failed: %v\n", err)
		return
	}
	if len(candles) < warmup+20 {
		fmt.Printf("Safety/profit gate: INCONCLUSIVE/FAIL — insufficient data: got %d candles, need at least %d to form a split (and %d complete continuous candles for the 7-day gate).\n", len(candles), warmup+20, requiredSevenDayCandles)
		return
	}

	continuity := checkContinuity(candles)
	sevenDayComplete := hasCompleteSevenDayData(candles, continuity)
	start, end := candles[0].Timestamp, candles[len(candles)-1].Timestamp
	funding, fundingErr := fetchFunding(instrument)
	if fundingErr != nil {
		funding = FundingBook{Rates: map[int64]float64{}, Source: fmt.Sprintf("fallback signed rate %.4f%%/8h (OKX historical funding unavailable: %v)", fallbackFundingRate*100, fundingErr)}
	} else {
		funding.Source = "OKX public historical funding-rate history; signed by actual published rate, with explicit fallback for missing boundaries"
	}

	split := int(float64(len(candles)) * selectionFraction)
	if split < warmup+2 || split >= len(candles)-2 {
		fmt.Println("Safety/profit gate: INCONCLUSIVE/FAIL — cannot form a chronological selection/OOS split from retrieved data.")
		return
	}
	selection := candles[:split]
	oos := candles[split:]
	selectionDays := daysBetween(selection)
	oosDays := daysBetween(oos)

	spreads := []float64{0.0005, 0.0006, 0.0008}
	stops := []float64{0.015, 0.020, 0.030}
	confidences := []float64{0.10, 0.20}
	var selectionResults []Result
	for _, spread := range spreads {
		for _, stop := range stops {
			for _, confidence := range confidences {
				cfg := defaultConfig(spread, stop, confidence)
				if err := validateConfig(cfg); err != nil {
					fmt.Printf("Safety/profit gate: INCONCLUSIVE/FAIL — scan configuration rejected: %v\n", err)
					return
				}
				selectionResults = append(selectionResults, runBacktest(selection, cfg, funding))
			}
		}
	}

	// Selection is based only on the earlier segment. Approximate-liquidation rows are rejected before ranking.
	sort.Slice(selectionResults, func(i, j int) bool {
		if selectionResults[i].Rejected != selectionResults[j].Rejected {
			return !selectionResults[i].Rejected
		}
		return selectionResults[i].RealizedPnL > selectionResults[j].RealizedPnL
	})
	chosen := selectionResults[0]
	oosResult := runBacktest(oos, chosen.Config, funding)

	var candidateComparisons []candidateEvaluation
	for _, spread := range spreads {
		for _, stop := range stops {
			selectionCandidate, found := bestSelectionForParameters(selectionResults, spread, stop)
			if !found {
				continue
			}
			oosCandidate := runBacktest(oos, selectionCandidate.Config, funding)
			passes, reason := passesLimitedGate(sevenDayComplete, selectionCandidate, oosCandidate)
			candidateComparisons = append(candidateComparisons, candidateEvaluation{
				Spread: spread, Stop: stop, Selection: selectionCandidate, OOS: oosCandidate, Passes: passes, Reason: reason,
			})
		}
	}

	fmt.Printf("\n=== CONSERVATIVE BTC-USDT-SWAP PAPER BACKTEST ===\n")
	fmt.Printf("Data: %s to %s; %d one-minute candles; continuity missing=%d non-monotonic=%d; complete 7-day gate data=%t\n",
		time.UnixMilli(start).UTC().Format(time.RFC3339), time.UnixMilli(end).UTC().Format(time.RFC3339), len(candles), continuity.Missing, continuity.NonMonotonic, sevenDayComplete)
	fmt.Printf("Chronological split: selection %s to %s (%d candles, %.2f days, %.0f%%); untouched OOS %s to %s (%d candles, %.2f days, %.0f%%)\n",
		time.UnixMilli(selection[0].Timestamp).UTC().Format(time.RFC3339), time.UnixMilli(selection[len(selection)-1].Timestamp).UTC().Format(time.RFC3339), len(selection), selectionDays, selectionFraction*100,
		time.UnixMilli(oos[0].Timestamp).UTC().Format(time.RFC3339), time.UnixMilli(oos[len(oos)-1].Timestamp).UTC().Format(time.RFC3339), len(oos), oosDays, (1-selectionFraction)*100)
	fmt.Printf("Hard-stop validation: every value must be finite and within %.2f%%-%.2f%% inclusive. Scanned hard stops: 1.50%%, 2.00%%, 3.00%%.\n", minimumHardStop*100, maximumHardStop*100)
	fmt.Printf("Initial close/TP spread validation: every value must be finite and within %.2f%%-%.2f%% inclusive. Scanned spreads: 0.05%%, 0.06%%, 0.08%%.\n", minimumCloseMargin*100, maximumCloseMargin*100)
	fmt.Printf("Assumptions: paper equity=$%.0f; requested notional=$%.0f; integer contracts only (1=%0.2f BTC); max initial margin=%.0f%% equity; maker=%.03f%%; taker=%.03f%%; forced-exit slippage=%.03f%%\n",
		paperEquity, requestedNotional, contractBTC, maxMarginFraction*100, makerFeeRate*100, takerFeeRate*100, marketExitSlippage*100)
	fmt.Printf("Execution: decision uses completed candle i-1; post-only limit uses its close proxy +/- $%.1f and can first fill in candle i; unfilled entry cancels after that eligible candle; stop/target collisions are stop-first. On a stop gap, exit uses the adverse observed candle extreme plus slippage, never the trigger alone. %s\n", limitOffset, funding.Source)
	fmt.Printf("Liquidation: approximate isolated-margin screen at %.0f%% of 1/leverage; any event rejects a row (not a proof of zero liquidation risk).\n", liquidationSafetyFactor*100)
	fmt.Printf("Limited gate: complete continuous 7-day input; positive selection and OOS realized PnL; at least %d OOS fills and %d OOS completed trades; zero approximate liquidations in both periods.\n", minimumOOSFills, minimumOOSTrades)
	printStopCostMath(0.015)
	printTakeProfitMarginMath(0.0005)
	printTakeProfitMarginMath(0.0008)

	fmt.Println("\nSelection ranking (not a recommendation; OOS was not used to choose):")
	for i := 0; i < len(selectionResults) && i < 6; i++ {
		printResult(fmt.Sprintf("selection #%d", i+1), selectionResults[i], selectionDays)
	}
	fmt.Println("\nCandidate comparison: for every initial close/TP spread and hard stop, confidence is selected only on the selection period, then evaluated once on untouched OOS.")
	for _, spread := range spreads {
		fmt.Printf("\nRequested initial close/TP spread %.2f%% — reported separately:\n", spread*100)
		for _, comparison := range candidateComparisons {
			if comparison.Spread == spread {
				printCandidateComparison(comparison, selectionDays, oosDays)
			}
		}
	}

	fmt.Println("\nOverall selection winner (not selected using OOS):")
	printResult("chosen selection setting", chosen, selectionDays)
	printResult("untouched OOS", oosResult, oosDays)
	passes, reason := passesLimitedGate(sevenDayComplete, chosen, oosResult)
	if passes {
		fmt.Println("Safety/profit gate: PASS for this limited local sample only; this is not a deployment recommendation.")
	} else {
		fmt.Printf("Safety/profit gate: INCONCLUSIVE/FAIL — %s. Do not treat this backtest as a production recommendation.\n", reason)
	}
}

func printResult(label string, r Result, days float64) {
	status := "eligible"
	if r.InvalidConfig {
		status = "INVALID CONFIG: " + r.ConfigError
	} else if r.Rejected {
		status = "REJECTED: approximate-liquidation event"
	}
	returnPct := 0.0
	if r.Config.PaperEquity > 0 {
		returnPct = r.RealizedPnL / r.Config.PaperEquity * 100
	}
	perContract := "n/a (no fills)"
	if r.EntryFills > 0 {
		perContract = fmt.Sprintf("$%.2f-$%.2f", r.PerContractNotionalMin, r.PerContractNotionalMax)
	}
	fmt.Printf("%s: spread=%.2f%% trigger-stop=%.2f%% confidence=%.0f%% | %s\n", label, r.Config.Spread*100, r.Config.Stop*100, r.Config.ConfidenceThreshold*100, status)
	fmt.Printf("  trades=%d (long=%d short=%d, win=%.1f%%, %.2f/day); fills=%d cancels=%d skips(<1 contract/risk)=%d; contracts open/close=%d/%d; actual per-contract notional=%s\n",
		len(r.Trades), r.Longs, r.Shorts, r.WinRate, tradesPerDay(r, days), r.EntryFills, r.EntryCancels, r.SkippedSignals, r.OpeningContracts, r.ClosingContracts, perContract)
	fmt.Printf("  realized PnL=%+.2f USDT (%.3f%% paper equity); final equity=%.2f; max drawdown=%.2f (%.3f%%); fees=%.2f; funding=%+.2f (fallback boundaries=%d); approximate-liquidations=%d\n",
		r.RealizedPnL, returnPct, r.FinalEquity, r.MaxDrawdown, r.MaxDrawdownPct, r.Fees, r.Funding, r.FundingFallbackEvents, r.ApproxLiquidations)
	fmt.Printf("  hard-stop triggers=%d; realized hard-stop PnL=%+.4f USDT (entry maker fee + exit taker fee + adverse slippage included; funding included when crossed)\n",
		r.HardStops, r.HardStopRealizedPnL)
}

func printCandidateComparison(comparison candidateEvaluation, selectionDays, oosDays float64) {
	fmt.Printf("\n--- initial close/TP spread %.2f%%; hard stop %.2f%% ---\n", comparison.Spread*100, comparison.Stop*100)
	printResult("selection-best", comparison.Selection, selectionDays)
	printResult("untouched OOS", comparison.OOS, oosDays)
	if comparison.Passes {
		fmt.Println("  limited gate: PASS (limited local sample only; not a production recommendation)")
	} else {
		fmt.Printf("  limited gate: INCONCLUSIVE/FAIL — %s\n", comparison.Reason)
	}
}

func printStopCostMath(stop float64) {
	longExit, longLoss := modeledNoGapStopLoss(Long, stop, makerFeeRate, takerFeeRate, marketExitSlippage)
	shortExit, shortLoss := modeledNoGapStopLoss(Short, stop, makerFeeRate, takerFeeRate, marketExitSlippage)
	fmt.Printf("%.2f%% hard stop versus modeled realized loss (no gap, entry=100): long trigger=%.5f, forced exit=%.5f, loss=%.5f%%; short trigger=%.5f, forced exit=%.5f, loss=%.5f%%. Loss = adverse price move to trigger, then %.03f%% forced-exit slippage, plus %.03f%% entry maker fee and %.03f%% exit taker fee; gaps use the worse observed extreme, so realized loss can be larger.\n",
		stop*100, (1-stop)*100, longExit*100, longLoss*100, (1+stop)*100, shortExit*100, shortLoss*100, marketExitSlippage*100, makerFeeRate*100, takerFeeRate*100)
}

func printTakeProfitMarginMath(spread float64) {
	_, gross, longFees, longNet := modeledTakeProfitNetMargin(Long, spread, makerFeeRate)
	_, _, shortFees, shortNet := modeledTakeProfitNetMargin(Short, spread, makerFeeRate)
	nominalFees := 2 * makerFeeRate
	nominalNet := gross - nominalFees
	fmt.Printf("TP spread %.2f%% is not fee-free: gross=%.3f%%; nominal maker entry + maker TP fees=%.3f%%; nominal net=%.3f%%. Exact notional-adjusted net: long=%.5f%% (fees=%.5f%%), short=%.5f%% (fees=%.5f%%), before funding, rounding, queue failure, or adverse fills.\n",
		spread*100, gross*100, nominalFees*100, nominalNet*100, longNet*100, longFees*100, shortNet*100, shortFees*100)
}

// modeledTakeProfitNetMargin calculates a maker-entry and maker-TP close margin as a fraction of entry notional.
func modeledTakeProfitNetMargin(dir Direction, spread, makerFee float64) (target, grossMargin, fees, netMargin float64) {
	target = 1 + float64(dir)*spread
	grossMargin = float64(dir) * (target - 1)
	fees = makerFee + target*makerFee
	netMargin = grossMargin - fees
	return target, grossMargin, fees, netMargin
}

// modeledNoGapStopLoss expresses the all-in loss fraction when a candle reaches, but does not gap beyond, a stop.
func modeledNoGapStopLoss(dir Direction, stop, makerFee, takerFee, slippage float64) (exit, lossFraction float64) {
	entry := 1.0
	trigger := entry * (1 - stop)
	if dir == Short {
		trigger = entry * (1 + stop)
	}
	exit = adverseMarketExit(dir, trigger, slippage)
	gross := float64(dir) * (exit - entry)
	fees := makerFee + exit*takerFee
	return exit, -(gross - fees)
}

func bestSelectionForParameters(results []Result, spread, stop float64) (Result, bool) {
	for _, result := range results {
		if result.Config.Spread == spread && result.Config.Stop == stop {
			return result, true // results are already selection-ranked with rejected rows last.
		}
	}
	return Result{}, false
}

func hasCompleteSevenDayData(candles []Candle, continuity continuityCheck) bool {
	return len(candles) >= requiredSevenDayCandles && continuity.Missing == 0 && continuity.NonMonotonic == 0
}

func passesLimitedGate(sevenDayComplete bool, selection, oos Result) (bool, string) {
	if !sevenDayComplete {
		return false, fmt.Sprintf("7-day input is insufficient or discontinuous (need %d continuous one-minute candles)", requiredSevenDayCandles)
	}
	if selection.InvalidConfig || oos.InvalidConfig {
		return false, "invalid stop configuration"
	}
	if selection.Rejected || oos.Rejected || selection.ApproxLiquidations != 0 || oos.ApproxLiquidations != 0 {
		return false, "approximate liquidation occurred"
	}
	if selection.RealizedPnL <= 0 {
		return false, "selection realized PnL is not positive"
	}
	if oos.RealizedPnL <= 0 {
		return false, "OOS realized PnL is not positive"
	}
	if oos.EntryFills < minimumOOSFills {
		return false, fmt.Sprintf("OOS fills %d are below the minimum %d", oos.EntryFills, minimumOOSFills)
	}
	if len(oos.Trades) < minimumOOSTrades {
		return false, fmt.Sprintf("OOS completed trades %d are below the minimum %d", len(oos.Trades), minimumOOSTrades)
	}
	return true, ""
}

func tradesPerDay(r Result, days float64) float64 {
	if days <= 0 {
		return 0
	}
	return float64(len(r.Trades)) / days
}

// runBacktest performs a chronological, conservative simulation without current-candle look-ahead.
func runBacktest(candles []Candle, cfg Config, funding FundingBook) Result {
	if err := validateConfig(cfg); err != nil {
		return Result{Config: cfg, FinalEquity: cfg.PaperEquity, InvalidConfig: true, ConfigError: err.Error()}
	}
	result := Result{Config: cfg, FinalEquity: cfg.PaperEquity, PerContractNotionalMin: math.Inf(1)}
	if len(candles) <= warmup {
		return result
	}
	closes := candleCloses(candles)
	emaShort := computeEMA(closes, 5)
	emaLong := computeEMA(closes, 20)
	cash := cfg.PaperEquity
	peakEquity := cash
	var pending *PendingOrder
	var position *Position

	for i, candle := range candles {
		// A pending order was decided at the end of i-1. It can only be evaluated now.
		if pending != nil {
			if eligibleForFill(*pending, candle) && passiveEntryFilled(*pending, candle) {
				entryNotional := float64(pending.Contracts) * contractBTC * pending.Limit
				entryFee := entryNotional * cfg.MakerFee
				cash -= entryFee
				position = &Position{
					Direction:     pending.Direction,
					Entry:         pending.Limit,
					Contracts:     pending.Contracts,
					Leverage:      pending.Leverage,
					OpenedAt:      candle.Timestamp,
					OpenedIndex:   i,
					EntryFee:      entryFee,
					LastFundingAt: candle.Timestamp,
				}
				result.EntryFills++
				result.OpeningContracts += pending.Contracts
				if pending.Direction == Long {
					result.Longs++
				} else {
					result.Shorts++
				}
				perContractNotional := entryNotional / float64(pending.Contracts)
				if perContractNotional < result.PerContractNotionalMin {
					result.PerContractNotionalMin = perContractNotional
				}
				if perContractNotional > result.PerContractNotionalMax {
					result.PerContractNotionalMax = perContractNotional
				}
			} else {
				result.EntryCancels++
			}
			pending = nil // one eligible candle only; unfilled post-only intent is explicitly cancelled
		}

		// Do not infer an exit after an intrabar entry: subsequent candles manage the position.
		if position != nil && position.OpenedIndex < i {
			position.HoldCandles++
			cash, result = bookFunding(cash, result, position, candle.Timestamp, funding)
			exitType, exitPrice := decideExit(*position, candle, cfg)
			if exitType != "" {
				trade := closePosition(*position, exitPrice, exitType, cfg)
				cash += trade.GrossPnL - (trade.Fees - position.EntryFee)
				result.Trades = append(result.Trades, trade)
				result.ClosingContracts += position.Contracts
				result.Fees += trade.Fees
				result.Funding += trade.Funding
				if exitType == "hard-stop" {
					result.HardStops++
					result.HardStopRealizedPnL += trade.PnL
				}
				if exitType == "approx-liquidation" {
					result.ApproxLiquidations++
					result.Rejected = true
				}
				position = nil
			}
		}

		// Equity is marked every candle. Cash already includes entry fees and funding cash flows.
		equity := cash
		if position != nil {
			equity += unrealizedPnL(*position, candle.Close)
		}
		if equity > peakEquity {
			peakEquity = equity
		}
		dd := peakEquity - equity
		if dd > result.MaxDrawdown {
			result.MaxDrawdown = dd
		}
		if peakEquity > 0 && dd/peakEquity*100 > result.MaxDrawdownPct {
			result.MaxDrawdownPct = dd / peakEquity * 100
		}

		// This decision sees only the completed current candle. Its order is first eligible in i+1.
		if i >= warmup && position == nil && pending == nil && i < len(candles)-1 {
			dir, confidence := emaSignal(emaShort, emaLong, closes, i)
			if dir != Flat && confidence >= cfg.ConfidenceThreshold {
				limit := candle.Close - cfg.LimitOffset
				if dir == Short {
					limit = candle.Close + cfg.LimitOffset
				}
				leverage := leverageForDecision(closes, i, emaShort, emaLong)
				contracts := contractCount(cfg, limit*contractBTC, leverage)
				if contracts < 1 {
					result.SkippedSignals++
				} else {
					pending = &PendingOrder{
						Direction:     dir,
						Limit:         limit,
						Contracts:     contracts,
						Leverage:      leverage,
						SubmittedAt:   candle.Timestamp,
						DecisionIndex: i,
					}
				}
			}
		}
	}

	if pending != nil {
		result.EntryCancels++
	}
	if position != nil {
		last := candles[len(candles)-1]
		forced := adverseMarketExit(position.Direction, last.Close, cfg.MarketSlippage)
		trade := closePosition(*position, forced, "end-of-data", cfg)
		cash += trade.GrossPnL - (trade.Fees - position.EntryFee)
		result.Trades = append(result.Trades, trade)
		result.ClosingContracts += position.Contracts
		result.Fees += trade.Fees
		result.Funding += trade.Funding
	}

	result.FinalEquity = cash
	result.RealizedPnL = cash - cfg.PaperEquity
	if result.PerContractNotionalMin == math.Inf(1) {
		result.PerContractNotionalMin = 0
	}
	if len(result.Trades) > 0 {
		wins := 0
		for _, trade := range result.Trades {
			if trade.PnL > 0 {
				wins++
			}
		}
		result.WinRate = float64(wins) / float64(len(result.Trades)) * 100
	}
	return result
}

func candleLimitNotional(order PendingOrder) float64 { return order.Limit * contractBTC }

func eligibleForFill(order PendingOrder, candle Candle) bool {
	return candle.Timestamp > order.SubmittedAt
}

func passiveEntryFilled(order PendingOrder, candle Candle) bool {
	if order.Direction == Long {
		return candle.Low <= order.Limit
	}
	return candle.High >= order.Limit
}

func contractCount(cfg Config, perContractNotional, leverage float64) int {
	if perContractNotional <= 0 || leverage <= 0 {
		return 0
	}
	contracts := int(math.Floor(cfg.RequestedNotional / perContractNotional))
	for contracts > 0 && (float64(contracts)*perContractNotional/leverage > cfg.PaperEquity*cfg.MaxMarginFraction) {
		contracts--
	}
	return contracts
}

func leverageForDecision(closes []float64, index int, shortEMA, longEMA []float64) float64 {
	_, confidence := emaSignal(shortEMA, longEMA, closes, index)
	return clamp(1+confidence*2-realizedVol(closes, index, 20)*40, 1, 3)
}

func decideExit(p Position, candle Candle, cfg Config) (string, float64) {
	adverseMove := (p.Entry - candle.Low) / p.Entry
	if p.Direction == Short {
		adverseMove = (candle.High - p.Entry) / p.Entry
	}
	if adverseMove >= (1/p.Leverage)*liquidationSafetyFactor {
		base := candle.Low
		if p.Direction == Short {
			base = candle.High
		}
		return "approx-liquidation", adverseMarketExit(p.Direction, base, cfg.MarketSlippage)
	}

	stop := p.Entry * (1 - cfg.Stop)
	target := p.Entry * (1 + closeMargin(cfg.Spread, p.HoldCandles))
	if p.Direction == Short {
		stop = p.Entry * (1 + cfg.Stop)
		target = p.Entry * (1 - closeMargin(cfg.Spread, p.HoldCandles))
	}
	stopHit := (p.Direction == Long && candle.Low <= stop) || (p.Direction == Short && candle.High >= stop)
	targetHit := (p.Direction == Long && candle.High >= target) || (p.Direction == Short && candle.Low <= target)
	// An OHLC candle cannot order competing touches; choose the adverse stop before any favorable target.
	if stopHit {
		base := math.Min(stop, candle.Low)
		if p.Direction == Short {
			base = math.Max(stop, candle.High)
		}
		return "hard-stop", adverseMarketExit(p.Direction, base, cfg.MarketSlippage)
	}
	if targetHit {
		return "take-profit-maker", target
	}
	if p.HoldCandles >= cfg.MaxHold {
		return "max-hold", adverseMarketExit(p.Direction, candle.Close, cfg.MarketSlippage)
	}
	return "", 0
}

func adverseMarketExit(dir Direction, mark, slippage float64) float64 {
	if dir == Long {
		return mark * (1 - slippage)
	}
	return mark * (1 + slippage)
}

func closePosition(p Position, exit float64, exitType string, cfg Config) Trade {
	notionalExit := float64(p.Contracts) * contractBTC * exit
	exitFeeRate := cfg.MakerFee
	if exitType != "take-profit-maker" {
		exitFeeRate = cfg.TakerFee
	}
	gross := float64(p.Direction) * float64(p.Contracts) * contractBTC * (exit - p.Entry)
	exitFee := notionalExit * exitFeeRate
	fees := p.EntryFee + exitFee
	return Trade{
		Direction: p.Direction, Entry: p.Entry, Exit: exit, Contracts: p.Contracts,
		GrossPnL: gross, Fees: fees, Funding: p.Funding, PnL: gross - fees + p.Funding,
		Hold: p.HoldCandles, ExitType: exitType,
	}
}

func unrealizedPnL(p Position, mark float64) float64 {
	return float64(p.Direction) * float64(p.Contracts) * contractBTC * (mark - p.Entry)
}

func bookFunding(cash float64, result Result, p *Position, until int64, funding FundingBook) (float64, Result) {
	for _, boundary := range fundingBoundariesCrossed(p.LastFundingAt, until) {
		rate, found := funding.Rates[boundary]
		if !found {
			rate = fallbackFundingRate
			result.FundingFallbackEvents++
		}
		notional := float64(p.Contracts) * contractBTC * p.Entry
		// Positive OKX funding: longs pay and shorts receive. Negative funding reverses that direction.
		cashflow := -float64(p.Direction) * notional * rate
		cash += cashflow
		p.Funding += cashflow
		p.LastFundingAt = boundary
	}
	return cash, result
}

func fundingBoundariesCrossed(previous, current int64) []int64 {
	if current <= previous {
		return nil
	}
	first := (previous/fundingIntervalMillis + 1) * fundingIntervalMillis
	var boundaries []int64
	for boundary := first; boundary <= current; boundary += fundingIntervalMillis {
		boundaries = append(boundaries, boundary)
	}
	return boundaries
}

func closeMargin(spread float64, hold int) float64 {
	margin := spread
	if hold > 60 && hold <= 360 {
		margin = spread * 0.8
	} else if hold > 360 {
		margin = spread * 0.67
	}
	if margin < minimumCloseMargin {
		margin = minimumCloseMargin
	}
	return margin
}

func emaSignal(shortEMA, longEMA, closes []float64, index int) (Direction, float64) {
	if index < 2 || index >= len(closes) || longEMA[index] <= 0 {
		return Flat, 0
	}
	gap := shortEMA[index] - longEMA[index]
	confidence := clamp(math.Abs(gap)/longEMA[index]*50, 0, 1)
	if shortEMA[index] > longEMA[index] && shortEMA[index] > shortEMA[index-1] {
		return Long, confidence
	}
	if shortEMA[index] < longEMA[index] && shortEMA[index] < shortEMA[index-1] {
		return Short, confidence
	}
	return Flat, 0
}

func candleCloses(candles []Candle) []float64 {
	closes := make([]float64, len(candles))
	for i, candle := range candles {
		closes[i] = candle.Close
	}
	return closes
}

func computeEMA(data []float64, period int) []float64 {
	ema := make([]float64, len(data))
	if len(data) == 0 {
		return ema
	}
	multiplier := 2.0 / float64(period+1)
	ema[0] = data[0]
	for i := 1; i < len(data); i++ {
		ema[i] = (data[i]-ema[i-1])*multiplier + ema[i-1]
	}
	return ema
}

func realizedVol(closes []float64, index, window int) float64 {
	start := index - window
	if start < 1 {
		start = 1
	}
	var returns []float64
	for i := start; i <= index && i < len(closes); i++ {
		if closes[i-1] > 0 && closes[i] > 0 {
			returns = append(returns, math.Log(closes[i]/closes[i-1]))
		}
	}
	if len(returns) < 2 {
		return 0
	}
	mean := 0.0
	for _, value := range returns {
		mean += value
	}
	mean /= float64(len(returns))
	variance := 0.0
	for _, value := range returns {
		variance += (value - mean) * (value - mean)
	}
	return math.Sqrt(variance / float64(len(returns)-1))
}

func clamp(value, low, high float64) float64 {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func daysBetween(candles []Candle) float64 {
	if len(candles) < 2 {
		return 0
	}
	return float64(candles[len(candles)-1].Timestamp-candles[0].Timestamp) / float64(24*time.Hour/time.Millisecond)
}

type continuityCheck struct{ Missing, NonMonotonic int }

func checkContinuity(candles []Candle) continuityCheck {
	var check continuityCheck
	for i := 1; i < len(candles); i++ {
		delta := candles[i].Timestamp - candles[i-1].Timestamp
		if delta <= 0 {
			check.NonMonotonic++
			continue
		}
		if delta > int64(time.Minute/time.Millisecond) {
			check.Missing += int(delta/int64(time.Minute/time.Millisecond)) - 1
		}
	}
	return check
}

// fetchCandles retrieves completed 1m public candles, deduplicates them and sorts chronologically.
func fetchCandles(instID string, target int) ([]Candle, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	var all []Candle
	var after string
	for len(all) < target {
		batch, err := fetchCandlePage(client, instID, after)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		all = append(all, batch...)
		oldest := batch[0].Timestamp
		for _, candle := range batch[1:] {
			if candle.Timestamp < oldest {
				oldest = candle.Timestamp
			}
		}
		previousAfter := after
		after = strconv.FormatInt(oldest, 10)
		if after == previousAfter {
			break // endpoint made no pagination progress
		}
		time.Sleep(250 * time.Millisecond)
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("OKX returned no candles")
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Timestamp < all[j].Timestamp })
	deduped := all[:0]
	for _, candle := range all {
		if len(deduped) == 0 || candle.Timestamp != deduped[len(deduped)-1].Timestamp {
			deduped = append(deduped, candle)
		}
	}
	if len(deduped) > target {
		deduped = deduped[len(deduped)-target:]
	}
	return deduped, nil
}

func fetchCandlePage(client *http.Client, instID, after string) ([]Candle, error) {
	endpoint := "https://www.okx.com/api/v5/market/history-candles"
	if after == "" {
		endpoint = "https://www.okx.com/api/v5/market/candles"
	}
	query := url.Values{"instId": {instID}, "bar": {"1m"}, "limit": {"300"}}
	if after != "" {
		query.Set("after", after)
	}
	response, err := client.Get(endpoint + "?" + query.Encode())
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Code string     `json:"code"`
		Msg  string     `json:"msg"`
		Data [][]string `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if payload.Code != "0" {
		return nil, fmt.Errorf("OKX candles: code=%s msg=%s", payload.Code, payload.Msg)
	}
	candles := make([]Candle, 0, len(payload.Data))
	for _, row := range payload.Data {
		if len(row) < 6 || (len(row) >= 9 && row[8] != "1") { // discard an unfinished current candle when supplied
			continue
		}
		timestamp, err := strconv.ParseInt(row[0], 10, 64)
		if err != nil {
			continue
		}
		values := make([]float64, 5)
		valid := true
		for i := range values {
			values[i], err = strconv.ParseFloat(row[i+1], 64)
			if err != nil {
				valid = false
				break
			}
		}
		if valid {
			candles = append(candles, Candle{Timestamp: timestamp, Open: values[0], High: values[1], Low: values[2], Close: values[3], Vol: values[4]})
		}
	}
	return candles, nil
}

// fetchFunding obtains public historical rates. The model uses fallback only for a crossed boundary not returned by OKX.
func fetchFunding(instID string) (FundingBook, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	endpoint := "https://www.okx.com/api/v5/public/funding-rate-history"
	query := url.Values{"instId": {instID}, "limit": {"100"}}
	response, err := client.Get(endpoint + "?" + query.Encode())
	if err != nil {
		return FundingBook{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return FundingBook{}, err
	}
	var payload struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			FundingRate string `json:"fundingRate"`
			FundingTime string `json:"fundingTime"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return FundingBook{}, err
	}
	if payload.Code != "0" {
		return FundingBook{}, fmt.Errorf("OKX funding: code=%s msg=%s", payload.Code, payload.Msg)
	}
	book := FundingBook{Rates: make(map[int64]float64)}
	for _, row := range payload.Data {
		timestamp, timestampErr := strconv.ParseInt(row.FundingTime, 10, 64)
		rate, rateErr := strconv.ParseFloat(row.FundingRate, 64)
		if timestampErr == nil && rateErr == nil {
			book.Rates[timestamp] = rate
		}
	}
	if len(book.Rates) == 0 {
		return FundingBook{}, fmt.Errorf("OKX returned no parseable funding rates")
	}
	return book, nil
}
