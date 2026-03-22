package main

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	scanDurationBuckets     = []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}
	callbackDurationBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}
)

type histogramMetric struct {
	mu      sync.Mutex
	buckets []float64
	counts  []uint64
	sum     float64
	count   uint64
}

type histogramSnapshot struct {
	Buckets []float64
	Counts  []uint64
	Sum     float64
	Count   uint64
}

func newHistogramMetric(buckets []float64) histogramMetric {
	return histogramMetric{
		buckets: append([]float64(nil), buckets...),
		counts:  make([]uint64, len(buckets)),
	}
}

func (h *histogramMetric) Observe(value float64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.sum += value
	h.count++
	for index, bucket := range h.buckets {
		if value <= bucket {
			h.counts[index]++
		}
	}
}

func (h *histogramMetric) Snapshot() histogramSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()

	snapshot := histogramSnapshot{
		Buckets: append([]float64(nil), h.buckets...),
		Counts:  append([]uint64(nil), h.counts...),
		Sum:     h.sum,
		Count:   h.count,
	}
	return snapshot
}

type labeledCounter struct {
	mu     sync.Mutex
	values map[string]uint64
}

func (c *labeledCounter) Add(label string, delta uint64) {
	if delta == 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.values == nil {
		c.values = map[string]uint64{}
	}
	c.values[label] += delta
}

func (c *labeledCounter) Snapshot() map[string]uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	result := make(map[string]uint64, len(c.values))
	for label, value := range c.values {
		result[label] = value
	}
	return result
}

type metricsStore struct {
	lastScanDurationSecondsBits atomic.Uint64
	lastScanTimestampSec        atomic.Int64

	scanDurationHistogram histogramMetric

	scanAddressesTotal        atomic.Uint64
	scanDetectedTxsTotal      atomic.Uint64
	scanQueuedCallbacksTotal  atomic.Uint64
	scanDuplicateTxsTotal     atomic.Uint64
	scanFailedCallbacksTotal  atomic.Uint64
	scanDeadCallbacksTotal    atomic.Uint64
	scanUpdatedAddressesTotal atomic.Uint64
	scanSkippedTotal          atomic.Uint64
	scanAddressFailuresTotal  atomic.Uint64

	callbackDurationHistogram    histogramMetric
	callbackAttemptsTotal        atomic.Uint64
	callbackSuccessTotal         atomic.Uint64
	callbackDeadTotal            atomic.Uint64
	callbackDispatchSkippedTotal atomic.Uint64
	callbackFailuresByKind       labeledCounter
	callbackHTTPStatusTotal      labeledCounter

	callbackPending  atomic.Uint64
	callbackRetrying atomic.Uint64
	callbackSuccess  atomic.Uint64
	callbackDead     atomic.Uint64

	watchedEnabled  atomic.Uint64
	watchedDisabled atomic.Uint64
}

var metrics = &metricsStore{
	scanDurationHistogram:     newHistogramMetric(scanDurationBuckets),
	callbackDurationHistogram: newHistogramMetric(callbackDurationBuckets),
}

func setFloatBits(target *atomic.Uint64, value float64) {
	target.Store(math.Float64bits(value))
}

func getFloatBits(target *atomic.Uint64) float64 {
	return math.Float64frombits(target.Load())
}

func recordScanMetrics(result ScanResult, startedAt time.Time) {
	duration := time.Since(startedAt).Seconds()
	setFloatBits(&metrics.lastScanDurationSecondsBits, duration)
	metrics.scanDurationHistogram.Observe(duration)
	metrics.lastScanTimestampSec.Store(result.ScannedAt.Unix())

	metrics.scanAddressesTotal.Add(uint64(result.AddressesScanned))
	metrics.scanDetectedTxsTotal.Add(uint64(result.DetectedTxs))
	metrics.scanQueuedCallbacksTotal.Add(uint64(result.QueuedCallbacks))
	metrics.scanDuplicateTxsTotal.Add(uint64(result.DuplicateTxs))
	metrics.scanFailedCallbacksTotal.Add(uint64(result.FailedCallbacks))
	metrics.scanDeadCallbacksTotal.Add(uint64(result.DeadCallbacks))
	metrics.scanUpdatedAddressesTotal.Add(uint64(result.UpdatedAddresses))
}

func recordCallbackAttempt(duration time.Duration, callbackErr *CallbackError, dead bool) {
	metrics.callbackAttemptsTotal.Add(1)
	metrics.callbackDurationHistogram.Observe(duration.Seconds())

	if callbackErr == nil {
		metrics.callbackSuccessTotal.Add(1)
		return
	}

	kind := strings.TrimSpace(callbackErr.Kind)
	if kind == "" {
		kind = "unknown"
	}
	metrics.callbackFailuresByKind.Add(kind, 1)
	if callbackErr.StatusCode > 0 {
		metrics.callbackHTTPStatusTotal.Add(strconv.Itoa(callbackErr.StatusCode), 1)
	}
	if dead {
		metrics.callbackDeadTotal.Add(1)
	}
}

func (app *App) updateMetrics(ctx context.Context) {
	stats, err := app.collectStats(ctx)
	if err != nil {
		loggerFromContext(ctx).Error("update metrics failed", "err", err)
		return
	}

	metrics.watchedEnabled.Store(uint64(stats.WatchedEnabled))
	metrics.watchedDisabled.Store(uint64(stats.WatchedDisabled))
	metrics.callbackPending.Store(uint64(stats.CallbackPending))
	metrics.callbackRetrying.Store(uint64(stats.CallbackRetrying))
	metrics.callbackSuccess.Store(uint64(stats.CallbackSuccess))
	metrics.callbackDead.Store(uint64(stats.CallbackDead))
}

func (app *App) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}

	var builder strings.Builder

	writeGauge := func(name, help string, value float64) {
		fmt.Fprintf(&builder, "# HELP %s %s\n", name, help)
		fmt.Fprintf(&builder, "# TYPE %s gauge\n", name)
		fmt.Fprintf(&builder, "%s %.6f\n", name, value)
	}
	writeCounter := func(name, help string, value uint64) {
		fmt.Fprintf(&builder, "# HELP %s %s\n", name, help)
		fmt.Fprintf(&builder, "# TYPE %s counter\n", name)
		fmt.Fprintf(&builder, "%s %d\n", name, value)
	}
	writeHistogram := func(name, help string, snapshot histogramSnapshot) {
		fmt.Fprintf(&builder, "# HELP %s %s\n", name, help)
		fmt.Fprintf(&builder, "# TYPE %s histogram\n", name)
		for index, bucket := range snapshot.Buckets {
			fmt.Fprintf(&builder, "%s_bucket{le=\"%.6f\"} %d\n", name, bucket, snapshot.Counts[index])
		}
		fmt.Fprintf(&builder, "%s_bucket{le=\"+Inf\"} %d\n", name, snapshot.Count)
		fmt.Fprintf(&builder, "%s_sum %.6f\n", name, snapshot.Sum)
		fmt.Fprintf(&builder, "%s_count %d\n", name, snapshot.Count)
	}
	writeLabeledGaugeHeader := func(name, help string) {
		fmt.Fprintf(&builder, "# HELP %s %s\n", name, help)
		fmt.Fprintf(&builder, "# TYPE %s gauge\n", name)
	}
	writeLabeledGaugeValue := func(name, labelKey, labelValue string, value uint64) {
		fmt.Fprintf(&builder, "%s{%s=\"%s\"} %d\n", name, labelKey, labelValue, value)
	}
	writeLabeledCounter := func(name, help, labelKey string, values map[string]uint64) {
		fmt.Fprintf(&builder, "# HELP %s %s\n", name, help)
		fmt.Fprintf(&builder, "# TYPE %s counter\n", name)
		labels := make([]string, 0, len(values))
		for label := range values {
			labels = append(labels, label)
		}
		sort.Strings(labels)
		for _, label := range labels {
			fmt.Fprintf(&builder, "%s{%s=\"%s\"} %d\n", name, labelKey, label, values[label])
		}
	}

	writeGauge("wallet_monitor_last_scan_duration_seconds", "Duration of the last successful scan cycle.", getFloatBits(&metrics.lastScanDurationSecondsBits))
	writeHistogram("wallet_monitor_scan_duration_seconds", "Duration of successful scan cycles.", metrics.scanDurationHistogram.Snapshot())
	writeCounter("wallet_monitor_scan_addresses_total", "Total scanned addresses.", metrics.scanAddressesTotal.Load())
	writeCounter("wallet_monitor_scan_detected_txs_total", "Total detected incoming transactions.", metrics.scanDetectedTxsTotal.Load())
	writeCounter("wallet_monitor_scan_queued_callbacks_total", "Total callback tasks queued from scans.", metrics.scanQueuedCallbacksTotal.Load())
	writeCounter("wallet_monitor_scan_duplicate_txs_total", "Total duplicate transactions skipped.", metrics.scanDuplicateTxsTotal.Load())
	writeCounter("wallet_monitor_scan_failed_callbacks_total", "Total failed callback deliveries observed by scans.", metrics.scanFailedCallbacksTotal.Load())
	writeCounter("wallet_monitor_scan_dead_callbacks_total", "Total callbacks moved to dead state during scans.", metrics.scanDeadCallbacksTotal.Load())
	writeCounter("wallet_monitor_scan_updated_addresses_total", "Total watched addresses updated with new heights.", metrics.scanUpdatedAddressesTotal.Load())
	writeCounter("wallet_monitor_scan_skipped_total", "Total scans skipped because another scan was already running.", metrics.scanSkippedTotal.Load())
	writeCounter("wallet_monitor_scan_address_failures_total", "Total per-address scan failures.", metrics.scanAddressFailuresTotal.Load())
	writeGauge("wallet_monitor_last_scan_timestamp", "Unix timestamp of the last completed successful scan.", float64(metrics.lastScanTimestampSec.Load()))

	writeHistogram("wallet_monitor_callback_delivery_duration_seconds", "Duration of callback delivery attempts.", metrics.callbackDurationHistogram.Snapshot())
	writeCounter("wallet_monitor_callback_attempts_total", "Total callback delivery attempts.", metrics.callbackAttemptsTotal.Load())
	writeCounter("wallet_monitor_callback_success_total", "Total successful callback deliveries.", metrics.callbackSuccessTotal.Load())
	writeCounter("wallet_monitor_callback_dead_total", "Total callbacks moved to dead state after retries.", metrics.callbackDeadTotal.Load())
	writeCounter("wallet_monitor_callback_dispatch_skipped_total", "Total callback dispatch cycles skipped because another dispatcher was already running.", metrics.callbackDispatchSkippedTotal.Load())
	writeLabeledCounter("wallet_monitor_callback_failures_total", "Total callback delivery failures by kind.", "kind", metrics.callbackFailuresByKind.Snapshot())
	writeLabeledCounter("wallet_monitor_callback_http_status_total", "Total non-2xx callback responses by status code.", "status", metrics.callbackHTTPStatusTotal.Snapshot())

	writeLabeledGaugeHeader("wallet_monitor_callback_tasks", "Callback task counts by status.")
	writeLabeledGaugeValue("wallet_monitor_callback_tasks", "status", "pending", metrics.callbackPending.Load())
	writeLabeledGaugeValue("wallet_monitor_callback_tasks", "status", "retrying", metrics.callbackRetrying.Load())
	writeLabeledGaugeValue("wallet_monitor_callback_tasks", "status", "success", metrics.callbackSuccess.Load())
	writeLabeledGaugeValue("wallet_monitor_callback_tasks", "status", "dead", metrics.callbackDead.Load())

	writeLabeledGaugeHeader("wallet_monitor_watched_addresses", "Watched address counts by enabled state.")
	writeLabeledGaugeValue("wallet_monitor_watched_addresses", "state", "enabled", metrics.watchedEnabled.Load())
	writeLabeledGaugeValue("wallet_monitor_watched_addresses", "state", "disabled", metrics.watchedDisabled.Load())

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(builder.String()))
}
