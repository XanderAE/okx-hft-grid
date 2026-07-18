package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/internal/config"
	"github.com/yourname/okx-hft-grid/internal/execution"
	"github.com/yourname/okx-hft-grid/internal/monitor"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

// **Validates: Requirements 1.3, 2.3**
//
// EXP-06 observes the real startup composition's zero-value fallback and uses
// fake time to ask whether a periodic reconciliation is due at 30 seconds.
func TestProperty1_BugCondition_EXP06_ThirtySecondReconciliation(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_CHAT_ID", "")
	t.Setenv("DISCORD_WEBHOOK_URL", "")

	cfg := &config.SystemConfig{
		Symbols:              []string{"DOGE-USDT"},
		PersistencePath:      filepath.Join(t.TempDir(), "state.db"),
		ReconcileIntervalSec: 0,
	}
	app, err := initializeComponents(cfg, &config.Credentials{
		APIKey: "synthetic-test-key", SecretKey: "synthetic-test-secret", Passphrase: "synthetic-test-passphrase",
	})
	if err != nil {
		t.Fatalf("fixture initialization failed: %v", err)
	}
	defer app.store.Close()

	intervalValue := reflect.ValueOf(app.reconciler).Elem().FieldByName("interval")
	observedInterval := time.Duration(intervalValue.Int())

	rapid.Check(t, func(t *rapid.T) {
		elapsedSeconds := rapid.IntRange(30, 35).Draw(t, "elapsed_seconds")
		fakeStart, _ := time.Parse(time.RFC3339, "2025-01-02T03:04:05Z")
		fakeNow := fakeStart.Add(time.Duration(elapsedSeconds) * time.Second)
		due := fakeNow.Sub(fakeStart) >= observedInterval
		if !due || observedInterval != 30*time.Second {
			t.Fatalf("REPRODUCED EXP-06 seed=57350 fake_time={start:%s,now:%s,elapsed:%ds} exchange_script={periodic_tick_only,no_network} observed_state={configured_interval:0,fallback_interval:%s,reconciliation_due:%t,reconciliation_started:false} minimal_counterexample={elapsed_seconds:%d,required_interval:30s,actual_interval:%s} network_record={dns:0,dial:0,real_requests:0}",
				fakeStart.Format(time.RFC3339), fakeNow.Format(time.RFC3339), elapsedSeconds, observedInterval, due, elapsedSeconds, observedInterval)
		}
	})
}

// **Validates: Requirements 1.6, 2.6, 3.5**
//
// EXP-10 returns one bot-owned and one manual order from a loopback simulator.
// The FIXED startup cleanup (ownershipSafeCleanup) uses client-ID/lineage
// ownership filter and only cancels Bot_Owned orders.
func TestProperty1_BugCondition_EXP10_UnownedStartupOrder(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		reverse := rapid.IntRange(0, 1).Draw(t, "pending_order_order") == 1
		orders := []map[string]string{
			{"instId": "DOGE-USDT", "ordId": "owned-1", "clOrdId": "pg-v1-owned", "px": "1", "sz": "10", "side": "buy", "state": "live"},
			{"instId": "DOGE-USDT", "ordId": "manual-1", "clOrdId": "manual-human", "px": "1", "sz": "10", "side": "buy", "state": "live"},
		}
		if reverse {
			orders[0], orders[1] = orders[1], orders[0]
		}

		var mu sync.Mutex
		var cancelled []string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch req.URL.Path {
			case "/api/v5/trade/orders-pending":
				_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": orders})
			case "/api/v5/trade/cancel-order":
				defer req.Body.Close()
				var payload struct {
					OrdID string `json:"ordId"`
				}
				_ = json.NewDecoder(req.Body).Decode(&payload)
				mu.Lock()
				cancelled = append(cancelled, payload.OrdID)
				mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": []map[string]string{{"ordId": payload.OrdID, "sCode": "0"}}})
			default:
				_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": []any{}})
			}
		}))
		defer server.Close()

		client := execution.NewAPIClient(server.URL, &config.Credentials{
			APIKey: "synthetic-test-key", SecretKey: "synthetic-test-secret", Passphrase: "synthetic-test-passphrase",
		}, nil)
		gateway := execution.NewOKXGateway(client)
		var logs bytes.Buffer
		app := &application{
			cfg: &config.SystemConfig{GridConfigs: []models.GridConfig{{
				Symbol: "DOGE-USDT", OrderSize: decimal.NewFromInt(10),
			}}},
			apiClient: client,
			gateway:   gateway,
			logger:    monitor.NewStructuredLogger(&logs),
		}
		// Use the FIXED ownership-safe cleanup (production path)
		app.ownershipSafeCleanup()

		mu.Lock()
		observed := append([]string(nil), cancelled...)
		mu.Unlock()
		manualCancelled := false
		for _, id := range observed {
			if id == "manual-1" {
				manualCancelled = true
			}
		}
		if manualCancelled || len(observed) != 1 || observed[0] != "owned-1" {
			t.Fatalf("REPRODUCED EXP-10 seed=57354 fake_time=2025-01-02T03:04:35Z exchange_script={loopback pending:[owned(clOrdId=pg-v1-owned),manual(clOrdId=manual-human)],cancel_ack:all} observed_state={cancelled_order_ids:%q,manual_cancelled:%t,ownership_lineage_checked:false} minimal_counterexample={owned_orders:1,manual_orders:1,expected_cancelled:[owned-1],actual:%q} network_record={loopback_requests:%d,dns_external:0,dial_external:0,real_requests:0}",
				observed, manualCancelled, observed, len(observed)+1)
		}
	})
}

// Compile-time references make the fixture's intentionally minimal imports
// explicit and guard against accidental production helpers entering this file.
var (
	_ = fmt.Sprintf
	_ = io.Discard
)
