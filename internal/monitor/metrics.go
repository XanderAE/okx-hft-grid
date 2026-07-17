package monitor

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// MetricsServer exposes Prometheus metrics via an HTTP /metrics endpoint.
type MetricsServer struct {
	httpServer *http.Server
	port       int
	registry   *prometheus.Registry

	// Metrics
	orderLatency     prometheus.Histogram
	ordersTotal      prometheus.Counter
	ordersPerSecond  prometheus.Gauge
	errorTotal       prometheus.Counter
	activeStrategies prometheus.Gauge
	positionValue    *prometheus.GaugeVec
	dailyPnL         prometheus.Gauge

	// Grid drift metrics
	gridDriftTotal      prometheus.Counter
	gridDriftLatency    prometheus.Histogram
	gridDriftFailure    prometheus.Counter
	gridDriftSuppressed prometheus.Counter

	// Internal state for rate calculation
	orderCount   atomic.Int64
	lastSnap     int64
	lastSnapTime time.Time
	stopRateCalc chan struct{}
}

// NewMetricsServer creates a MetricsServer that will listen on the given port.
func NewMetricsServer(port int) *MetricsServer {
	reg := prometheus.NewRegistry()

	orderLatency := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "order_latency_ms",
		Help:    "Order placement latency in milliseconds",
		Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 50, 100, 500, 1000},
	})

	ordersTotal := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "orders_total",
		Help: "Total number of orders placed",
	})

	ordersPerSecond := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "orders_per_second",
		Help: "Calculated order rate (orders per second)",
	})

	errorTotal := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "error_total",
		Help: "Total number of errors",
	})

	activeStrategies := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "active_strategies",
		Help: "Number of active strategies",
	})

	positionValue := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "position_value",
		Help: "Position value by symbol",
	}, []string{"symbol"})

	dailyPnL := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "daily_pnl",
		Help: "Daily profit and loss",
	})

	gridDriftTotal := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "grid_drift_total",
		Help: "Total number of grid drift operations executed",
	})

	gridDriftLatency := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "grid_drift_latency_ms",
		Help:    "Grid drift operation latency in milliseconds",
		Buckets: []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2000},
	})

	gridDriftFailure := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "grid_drift_failure_total",
		Help: "Total number of grid drift operations that failed",
	})

	gridDriftSuppressed := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "grid_drift_suppressed_total",
		Help: "Total number of grid drift operations suppressed by cooldown",
	})

	reg.MustRegister(orderLatency)
	reg.MustRegister(ordersTotal)
	reg.MustRegister(ordersPerSecond)
	reg.MustRegister(errorTotal)
	reg.MustRegister(activeStrategies)
	reg.MustRegister(positionValue)
	reg.MustRegister(dailyPnL)
	reg.MustRegister(gridDriftTotal)
	reg.MustRegister(gridDriftLatency)
	reg.MustRegister(gridDriftFailure)
	reg.MustRegister(gridDriftSuppressed)

	return &MetricsServer{
		port:                port,
		registry:            reg,
		orderLatency:        orderLatency,
		ordersTotal:         ordersTotal,
		ordersPerSecond:     ordersPerSecond,
		errorTotal:          errorTotal,
		activeStrategies:    activeStrategies,
		positionValue:       positionValue,
		dailyPnL:            dailyPnL,
		gridDriftTotal:      gridDriftTotal,
		gridDriftLatency:    gridDriftLatency,
		gridDriftFailure:    gridDriftFailure,
		gridDriftSuppressed: gridDriftSuppressed,
		lastSnapTime:        time.Now(),
		stopRateCalc:        make(chan struct{}),
	}
}

// RecordOrderLatency records an order placement latency in milliseconds.
func (m *MetricsServer) RecordOrderLatency(durationMs float64) {
	m.orderLatency.Observe(durationMs)
}

// IncrementOrderCount increments the total orders counter.
func (m *MetricsServer) IncrementOrderCount() {
	m.ordersTotal.Inc()
	m.orderCount.Add(1)
}

// IncrementErrorCount increments the total error counter.
func (m *MetricsServer) IncrementErrorCount() {
	m.errorTotal.Inc()
}

// SetActiveStrategies sets the number of active strategies.
func (m *MetricsServer) SetActiveStrategies(count int) {
	m.activeStrategies.Set(float64(count))
}

// SetPositionValue sets the position value for a given symbol.
func (m *MetricsServer) SetPositionValue(symbol string, value float64) {
	m.positionValue.WithLabelValues(symbol).Set(value)
}

// SetDailyPnL sets the current daily PnL value.
func (m *MetricsServer) SetDailyPnL(value float64) {
	m.dailyPnL.Set(value)
}

// IncrementDriftCount increments the total grid drift operations counter.
func (m *MetricsServer) IncrementDriftCount() {
	m.gridDriftTotal.Inc()
}

// RecordDriftLatency records a grid drift operation latency in milliseconds.
func (m *MetricsServer) RecordDriftLatency(ms float64) {
	m.gridDriftLatency.Observe(ms)
}

// IncrementDriftFailure increments the grid drift failure counter.
func (m *MetricsServer) IncrementDriftFailure() {
	m.gridDriftFailure.Inc()
}

// IncrementDriftSuppressed increments the grid drift suppressed (by cooldown) counter.
func (m *MetricsServer) IncrementDriftSuppressed() {
	m.gridDriftSuppressed.Inc()
}

// Start starts the HTTP server serving /metrics and begins the rate calculator.
func (m *MetricsServer) Start() error {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{}))

	m.httpServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", m.port),
		Handler: mux,
	}

	// Start the orders-per-second rate calculation goroutine (updates every 5s).
	go m.runRateCalculator()

	errCh := make(chan error, 1)
	go func() {
		if err := m.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	// Give the server a brief moment to fail on binding issues.
	select {
	case err := <-errCh:
		return err
	case <-time.After(50 * time.Millisecond):
		return nil
	}
}

// Stop gracefully shuts down the HTTP server and stops the rate calculator.
func (m *MetricsServer) Stop() error {
	close(m.stopRateCalc)

	if m.httpServer == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return m.httpServer.Shutdown(ctx)
}

// runRateCalculator periodically computes orders_per_second from the counter delta.
func (m *MetricsServer) runRateCalculator() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopRateCalc:
			return
		case now := <-ticker.C:
			currentCount := m.orderCount.Load()
			elapsed := now.Sub(m.lastSnapTime).Seconds()
			if elapsed > 0 {
				rate := float64(currentCount-m.lastSnap) / elapsed
				m.ordersPerSecond.Set(rate)
			}
			m.lastSnap = currentCount
			m.lastSnapTime = now
		}
	}
}
