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
// Metrics use bounded labels (symbol, scope, reason_code, outcome).
// Order/fill IDs are NEVER used as Prometheus labels.
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

	// Production grid stabilization metrics (Task 3.9)
	serviceUp             prometheus.Gauge
	healthState           *prometheus.GaugeVec
	stateDirectoryWritable prometheus.Gauge

	// Private_WS
	privateWSState          *prometheus.GaugeVec
	privateWSLivenessAge    prometheus.Gauge
	privateWSReconnectTotal prometheus.Counter
	privateWSSubscriptionReady prometheus.Gauge

	// Reconciliation
	reconciliationLastSuccess prometheus.Gauge
	reconciliationLag         prometheus.Gauge
	reconciliationDuration    prometheus.Histogram
	reconciliationOutcome     *prometheus.CounterVec
	reconciliationChecked     prometheus.Counter
	reconciliationDiffs       prometheus.Counter
	reconciliationCompensated prometheus.Counter

	// Fill/Outbox
	fillDuplicatesSuppressed prometheus.Counter
	fillGaps                 prometheus.Counter
	outboxBacklog            prometheus.Gauge
	outboxLeaseRecovery      prometheus.Counter

	// Counter_SELL
	counterSellInitiationLatency prometheus.Histogram
	counterSellOutcome           *prometheus.CounterVec

	// Rebalancer
	rebalancerLastRun    prometheus.Gauge
	rebalancerJitter     prometheus.Histogram
	rebalancerRefAge     prometheus.Gauge
	rebalancerStaleCount prometheus.Gauge
	rebalancerOutcome    *prometheus.CounterVec

	// Safe_Stop
	safeStopActive *prometheus.GaugeVec

	// Alerts
	alertAttempts       prometheus.Counter
	alertDeliveryOutcome *prometheus.CounterVec

	// Internal state for rate calculation
	orderCount   atomic.Int64
	lastSnap     int64
	lastSnapTime time.Time
	stopRateCalc chan struct{}
}

// NewMetricsServer creates a MetricsServer that will listen on the given port.
// All production grid metrics use bounded labels (symbol, scope, reason_code, outcome).
// Order/fill IDs are never used as label values.
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

	// Production grid stabilization metrics
	serviceUp := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "grid_service_up",
		Help: "Whether the service is expected and running (1=up, 0=down)",
	})

	healthState := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "grid_health_state",
		Help: "Current health state (1=active for that state)",
	}, []string{"state"})

	stateDirectoryWritable := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "grid_state_directory_writable",
		Help: "Whether the production state directory is writable (1=yes, 0=no)",
	})

	privateWSState := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "grid_private_ws_state",
		Help: "Private_WS connection state (1=active for that state)",
	}, []string{"state"})

	privateWSLivenessAge := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "grid_private_ws_liveness_age_seconds",
		Help: "Seconds since last verified Private_WS liveness",
	})

	privateWSReconnectTotal := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "grid_private_ws_reconnect_total",
		Help: "Total Private_WS reconnection attempts",
	})

	privateWSSubscriptionReady := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "grid_private_ws_subscription_ready",
		Help: "Whether all required subscriptions are confirmed (1=yes, 0=no)",
	})

	reconciliationLastSuccess := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "grid_reconciliation_last_success_timestamp",
		Help: "Unix timestamp of last successful reconciliation",
	})

	reconciliationLag := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "grid_reconciliation_lag_seconds",
		Help: "Seconds since last successful reconciliation relative to 30s schedule",
	})

	reconciliationDuration := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "grid_reconciliation_duration_seconds",
		Help:    "Duration of reconciliation cycles",
		Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 20, 30},
	})

	reconciliationOutcome := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "grid_reconciliation_outcome_total",
		Help: "Reconciliation cycle outcomes by symbol and result",
	}, []string{"symbol", "result"})

	reconciliationChecked := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "grid_reconciliation_checked_total",
		Help: "Total fills/orders checked during reconciliation",
	})

	reconciliationDiffs := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "grid_reconciliation_diffs_total",
		Help: "Total diffs found during reconciliation",
	})

	reconciliationCompensated := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "grid_reconciliation_compensated_total",
		Help: "Total fills compensated during reconciliation",
	})

	fillDuplicatesSuppressed := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "grid_fill_duplicates_suppressed_total",
		Help: "Total duplicate fills suppressed by idempotent processing",
	})

	fillGaps := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "grid_fill_gaps_total",
		Help: "Total fill gaps detected requiring reconciliation",
	})

	outboxBacklog := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "grid_outbox_backlog",
		Help: "Current number of pending outbox records",
	})

	outboxLeaseRecovery := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "grid_outbox_lease_recovery_total",
		Help: "Total outbox lease recovery events",
	})

	counterSellInitiationLatency := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "grid_counter_sell_initiation_latency_seconds",
		Help:    "Latency from fill observation to Counter_SELL initiation",
		Buckets: []float64{0.1, 0.5, 1, 2, 3, 4, 5, 7, 10, 15},
	})

	counterSellOutcome := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "grid_counter_sell_outcome_total",
		Help: "Counter_SELL terminal outcomes by symbol and result",
	}, []string{"symbol", "outcome"})

	rebalancerLastRun := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "grid_rebalancer_last_run_timestamp",
		Help: "Unix timestamp of last rebalancer run",
	})

	rebalancerJitter := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "grid_rebalancer_jitter_seconds",
		Help:    "Rebalancer schedule jitter",
		Buckets: []float64{0.1, 0.5, 1, 2, 3, 4, 5},
	})

	rebalancerRefAge := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "grid_rebalancer_reference_age_seconds",
		Help: "Age of the reference price used by rebalancer",
	})

	rebalancerStaleCount := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "grid_rebalancer_stale_orders",
		Help: "Number of stale orders found in last rebalancer cycle",
	})

	rebalancerOutcome := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "grid_rebalancer_outcome_total",
		Help: "Rebalancer terminal outcomes by symbol and result",
	}, []string{"symbol", "outcome"})

	safeStopActive := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "grid_safe_stop_active",
		Help: "Whether Safe_Stop is active for a given scope and reason",
	}, []string{"scope", "reason_code"})

	alertAttempts := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "grid_alert_attempts_total",
		Help: "Total alert delivery attempts",
	})

	alertDeliveryOutcome := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "grid_alert_delivery_outcome_total",
		Help: "Alert delivery outcomes by channel and result",
	}, []string{"channel", "result"})

	// Register all metrics
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
	reg.MustRegister(serviceUp)
	reg.MustRegister(healthState)
	reg.MustRegister(stateDirectoryWritable)
	reg.MustRegister(privateWSState)
	reg.MustRegister(privateWSLivenessAge)
	reg.MustRegister(privateWSReconnectTotal)
	reg.MustRegister(privateWSSubscriptionReady)
	reg.MustRegister(reconciliationLastSuccess)
	reg.MustRegister(reconciliationLag)
	reg.MustRegister(reconciliationDuration)
	reg.MustRegister(reconciliationOutcome)
	reg.MustRegister(reconciliationChecked)
	reg.MustRegister(reconciliationDiffs)
	reg.MustRegister(reconciliationCompensated)
	reg.MustRegister(fillDuplicatesSuppressed)
	reg.MustRegister(fillGaps)
	reg.MustRegister(outboxBacklog)
	reg.MustRegister(outboxLeaseRecovery)
	reg.MustRegister(counterSellInitiationLatency)
	reg.MustRegister(counterSellOutcome)
	reg.MustRegister(rebalancerLastRun)
	reg.MustRegister(rebalancerJitter)
	reg.MustRegister(rebalancerRefAge)
	reg.MustRegister(rebalancerStaleCount)
	reg.MustRegister(rebalancerOutcome)
	reg.MustRegister(safeStopActive)
	reg.MustRegister(alertAttempts)
	reg.MustRegister(alertDeliveryOutcome)

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
		serviceUp:                    serviceUp,
		healthState:                  healthState,
		stateDirectoryWritable:       stateDirectoryWritable,
		privateWSState:               privateWSState,
		privateWSLivenessAge:         privateWSLivenessAge,
		privateWSReconnectTotal:      privateWSReconnectTotal,
		privateWSSubscriptionReady:   privateWSSubscriptionReady,
		reconciliationLastSuccess:    reconciliationLastSuccess,
		reconciliationLag:            reconciliationLag,
		reconciliationDuration:       reconciliationDuration,
		reconciliationOutcome:        reconciliationOutcome,
		reconciliationChecked:        reconciliationChecked,
		reconciliationDiffs:          reconciliationDiffs,
		reconciliationCompensated:    reconciliationCompensated,
		fillDuplicatesSuppressed:     fillDuplicatesSuppressed,
		fillGaps:                     fillGaps,
		outboxBacklog:                outboxBacklog,
		outboxLeaseRecovery:          outboxLeaseRecovery,
		counterSellInitiationLatency: counterSellInitiationLatency,
		counterSellOutcome:           counterSellOutcome,
		rebalancerLastRun:            rebalancerLastRun,
		rebalancerJitter:             rebalancerJitter,
		rebalancerRefAge:             rebalancerRefAge,
		rebalancerStaleCount:         rebalancerStaleCount,
		rebalancerOutcome:            rebalancerOutcome,
		safeStopActive:               safeStopActive,
		alertAttempts:                alertAttempts,
		alertDeliveryOutcome:         alertDeliveryOutcome,
		lastSnapTime:                 time.Now(),
		stopRateCalc:                 make(chan struct{}),
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

// --- Production Grid Stabilization Metric Methods ---

// SetServiceUp records whether the service is expected and running.
func (m *MetricsServer) SetServiceUp(up bool) {
	if up {
		m.serviceUp.Set(1)
	} else {
		m.serviceUp.Set(0)
	}
}

// SetHealthState sets the current health state gauge (only one state is active=1 at a time).
func (m *MetricsServer) SetHealthState(state string) {
	for _, s := range []string{"healthy", "degraded/reconciling", "safe-stopped"} {
		if s == state {
			m.healthState.WithLabelValues(s).Set(1)
		} else {
			m.healthState.WithLabelValues(s).Set(0)
		}
	}
}

// SetStateDirectoryWritable records state directory health.
func (m *MetricsServer) SetStateDirectoryWritable(writable bool) {
	if writable {
		m.stateDirectoryWritable.Set(1)
	} else {
		m.stateDirectoryWritable.Set(0)
	}
}

// SetPrivateWSStateMetric sets the Private_WS state gauge.
func (m *MetricsServer) SetPrivateWSStateMetric(state string) {
	for _, s := range []string{"disconnected", "connecting", "authenticating", "subscribing", "reconciling", "ready", "unhealthy", "backoff", "safe-stopped"} {
		if s == state {
			m.privateWSState.WithLabelValues(s).Set(1)
		} else {
			m.privateWSState.WithLabelValues(s).Set(0)
		}
	}
}

// SetPrivateWSLivenessAge records seconds since last verified liveness.
func (m *MetricsServer) SetPrivateWSLivenessAge(seconds float64) {
	m.privateWSLivenessAge.Set(seconds)
}

// IncrementPrivateWSReconnect increments the reconnection counter.
func (m *MetricsServer) IncrementPrivateWSReconnect() {
	m.privateWSReconnectTotal.Inc()
}

// SetPrivateWSSubscriptionReady records subscription readiness.
func (m *MetricsServer) SetPrivateWSSubscriptionReady(ready bool) {
	if ready {
		m.privateWSSubscriptionReady.Set(1)
	} else {
		m.privateWSSubscriptionReady.Set(0)
	}
}

// SetReconciliationLastSuccess records the last success timestamp.
func (m *MetricsServer) SetReconciliationLastSuccess(unixSeconds float64) {
	m.reconciliationLastSuccess.Set(unixSeconds)
}

// SetReconciliationLag records lag from the 30-second schedule.
func (m *MetricsServer) SetReconciliationLag(seconds float64) {
	m.reconciliationLag.Set(seconds)
}

// RecordReconciliationDuration records a reconciliation cycle duration.
func (m *MetricsServer) RecordReconciliationDuration(seconds float64) {
	m.reconciliationDuration.Observe(seconds)
}

// RecordReconciliationOutcome records a cycle outcome by symbol and result.
func (m *MetricsServer) RecordReconciliationOutcome(symbol, result string) {
	m.reconciliationOutcome.WithLabelValues(symbol, result).Inc()
}

// IncrementReconciliationChecked increments the checked counter.
func (m *MetricsServer) IncrementReconciliationChecked(count int) {
	m.reconciliationChecked.Add(float64(count))
}

// IncrementReconciliationDiffs increments the diffs counter.
func (m *MetricsServer) IncrementReconciliationDiffs(count int) {
	m.reconciliationDiffs.Add(float64(count))
}

// IncrementReconciliationCompensated increments compensated fills counter.
func (m *MetricsServer) IncrementReconciliationCompensated(count int) {
	m.reconciliationCompensated.Add(float64(count))
}

// IncrementFillDuplicatesSuppressed increments the duplicate fill counter.
func (m *MetricsServer) IncrementFillDuplicatesSuppressed() {
	m.fillDuplicatesSuppressed.Inc()
}

// IncrementFillGaps increments the fill gap counter.
func (m *MetricsServer) IncrementFillGaps() {
	m.fillGaps.Inc()
}

// SetOutboxBacklog records the current outbox backlog count.
func (m *MetricsServer) SetOutboxBacklog(count float64) {
	m.outboxBacklog.Set(count)
}

// IncrementOutboxLeaseRecovery increments lease recovery events.
func (m *MetricsServer) IncrementOutboxLeaseRecovery() {
	m.outboxLeaseRecovery.Inc()
}

// RecordCounterSellInitiationLatency records time from fill to Counter_SELL initiation.
func (m *MetricsServer) RecordCounterSellInitiationLatency(seconds float64) {
	m.counterSellInitiationLatency.Observe(seconds)
}

// RecordCounterSellOutcome records a Counter_SELL terminal outcome.
// Outcome must be one of: confirmed, rejected, unconfirmed, safe_failed.
func (m *MetricsServer) RecordCounterSellOutcome(symbol, outcome string) {
	m.counterSellOutcome.WithLabelValues(symbol, outcome).Inc()
}

// SetRebalancerLastRun records the last run timestamp.
func (m *MetricsServer) SetRebalancerLastRun(unixSeconds float64) {
	m.rebalancerLastRun.Set(unixSeconds)
}

// RecordRebalancerJitter records schedule jitter.
func (m *MetricsServer) RecordRebalancerJitter(seconds float64) {
	m.rebalancerJitter.Observe(seconds)
}

// SetRebalancerRefAge records the reference price age.
func (m *MetricsServer) SetRebalancerRefAge(seconds float64) {
	m.rebalancerRefAge.Set(seconds)
}

// SetRebalancerStaleCount records stale order count.
func (m *MetricsServer) SetRebalancerStaleCount(count float64) {
	m.rebalancerStaleCount.Set(count)
}

// RecordRebalancerOutcome records a rebalancer terminal outcome.
// Outcome must be one of: replaced, filled, already-cancelled, kept-by-rule, failed-safe.
func (m *MetricsServer) RecordRebalancerOutcome(symbol, outcome string) {
	m.rebalancerOutcome.WithLabelValues(symbol, outcome).Inc()
}

// SetSafeStopActive records whether Safe_Stop is active for a scope and reason.
func (m *MetricsServer) SetSafeStopActive(scope, reasonCode string, active bool) {
	if active {
		m.safeStopActive.WithLabelValues(scope, reasonCode).Set(1)
	} else {
		m.safeStopActive.WithLabelValues(scope, reasonCode).Set(0)
	}
}

// IncrementAlertAttempts increments the alert attempt counter.
func (m *MetricsServer) IncrementAlertAttempts() {
	m.alertAttempts.Inc()
}

// RecordAlertDeliveryOutcome records an alert delivery outcome by channel and result.
func (m *MetricsServer) RecordAlertDeliveryOutcome(channel, result string) {
	m.alertDeliveryOutcome.WithLabelValues(channel, result).Inc()
}
