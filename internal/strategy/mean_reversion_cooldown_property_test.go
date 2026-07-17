package strategy

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

// **Validates: Requirement 5.9**
// Property 12: Signal Cooldown Enforcement
// "No signal change SHALL occur within cooldownMs milliseconds of the previous signal change."

// genSignalGeneratorConfig generates a random SignalGeneratorConfig with cooldownMs in [100, 5000].
func genSignalGeneratorConfig(t *rapid.T) SignalGeneratorConfig {
	cooldownMs := rapid.IntRange(100, 5000).Draw(t, "cooldownMs")
	// entryThreshold must be > exitThreshold, entry in [1.0, 5.0], exit in [0.1, 2.0]
	exitCents := rapid.IntRange(10, 150).Draw(t, "exitCents") // 0.10 to 1.50
	exitThreshold := decimal.NewFromInt(int64(exitCents)).Div(decimal.NewFromInt(100))
	// entry must be > exit, and in [1.0, 5.0]
	entryMin := exitCents + 50 // ensure entry > exit by at least 0.50
	if entryMin < 100 {
		entryMin = 100
	}
	entryCents := rapid.IntRange(entryMin, 500).Draw(t, "entryCents")
	entryThreshold := decimal.NewFromInt(int64(entryCents)).Div(decimal.NewFromInt(100))

	lookback := rapid.IntRange(3, 20).Draw(t, "lookbackPeriod")

	maTypeInt := rapid.IntRange(0, 2).Draw(t, "maType")
	maType := models.MAType(maTypeInt)

	return SignalGeneratorConfig{
		EntryThreshold: entryThreshold,
		ExitThreshold:  exitThreshold,
		CooldownMs:     cooldownMs,
		LookbackPeriod: lookback,
		MAType:         maType,
	}
}

// TestPropertySignalCooldownNoChangeWithinCooldown verifies that no signal change occurs
// within cooldownMs milliseconds of the previous signal change.
func TestPropertySignalCooldownNoChangeWithinCooldown(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		config := genSignalGeneratorConfig(t)
		sg := NewSignalGenerator(config)

		// Fill the lookback buffer with varied prices to produce non-zero stdDev.
		// Use prices centered around 100 with spread to ensure valid signals.
		basePrice := 100
		for i := 0; i < config.LookbackPeriod; i++ {
			// Create varied prices: 96, 98, 100, 102, 104, ... repeating
			offset := (i%5)*2 - 4 // -4, -2, 0, 2, 4
			price := decimal.NewFromInt(int64(basePrice + offset))
			sg.AddDataPoint(price, decimal.NewFromInt(1))
		}

		// Generate a first signal change (from NONE to BUY/SELL) using an extreme price
		startTime := time.Now()
		// Use a very low price to trigger BUY (Z < -entryThreshold)
		firstSignal := sg.GenerateSignal(decimal.NewFromInt(50), startTime)
		if firstSignal == models.SignalDirectionNone {
			// If we didn't get a signal change, the test setup couldn't produce one.
			// Skip this iteration - the property doesn't apply without a signal change.
			return
		}

		// Now attempt to change the signal within the cooldown period.
		// Generate random timestamps within the cooldown window.
		cooldownDuration := time.Duration(config.CooldownMs) * time.Millisecond
		numAttempts := rapid.IntRange(1, 10).Draw(t, "numAttempts")

		for i := 0; i < numAttempts; i++ {
			// Generate a time offset within [1ms, cooldownMs-1ms] of the start time
			offsetMs := rapid.IntRange(1, config.CooldownMs-1).Draw(t, "offsetMs")
			attemptTime := startTime.Add(time.Duration(offsetMs) * time.Millisecond)

			// Use opposite extreme price to try to force a different signal
			// If current signal is BUY, try to trigger SELL with very high price
			// If current signal is SELL, try to trigger BUY with very low price
			var triggerPrice decimal.Decimal
			if firstSignal == models.SignalDirectionBuy {
				triggerPrice = decimal.NewFromInt(200) // very high → should want SELL
			} else {
				triggerPrice = decimal.NewFromInt(50) // very low → should want BUY
			}

			result := sg.GenerateSignal(triggerPrice, attemptTime)

			// The signal must NOT have changed within the cooldown period
			if result != firstSignal {
				t.Fatalf("signal changed within cooldown period: from %v to %v at offset %dms (cooldown=%dms)",
					firstSignal, result, offsetMs, config.CooldownMs)
			}

			// Also verify the time is actually within cooldown
			elapsed := attemptTime.Sub(startTime)
			if elapsed >= cooldownDuration {
				t.Fatalf("test logic error: elapsed %v >= cooldown %v", elapsed, cooldownDuration)
			}
		}
	})
}

// TestPropertySignalCooldownChangesAfterCooldown verifies that a signal CAN change
// after cooldownMs milliseconds have elapsed since the previous signal change.
func TestPropertySignalCooldownChangesAfterCooldown(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		config := genSignalGeneratorConfig(t)
		sg := NewSignalGenerator(config)

		// Fill the lookback buffer with varied prices
		basePrice := 100
		for i := 0; i < config.LookbackPeriod; i++ {
			offset := (i%5)*2 - 4
			price := decimal.NewFromInt(int64(basePrice + offset))
			sg.AddDataPoint(price, decimal.NewFromInt(1))
		}

		// Generate a first signal change using extreme low price → BUY
		startTime := time.Now()
		firstSignal := sg.GenerateSignal(decimal.NewFromInt(50), startTime)
		if firstSignal == models.SignalDirectionNone {
			return // Can't test if no signal was generated
		}

		// Now attempt to change the signal AFTER the cooldown has elapsed.
		cooldownDuration := time.Duration(config.CooldownMs) * time.Millisecond
		// Add some extra time beyond cooldown: [1ms, 1000ms] past cooldown
		extraMs := rapid.IntRange(1, 1000).Draw(t, "extraMs")
		afterCooldownTime := startTime.Add(cooldownDuration + time.Duration(extraMs)*time.Millisecond)

		// Try to trigger the opposite signal
		var triggerPrice decimal.Decimal
		if firstSignal == models.SignalDirectionBuy {
			triggerPrice = decimal.NewFromInt(200) // very high → SELL
		} else {
			triggerPrice = decimal.NewFromInt(50) // very low → BUY
		}

		result := sg.GenerateSignal(triggerPrice, afterCooldownTime)

		// After cooldown, the signal SHOULD be allowed to change (not forced to stay the same)
		// The signal must be different from the first signal if the price is extreme enough
		if result == firstSignal {
			t.Fatalf("signal did not change after cooldown elapsed: still %v at %dms after change (cooldown=%dms)",
				firstSignal, config.CooldownMs+extraMs, config.CooldownMs)
		}
	})
}

// TestPropertySignalCooldownConsecutiveChangesRespectCooldown verifies that for any sequence
// of signal changes, consecutive changes are always separated by at least cooldownMs.
func TestPropertySignalCooldownConsecutiveChangesRespectCooldown(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		config := genSignalGeneratorConfig(t)
		sg := NewSignalGenerator(config)

		// Fill buffer with varied prices
		basePrice := 100
		for i := 0; i < config.LookbackPeriod; i++ {
			offset := (i%5)*2 - 4
			price := decimal.NewFromInt(int64(basePrice + offset))
			sg.AddDataPoint(price, decimal.NewFromInt(1))
		}

		// Track all signal changes with their timestamps
		type signalChange struct {
			signal models.SignalDirection
			time   time.Time
		}
		var changes []signalChange

		startTime := time.Now()
		prevSignal := models.SignalDirectionNone

		// Generate a series of signals with varying times and prices
		numSteps := rapid.IntRange(5, 30).Draw(t, "numSteps")
		currentTime := startTime

		for step := 0; step < numSteps; step++ {
			// Advance time by a random amount [1ms, 3*cooldownMs]
			advanceMs := rapid.IntRange(1, config.CooldownMs*3).Draw(t, "advanceMs")
			currentTime = currentTime.Add(time.Duration(advanceMs) * time.Millisecond)

			// Alternate between extreme prices to try to force signal changes
			var price decimal.Decimal
			if step%2 == 0 {
				price = decimal.NewFromInt(50) // extreme low → BUY
			} else {
				price = decimal.NewFromInt(200) // extreme high → SELL
			}

			signal := sg.GenerateSignal(price, currentTime)

			if signal != prevSignal {
				changes = append(changes, signalChange{signal: signal, time: currentTime})
				prevSignal = signal
			}
		}

		// Verify: all consecutive signal changes are separated by at least cooldownMs
		cooldownDuration := time.Duration(config.CooldownMs) * time.Millisecond
		for i := 1; i < len(changes); i++ {
			elapsed := changes[i].time.Sub(changes[i-1].time)
			if elapsed < cooldownDuration {
				t.Fatalf("consecutive signal changes too close: change[%d]=%v at %v → change[%d]=%v at %v, elapsed=%v, required=%v",
					i-1, changes[i-1].signal, changes[i-1].time,
					i, changes[i].signal, changes[i].time,
					elapsed, cooldownDuration)
			}
		}
	})
}
