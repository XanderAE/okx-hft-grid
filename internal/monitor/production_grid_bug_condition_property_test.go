package monitor

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

type explorationAlertChannel struct {
	fail bool
}

func (c *explorationAlertChannel) Send(Alert) error {
	if c.fail {
		return errors.New("simulated isolated delivery failure")
	}
	return nil
}

func (c *explorationAlertChannel) Name() string { return "isolated-fake" }

// **Validates: Requirements 1.17, 2.5, 2.17**
//
// EXP-13 forces both external-channel success and failure. Mandatory journal
// evidence must exist before delivery in either case; the fake never uses I/O.
func TestProperty1_BugCondition_EXP13_JournaldFirstAlert(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		level := []AlertLevel{WARNING, CRITICAL}[rapid.IntRange(0, 1).Draw(t, "alert_level_index")]
		outputs := make(map[string]string)
		for _, outcome := range []struct {
			name string
			fail bool
		}{
			{name: "success", fail: false},
			{name: "failure", fail: true},
		} {
			var journal bytes.Buffer
			alerter := NewAlerter([]AlertChannel{&explorationAlertChannel{fail: outcome.fail}}, NewStructuredLogger(&journal))
			alerter.retries = 0
			alerter.retryInterval = 0
			_ = alerter.SendAlert(Alert{
				Level: level, Message: "production-grid isolated test alert",
				Timestamp: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC),
				Extra:     map[string]string{"symbol": "WIF-USDT"},
			})
			outputs[outcome.name] = journal.String()
		}

		successEvidence := strings.Contains(outputs["success"], "alert_raised")
		failureEvidence := strings.Contains(outputs["failure"], "alert_raised")
		if !successEvidence || !failureEvidence {
			t.Fatalf("REPRODUCED EXP-13 seed=57357 fake_time=2025-01-02T03:04:05Z exchange_script={external_alert_success,external_alert_failure,retries:0} observed_state={success_journal_alert_raised:%t,failure_journal_alert_raised:%t,success_journal_bytes:%d,failure_journal_bytes:%d} minimal_counterexample={configured_channels:1,external_success:true,mandatory_journal_evidence:false} network_record={dns:0,dial:0,real_requests:0}",
				successEvidence, failureEvidence, len(outputs["success"]), len(outputs["failure"]))
		}
	})
}
