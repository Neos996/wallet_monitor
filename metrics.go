package main

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

type metricsStore struct {
	scanDurationBits     atomic.Uint64
	lastScanTimestampSec atomic.Int64

	scanAddressesTotal        atomic.Uint64
	scanDetectedTxsTotal      atomic.Uint64
	scanQueuedCallbacksTotal  atomic.Uint64
	scanDuplicateTxsTotal     atomic.Uint64
	scanFailedCallbacksTotal  atomic.Uint64
	scanDeadCallbacksTotal    atomic.Uint64
	scanUpdatedAddressesTotal atomic.Uint64

	callbackPending  atomic.Uint64
	callbackRetrying atomic.Uint64
	callbackSuccess  atomic.Uint64
	callbackDead     atomic.Uint64

	watchedEnabled  atomic.Uint64
	watchedDisabled atomic.Uint64
}

var metrics = &metricsStore{}

func setFloat(a *atomic.Uint64, v float64) {
	a.Store(math.Float64bits(v))
}

func getFloat(a *atomic.Uint64) float64 {
	return math.Float64frombits(a.Load())
}

func recordScanMetrics(result ScanResult, startedAt time.Time) {
	duration := time.Since(startedAt).Seconds()
	setFloat(&metrics.scanDurationBits, duration)
	metrics.lastScanTimestampSec.Store(result.ScannedAt.Unix())

	metrics.scanAddressesTotal.Add(uint64(result.AddressesScanned))
	metrics.scanDetectedTxsTotal.Add(uint64(result.DetectedTxs))
	metrics.scanQueuedCallbacksTotal.Add(uint64(result.QueuedCallbacks))
	metrics.scanDuplicateTxsTotal.Add(uint64(result.DuplicateTxs))
	metrics.scanFailedCallbacksTotal.Add(uint64(result.FailedCallbacks))
	metrics.scanDeadCallbacksTotal.Add(uint64(result.DeadCallbacks))
	metrics.scanUpdatedAddressesTotal.Add(uint64(result.UpdatedAddresses))
}

func (app *App) updateMetrics(ctx context.Context) {
	stats, err := app.collectStats(ctx)
	if err != nil {
		slog.Error("update metrics failed", "err", err)
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
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var b strings.Builder

	writeGauge := func(name, help string, value float64) {
		fmt.Fprintf(&b, "# HELP %s %s\n", name, help)
		fmt.Fprintf(&b, "# TYPE %s gauge\n", name)
		fmt.Fprintf(&b, "%s %.6f\n", name, value)
	}
	writeCounter := func(name, help string, value uint64) {
		fmt.Fprintf(&b, "# HELP %s %s\n", name, help)
		fmt.Fprintf(&b, "# TYPE %s counter\n", name)
		fmt.Fprintf(&b, "%s %d\n", name, value)
	}
	writeLabeledGaugeHeader := func(name, help string) {
		fmt.Fprintf(&b, "# HELP %s %s\n", name, help)
		fmt.Fprintf(&b, "# TYPE %s gauge\n", name)
	}
	writeLabeledGaugeValue := func(name, labelKey, labelValue string, value uint64) {
		fmt.Fprintf(&b, "%s{%s=\"%s\"} %d\n", name, labelKey, labelValue, value)
	}

	writeGauge("wallet_monitor_scan_duration_seconds", "Duration of one scan cycle.", getFloat(&metrics.scanDurationBits))
	writeCounter("wallet_monitor_scan_addresses_total", "Total scanned addresses.", metrics.scanAddressesTotal.Load())
	writeCounter("wallet_monitor_scan_detected_txs_total", "Total detected incoming transactions.", metrics.scanDetectedTxsTotal.Load())
	writeCounter("wallet_monitor_scan_queued_callbacks_total", "Total callback tasks queued from scans.", metrics.scanQueuedCallbacksTotal.Load())
	writeCounter("wallet_monitor_scan_duplicate_txs_total", "Total duplicate transactions skipped.", metrics.scanDuplicateTxsTotal.Load())
	writeCounter("wallet_monitor_scan_failed_callbacks_total", "Total callback enqueue failures during scans.", metrics.scanFailedCallbacksTotal.Load())
	writeCounter("wallet_monitor_scan_dead_callbacks_total", "Total callbacks moved to dead state during scans.", metrics.scanDeadCallbacksTotal.Load())
	writeCounter("wallet_monitor_scan_updated_addresses_total", "Total watched addresses updated with new heights.", metrics.scanUpdatedAddressesTotal.Load())
	writeGauge("wallet_monitor_last_scan_timestamp", "Unix timestamp of the last completed scan.", float64(metrics.lastScanTimestampSec.Load()))

	writeLabeledGaugeHeader("wallet_monitor_callback_tasks", "Callback task counts by status.")
	writeLabeledGaugeValue("wallet_monitor_callback_tasks", "status", "pending", metrics.callbackPending.Load())
	writeLabeledGaugeValue("wallet_monitor_callback_tasks", "status", "retrying", metrics.callbackRetrying.Load())
	writeLabeledGaugeValue("wallet_monitor_callback_tasks", "status", "success", metrics.callbackSuccess.Load())
	writeLabeledGaugeValue("wallet_monitor_callback_tasks", "status", "dead", metrics.callbackDead.Load())

	writeLabeledGaugeHeader("wallet_monitor_watched_addresses", "Watched address counts by enabled state.")
	writeLabeledGaugeValue("wallet_monitor_watched_addresses", "state", "enabled", metrics.watchedEnabled.Load())
	writeLabeledGaugeValue("wallet_monitor_watched_addresses", "state", "disabled", metrics.watchedDisabled.Load())

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}
