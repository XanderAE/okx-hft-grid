# Implementation Plan

- [x] 1. Write bug condition exploration test
  - **Property 1: Bug Condition** - Fee-Adjusted SELL Quantity & Zero SeqId Bypass
  - **CRITICAL**: This test MUST FAIL on unfixed code - failure confirms the bugs exist
  - **DO NOT attempt to fix the test or the code when it fails**
  - **NOTE**: This test encodes the expected behavior - it will validate the fix when it passes after implementation
  - **GOAL**: Surface counterexamples that demonstrate both bugs exist
  - **Bug 1 — SELL Quantity**: For BUY fill events in cash mode with feeRate > 0, assert counter SELL quantity equals `fillSz * (1 - feeRate)`. On unfixed code, the handler uses `cfg.OrderSize` instead, so this will fail.
  - **Scoped PBT Approach**: Generate random FillEvents with side="buy", tdMode="cash", feeRate in (0, 0.01], fillSz > 0. Assert counter-order quantity = fillSz * (1 - feeRate).
  - **Bug 2 — Tick Parser**: For tick sequences where seqId=0, assert all ticks (including second and subsequent) pass sequence validation. On unfixed code, the second tick fails with "sequence out of order: current=0, previous=0".
  - **Scoped PBT Approach**: Generate N consecutive ticks with seqId=0 and valid price data. Assert all N ticks pass validation.
  - isBugCondition_SellQuantity: `X.side = "buy" AND X.tdMode = "cash" AND X.feeRate > 0`
  - isBugCondition_TickParser: `X.seqId = 0`
  - Run test on UNFIXED code
  - **EXPECTED OUTCOME**: Test FAILS (this is correct - it proves the bugs exist)
  - Document counterexamples found: e.g., "SELL qty = cfg.OrderSize > fillSz*(1-fee)" and "second tick with seqId=0 rejected"
  - Mark task complete when test is written, run, and failure is documented
  - _Requirements: 1.1, 1.2, 1.4, 1.5_

- [x] 2. Write preservation property tests (BEFORE implementing fix)
  - **Property 2: Preservation** - Non-BUY Fills & Non-Zero SeqId Validation
  - **IMPORTANT**: Follow observation-first methodology
  - **Bug 1 Preservation**: For SELL fills, observe counter BUY order uses cfg.OrderSize. For BUY fills with feeRate=0, observe SELL quantity equals full fillSz.
  - Observe: OnFill(SELL fill) → counter BUY quantity = cfg.OrderSize on unfixed code
  - Observe: OnFill(BUY fill, feeRate=0) → counter SELL quantity = fillSz on unfixed code
  - Write property: for all fill events where side="sell" OR feeRate=0, counter-order quantity matches cfg.OrderSize (sells) or fillSz (buys with zero fee)
  - **Bug 2 Preservation**: For ticks with seqId > 0, observe monotonic validation still applies.
  - Observe: validate(seqId=5 then seqId=6) → accepted on unfixed code
  - Observe: validate(seqId=5 then seqId=4) → rejected on unfixed code
  - Write property: for all ticks with seqId > 0, accept iff strictly greater than previous; reject otherwise
  - Verify all preservation tests PASS on UNFIXED code
  - **EXPECTED OUTCOME**: Tests PASS (this confirms baseline behavior to preserve)
  - Mark task complete when tests are written, run, and passing on unfixed code
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 3.5_

- [x] 3. Fix for SELL quantity fee deduction and tick parser seqId bypass

  - [x] 3.1 Implement fee-adjusted SELL quantity in fill_handler.go
    - In `OnFill`, when `side == "buy"` in cash mode, calculate `orderSize = fillSz * (1 - feeRate)` instead of using `cfg.OrderSize`
    - Keep SELL-fill path unchanged: counter BUY still uses `cfg.OrderSize`
    - _Bug_Condition: isBugCondition_SellQuantity(X) where X.side="buy" AND X.tdMode="cash" AND X.feeRate > 0_
    - _Expected_Behavior: counterOrder.quantity = fillSz * (1 - feeRate)_
    - _Preservation: SELL fills use cfg.OrderSize; BUY fills with feeRate=0 use full fillSz_
    - _Requirements: 2.1, 2.2, 3.1, 3.2_

  - [x] 3.2 Implement seqId==0 bypass in parser.go validate()
    - Add guard `if tick.SequenceId == 0` before Rule 6 monotonic check to skip sequence validation
    - Do NOT store zero seqId into `p.lastSequenceIds` map (avoid polluting sequence tracking)
    - _Bug_Condition: isBugCondition_TickParser(X) where X.seqId = 0_
    - _Expected_Behavior: tick with seqId=0 passes sequence validation, proceeds to price/timestamp checks_
    - _Preservation: Non-zero seqId ticks still validated monotonically_
    - _Requirements: 2.4, 2.5, 3.3, 3.4, 3.5_

  - [x] 3.3 Verify bug condition exploration test now passes
    - **Property 1: Expected Behavior** - Fee-Adjusted SELL Quantity & Zero SeqId Bypass
    - **IMPORTANT**: Re-run the SAME test from task 1 - do NOT write a new test
    - The test from task 1 encodes the expected behavior for both bugs
    - When this test passes, it confirms: SELL qty = fillSz*(1-feeRate) AND all seqId=0 ticks accepted
    - Run bug condition exploration test from step 1
    - **EXPECTED OUTCOME**: Test PASSES (confirms bugs are fixed)
    - _Requirements: 2.1, 2.2, 2.4, 2.5_

  - [x] 3.4 Verify preservation tests still pass
    - **Property 2: Preservation** - Non-BUY Fills & Non-Zero SeqId Validation
    - **IMPORTANT**: Re-run the SAME tests from task 2 - do NOT write new tests
    - Run preservation property tests from step 2
    - **EXPECTED OUTCOME**: Tests PASS (confirms no regressions)
    - Confirm SELL fills still use cfg.OrderSize, BUY fills with feeRate=0 still use full fillSz
    - Confirm non-zero seqId ticks still validated monotonically

- [x] 4. Checkpoint - Ensure all tests pass
  - Run full test suite for `internal/execution` and `internal/marketdata` packages
  - Ensure all property-based tests pass (bug condition + preservation)
  - Ensure existing unit tests still pass (no regressions in other areas)
  - Ask the user if questions arise
