package monitor

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func getFreePort() int {
	// Use a high port unlikely to be in use for testing.
	return 19090 + int(time.Now().UnixNano()%1000)
}

func TestNewMetricsServer(t *testing.T) {
	port := getFreePort()
	ms := NewMetricsServer(port)
	if ms == nil {
		t.Fatal("NewMetricsServer returned nil")
	}
	if ms.port != port {
		t.Fatalf("expected port %d, got %d", port, ms.port)
	}
	if ms.registry == nil {
		t.Fatal("registry should not be nil")
	}
}

func TestMetricsServerStartStop(t *testing.T) {
	port := getFreePort()
	ms := NewMetricsServer(port)

	if err := ms.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Give server time to be ready.
	time.Sleep(100 * time.Millisecond)

	// Verify /metrics endpoint responds.
	resp, err := http.Get(fmt.Sprintf("http://localhost:%d/metrics", port))
	if err != nil {
		t.Fatalf("GET /metrics error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	if err := ms.Stop(); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}
}

func TestRecordOrderLatency(t *testing.T) {
	port := getFreePort()
	ms := NewMetricsServer(port)

	if err := ms.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer ms.Stop()

	time.Sleep(100 * time.Millisecond)

	// Record some latencies.
	ms.RecordOrderLatency(1.5)
	ms.RecordOrderLatency(10.0)
	ms.RecordOrderLatency(100.0)

	body := getMetricsBody(t, port)
	if !strings.Contains(body, "order_latency_ms") {
		t.Error("expected order_latency_ms metric in output")
	}
}

func TestIncrementOrderCount(t *testing.T) {
	port := getFreePort()
	ms := NewMetricsServer(port)

	if err := ms.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer ms.Stop()

	time.Sleep(100 * time.Millisecond)

	ms.IncrementOrderCount()
	ms.IncrementOrderCount()
	ms.IncrementOrderCount()

	body := getMetricsBody(t, port)
	if !strings.Contains(body, "orders_total 3") {
		t.Errorf("expected orders_total 3 in output, got:\n%s", body)
	}
}

func TestIncrementErrorCount(t *testing.T) {
	port := getFreePort()
	ms := NewMetricsServer(port)

	if err := ms.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer ms.Stop()

	time.Sleep(100 * time.Millisecond)

	ms.IncrementErrorCount()
	ms.IncrementErrorCount()

	body := getMetricsBody(t, port)
	if !strings.Contains(body, "error_total 2") {
		t.Errorf("expected error_total 2 in output, got:\n%s", body)
	}
}

func TestSetActiveStrategies(t *testing.T) {
	port := getFreePort()
	ms := NewMetricsServer(port)

	if err := ms.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer ms.Stop()

	time.Sleep(100 * time.Millisecond)

	ms.SetActiveStrategies(5)

	body := getMetricsBody(t, port)
	if !strings.Contains(body, "active_strategies 5") {
		t.Errorf("expected active_strategies 5 in output, got:\n%s", body)
	}
}

func TestSetPositionValue(t *testing.T) {
	port := getFreePort()
	ms := NewMetricsServer(port)

	if err := ms.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer ms.Stop()

	time.Sleep(100 * time.Millisecond)

	ms.SetPositionValue("BTC-USDT", 50000.0)

	body := getMetricsBody(t, port)
	if !strings.Contains(body, `position_value{symbol="BTC-USDT"} 50000`) {
		t.Errorf("expected position_value for BTC-USDT in output, got:\n%s", body)
	}
}

func TestSetDailyPnL(t *testing.T) {
	port := getFreePort()
	ms := NewMetricsServer(port)

	if err := ms.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer ms.Stop()

	time.Sleep(100 * time.Millisecond)

	ms.SetDailyPnL(-123.45)

	body := getMetricsBody(t, port)
	if !strings.Contains(body, "daily_pnl -123.45") {
		t.Errorf("expected daily_pnl -123.45 in output, got:\n%s", body)
	}
}

func TestMetricsEndpointContainsAllMetrics(t *testing.T) {
	port := getFreePort()
	ms := NewMetricsServer(port)

	if err := ms.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer ms.Stop()

	time.Sleep(100 * time.Millisecond)

	// Record some data to ensure metrics appear.
	ms.RecordOrderLatency(5.0)
	ms.IncrementOrderCount()
	ms.IncrementErrorCount()
	ms.SetActiveStrategies(2)
	ms.SetDailyPnL(100.0)

	body := getMetricsBody(t, port)

	expectedMetrics := []string{
		"order_latency_ms",
		"orders_total",
		"orders_per_second",
		"error_total",
		"active_strategies",
		"daily_pnl",
	}

	for _, metric := range expectedMetrics {
		if !strings.Contains(body, metric) {
			t.Errorf("expected metric %q in output", metric)
		}
	}
}

func getMetricsBody(t *testing.T, port int) string {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://localhost:%d/metrics", port))
	if err != nil {
		t.Fatalf("GET /metrics error: %v", err)
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading response body: %v", err)
	}
	return string(b)
}
