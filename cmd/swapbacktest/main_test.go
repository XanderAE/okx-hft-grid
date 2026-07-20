package main

import (
	"fmt"
	"math"
	"testing"
	"time"
)

func TestNextCandleOnlyExecution(t *testing.T) {
	order := PendingOrder{Direction: Long, Limit: 99, SubmittedAt: 60_000, DecisionIndex: 0}
	sameCandle := Candle{Timestamp: 60_000, Low: 98, High: 101}
	if eligibleForFill(order, sameCandle) {
		t.Fatal("order submitted on a candle must not be eligible to fill on that same candle")
	}
	nextCandle := Candle{Timestamp: 120_000, Low: 98, High: 101}
	if !eligibleForFill(order, nextCandle) || !passiveEntryFilled(order, nextCandle) {
		t.Fatal("a later candle that trades through the posted limit should be eligible to fill")
	}
}

func TestPassiveEntryIsNotGuaranteedToFill(t *testing.T) {
	long := PendingOrder{Direction: Long, Limit: 99, SubmittedAt: 60_000}
	short := PendingOrder{Direction: Short, Limit: 101, SubmittedAt: 60_000}
	candle := Candle{Timestamp: 120_000, Low: 99.5, High: 100.5}
	if passiveEntryFilled(long, candle) {
		t.Fatal("long limit below candle low must remain unfilled")
	}
	if passiveEntryFilled(short, candle) {
		t.Fatal("short limit above candle high must remain unfilled")
	}
}

func TestAdverseFirstWhenStopAndTargetCollide(t *testing.T) {
	cfg := defaultConfig(0.01, 0.01, 0)
	long := Position{Direction: Long, Entry: 100, Contracts: 1, Leverage: 1, HoldCandles: 1}
	typ, price := decideExit(long, Candle{Low: 98, High: 102, Close: 100}, cfg)
	if typ != "hard-stop" {
		t.Fatalf("collision exit type = %q, want hard-stop", typ)
	}
	if price >= 99 { // stop is 99 and forced long exit must include adverse slippage
		t.Fatalf("long adverse exit = %.5f, want less than stop price", price)
	}

	short := Position{Direction: Short, Entry: 100, Contracts: 1, Leverage: 1, HoldCandles: 1}
	typ, price = decideExit(short, Candle{Low: 98, High: 102, Close: 100}, cfg)
	if typ != "hard-stop" {
		t.Fatalf("collision exit type = %q, want hard-stop", typ)
	}
	if price <= 101 { // stop is 101 and forced short exit must include adverse slippage
		t.Fatalf("short adverse exit = %.5f, want greater than stop price", price)
	}
}

func TestFeeAndSlippageMathForForcedExit(t *testing.T) {
	cfg := defaultConfig(0.003, 0.015, 0)
	position := Position{Direction: Long, Entry: 100, Contracts: 2, EntryFee: 2 * contractBTC * 100 * cfg.MakerFee}
	exit := adverseMarketExit(Long, 100, cfg.MarketSlippage)
	trade := closePosition(position, exit, "hard-stop", cfg)
	wantExit := 99.95
	if math.Abs(exit-wantExit) > 1e-9 {
		t.Fatalf("slipped exit = %.8f, want %.8f", exit, wantExit)
	}
	wantGross := 2 * contractBTC * (wantExit - 100)
	wantFees := 2*contractBTC*100*cfg.MakerFee + 2*contractBTC*wantExit*cfg.TakerFee
	if math.Abs(trade.GrossPnL-wantGross) > 1e-12 || math.Abs(trade.Fees-wantFees) > 1e-12 {
		t.Fatalf("trade=%+v, want gross %.12f fees %.12f", trade, wantGross, wantFees)
	}
}

func TestIntegerContractSizingRespectsRequestedNotionalAndRiskBudget(t *testing.T) {
	cfg := defaultConfig(0.003, 0.015, 0)
	cfg.RequestedNotional = 1_999
	cfg.PaperEquity = 1_000
	cfg.MaxMarginFraction = 0.20
	if got := contractCount(cfg, 1_000, 1); got != 0 {
		t.Fatalf("1x margin budget should skip one $1000 contract, got %d", got)
	}
	if got := contractCount(cfg, 1_000, 3); got != 0 { // 1 contract needs $333.33; budget is $200
		t.Fatalf("3x margin budget should still skip one $1000 contract, got %d", got)
	}
	cfg.PaperEquity = 2_000
	if got := contractCount(cfg, 1_000, 3); got != 1 {
		t.Fatalf("expected exactly one whole contract, got %d", got)
	}
	cfg.RequestedNotional = 999
	if got := contractCount(cfg, 1_000, 3); got != 0 {
		t.Fatalf("requested notional below one contract must not create a fractional contract, got %d", got)
	}
}

func TestFundingBookedOnlyAtCrossedEightHourBoundaries(t *testing.T) {
	start := time.Date(2025, 1, 1, 7, 59, 0, 0, time.UTC).UnixMilli()
	end := time.Date(2025, 1, 1, 8, 1, 0, 0, time.UTC).UnixMilli()
	boundaries := fundingBoundariesCrossed(start, end)
	if len(boundaries) != 1 || boundaries[0] != time.Date(2025, 1, 1, 8, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("crossed boundaries=%v, want exactly 08:00", boundaries)
	}
	p := Position{Direction: Long, Entry: 100, Contracts: 1, LastFundingAt: start}
	book := FundingBook{Rates: map[int64]float64{boundaries[0]: 0.01}}
	cash, _ := bookFunding(0, Result{}, &p, end, book)
	if math.Abs(cash+0.01) > 1e-12 { // one contract is 0.01 BTC * $100 = $1 notional; long pays 1%
		t.Fatalf("funding cashflow = %.12f, want -0.01", cash)
	}
	if p.Funding != cash {
		t.Fatalf("position funding=%f, cash=%f", p.Funding, cash)
	}
}

func TestValidateHardStopRequiresFiniteValueWithinOnePointFiveToThreePercent(t *testing.T) {
	tests := []struct {
		name  string
		stop  float64
		valid bool
	}{
		{name: "minimum 1.5 percent", stop: 0.015, valid: true},
		{name: "middle 2 percent", stop: 0.020, valid: true},
		{name: "maximum 3 percent", stop: 0.030, valid: true},
		{name: "below minimum", stop: 0.014999},
		{name: "above maximum", stop: 0.030001},
		{name: "negative", stop: -0.01},
		{name: "nan", stop: math.NaN()},
		{name: "positive infinity", stop: math.Inf(1)},
		{name: "negative infinity", stop: math.Inf(-1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := defaultConfig(0.0006, test.stop, 0.10)
			err := validateConfig(cfg)
			if (err == nil) != test.valid {
				t.Fatalf("validateConfig(stop=%v) error=%v, valid=%t", test.stop, err, test.valid)
			}
			result := runBacktest(nil, cfg, FundingBook{})
			if result.InvalidConfig == test.valid {
				t.Fatalf("runBacktest invalidConfig=%t for stop=%v, valid=%t", result.InvalidConfig, test.stop, test.valid)
			}
		})
	}
}

func TestValidateInitialCloseSpreadRequiresFiniteValueWithinPointZeroFiveToPointZeroEightPercent(t *testing.T) {
	tests := []struct {
		name   string
		spread float64
		valid  bool
	}{
		{name: "minimum 0.05 percent", spread: 0.0005, valid: true},
		{name: "middle 0.06 percent", spread: 0.0006, valid: true},
		{name: "maximum 0.08 percent", spread: 0.0008, valid: true},
		{name: "below minimum", spread: 0.000499},
		{name: "above maximum", spread: 0.000801},
		{name: "negative", spread: -0.01},
		{name: "nan", spread: math.NaN()},
		{name: "positive infinity", spread: math.Inf(1)},
		{name: "negative infinity", spread: math.Inf(-1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := defaultConfig(test.spread, 0.015, 0.10)
			err := validateConfig(cfg)
			if (err == nil) != test.valid {
				t.Fatalf("validateConfig(spread=%v) error=%v, valid=%t", test.spread, err, test.valid)
			}
			result := runBacktest(nil, cfg, FundingBook{})
			if result.InvalidConfig == test.valid {
				t.Fatalf("runBacktest invalidConfig=%t for spread=%v, valid=%t", result.InvalidConfig, test.spread, test.valid)
			}
		})
	}
}

func TestCloseMarginTimeDecayNeverFallsBelowPointZeroFivePercent(t *testing.T) {
	tests := []struct {
		name   string
		spread float64
		hold   int
		want   float64
	}{
		{name: "initial 0.05 percent", spread: 0.0005, hold: 0, want: 0.0005},
		{name: "floor after one hour", spread: 0.0006, hold: 61, want: 0.0005},
		{name: "0.08 percent after one hour", spread: 0.0008, hold: 61, want: 0.00064},
		{name: "0.08 percent after six hours", spread: 0.0008, hold: 361, want: 0.000536},
		{name: "floor after six hours", spread: 0.0006, hold: 361, want: 0.0005},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := closeMargin(test.spread, test.hold)
			if math.Abs(got-test.want) > 1e-12 {
				t.Fatalf("closeMargin(%.6f, %d)=%.12f, want %.12f", test.spread, test.hold, got, test.want)
			}
			if got < minimumCloseMargin {
				t.Fatalf("closeMargin(%.6f, %d)=%.12f fell below floor %.12f", test.spread, test.hold, got, minimumCloseMargin)
			}
		})
	}
}

func TestMakerTakeProfitNetMarginAtPointZeroFiveAndPointZeroEightPercent(t *testing.T) {
	for _, spread := range []float64{0.0005, 0.0008} {
		t.Run(fmt.Sprintf("spread %.02f percent", spread*100), func(t *testing.T) {
			_, longGross, longFees, longNet := modeledTakeProfitNetMargin(Long, spread, makerFeeRate)
			_, shortGross, shortFees, shortNet := modeledTakeProfitNetMargin(Short, spread, makerFeeRate)
			if math.Abs(longGross-spread) > 1e-12 || math.Abs(shortGross-spread) > 1e-12 {
				t.Fatalf("gross margins long=%.12f short=%.12f, both want %.12f", longGross, shortGross, spread)
			}
			wantLongFees := makerFeeRate + (1+spread)*makerFeeRate
			wantShortFees := makerFeeRate + (1-spread)*makerFeeRate
			if math.Abs(longFees-wantLongFees) > 1e-12 || math.Abs(shortFees-wantShortFees) > 1e-12 {
				t.Fatalf("fees long=%.12f short=%.12f, want %.12f/%.12f", longFees, shortFees, wantLongFees, wantShortFees)
			}
			if math.Abs(longNet-(spread-wantLongFees)) > 1e-12 || math.Abs(shortNet-(spread-wantShortFees)) > 1e-12 {
				t.Fatalf("net margins long=%.12f short=%.12f", longNet, shortNet)
			}
			if spread == 0.0005 && math.Abs((spread-2*makerFeeRate)-0.0001) > 1e-12 {
				t.Fatal("0.05% gross less nominal 0.04% maker fees must leave approximately 0.01% before other costs")
			}
		})
	}
}

func TestHardStopGapUsesObservedAdverseExtremePlusSlippage(t *testing.T) {
	cfg := defaultConfig(0.003, 0.0008, 0)
	long := Position{Direction: Long, Entry: 100, Contracts: 1, Leverage: 1, HoldCandles: 1}
	typ, exit := decideExit(long, Candle{Low: 99, High: 100, Close: 99.5}, cfg)
	wantLongExit := 99 * (1 - cfg.MarketSlippage)
	if typ != "hard-stop" || math.Abs(exit-wantLongExit) > 1e-12 {
		t.Fatalf("long gap exit=(%q, %.8f), want hard-stop at adverse low plus slippage %.8f", typ, exit, wantLongExit)
	}

	short := Position{Direction: Short, Entry: 100, Contracts: 1, Leverage: 1, HoldCandles: 1}
	typ, exit = decideExit(short, Candle{Low: 100, High: 101, Close: 100.5}, cfg)
	wantShortExit := 101 * (1 + cfg.MarketSlippage)
	if typ != "hard-stop" || math.Abs(exit-wantShortExit) > 1e-12 {
		t.Fatalf("short gap exit=(%q, %.8f), want hard-stop at adverse high plus slippage %.8f", typ, exit, wantShortExit)
	}
}

func TestModeledNoGapStopLossIncludesFeesAndSlippage(t *testing.T) {
	longExit, longLoss := modeledNoGapStopLoss(Long, 0.0008, makerFeeRate, takerFeeRate, marketExitSlippage)
	wantLongExit := (1 - 0.0008) * (1 - marketExitSlippage)
	wantLongLoss := -(wantLongExit - 1 - (makerFeeRate + wantLongExit*takerFeeRate))
	if math.Abs(longExit-wantLongExit) > 1e-12 || math.Abs(longLoss-wantLongLoss) > 1e-12 {
		t.Fatalf("long exit/loss=(%.12f, %.12f), want (%.12f, %.12f)", longExit, longLoss, wantLongExit, wantLongLoss)
	}

	shortExit, shortLoss := modeledNoGapStopLoss(Short, 0.0008, makerFeeRate, takerFeeRate, marketExitSlippage)
	wantShortExit := (1 + 0.0008) * (1 + marketExitSlippage)
	wantShortLoss := -(-(wantShortExit - 1) - (makerFeeRate + wantShortExit*takerFeeRate))
	if math.Abs(shortExit-wantShortExit) > 1e-12 || math.Abs(shortLoss-wantShortLoss) > 1e-12 {
		t.Fatalf("short exit/loss=(%.12f, %.12f), want (%.12f, %.12f)", shortExit, shortLoss, wantShortExit, wantShortLoss)
	}
}
