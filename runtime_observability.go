package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

var errScanAlreadyRunning = errors.New("scan already running")
var errCallbackDispatchBusy = errors.New("callback dispatch already running")

type contextKey string

const (
	loggerContextKey    contextKey = "logger"
	requestIDContextKey contextKey = "request_id"
	scanIDContextKey    contextKey = "scan_id"
)

type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
	bytes      int
}

func (w *loggingResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *loggingResponseWriter) Write(body []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(body)
	w.bytes += n
	return n, err
}

type pagination struct {
	Limit    int
	Offset   int
	Enabled  bool
	MaxLimit int
}

type readinessCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type readinessResponse struct {
	Status                    string           `json:"status"`
	RequestID                 string           `json:"request_id,omitempty"`
	LastSuccessfulScanUnix    int64            `json:"last_successful_scan_unix,omitempty"`
	LastSuccessfulScanAgeSecs float64          `json:"last_successful_scan_age_seconds,omitempty"`
	DeadCallbackTasks         int64            `json:"dead_callback_tasks"`
	Checks                    []readinessCheck `json:"checks"`
}

func loggerFromContext(ctx context.Context) *slog.Logger {
	if ctx == nil {
		return slog.Default()
	}
	if logger, ok := ctx.Value(loggerContextKey).(*slog.Logger); ok && logger != nil {
		return logger
	}
	return slog.Default()
}

func withLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerContextKey, logger)
}

func withRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDContextKey, requestID)
}

func requestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if value, ok := ctx.Value(requestIDContextKey).(string); ok {
		return value
	}
	return ""
}

func withScanID(ctx context.Context, scanID string) context.Context {
	return context.WithValue(ctx, scanIDContextKey, scanID)
}

func scanIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if value, ok := ctx.Value(scanIDContextKey).(string); ok {
		return value
	}
	return ""
}

func newTraceID(prefix string) string {
	var bytes [6]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		if prefix == "" {
			return hex.EncodeToString(bytes[:])
		}
		return prefix + "_" + hex.EncodeToString(bytes[:])
	}

	fallback := strconv.FormatInt(time.Now().UTC().UnixNano(), 36)
	if prefix == "" {
		return fallback
	}
	return prefix + "_" + fallback
}

func truncateText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

func parsePagination(r *http.Request, defaultLimit, maxLimit int) (pagination, error) {
	result := pagination{
		Limit:    defaultLimit,
		MaxLimit: maxLimit,
	}

	rawLimit := strings.TrimSpace(r.URL.Query().Get("limit"))
	rawOffset := strings.TrimSpace(r.URL.Query().Get("offset"))
	if rawLimit == "" && rawOffset == "" {
		return result, nil
	}

	result.Enabled = true
	if rawLimit != "" {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil || limit <= 0 {
			return result, fmt.Errorf("limit must be a positive integer")
		}
		result.Limit = limit
	}
	if rawOffset != "" {
		offset, err := strconv.Atoi(rawOffset)
		if err != nil || offset < 0 {
			return result, fmt.Errorf("offset must be a non-negative integer")
		}
		result.Offset = offset
	}
	if result.Limit > maxLimit {
		result.Limit = maxLimit
	}
	return result, nil
}

func applyPagination[T any](query *gorm.DB, page pagination, rows *[]T) error {
	if page.Enabled {
		query = query.Limit(page.Limit).Offset(page.Offset)
	}
	return query.Find(rows).Error
}

func writePaginationHeaders(w http.ResponseWriter, total int64, page pagination) {
	w.Header().Set("X-Total-Count", strconv.FormatInt(total, 10))
	if page.Enabled {
		w.Header().Set("X-Limit", strconv.Itoa(page.Limit))
		w.Header().Set("X-Offset", strconv.Itoa(page.Offset))
	}
}

func logAndRespondError(ctx context.Context, w http.ResponseWriter, status int, publicMessage, logMessage string, err error, attrs ...any) {
	logger := loggerFromContext(ctx)
	if err != nil {
		logAttrs := append(append([]any{}, attrs...), "err", err)
		switch {
		case status >= 500:
			logger.Error(logMessage, logAttrs...)
		case status >= 400:
			logger.Warn(logMessage, logAttrs...)
		default:
			logger.Info(logMessage, logAttrs...)
		}
	}
	http.Error(w, publicMessage, status)
}

func (app *App) withObservability(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if requestID == "" {
			requestID = newTraceID("req")
		}

		logger := slog.Default().With("request_id", requestID)
		ctx := withLogger(withRequestID(r.Context(), requestID), logger)
		r = r.WithContext(ctx)

		w.Header().Set("X-Request-ID", requestID)
		recorder := &loggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		startedAt := time.Now()

		next.ServeHTTP(recorder, r)

		duration := time.Since(startedAt)
		message := "http request completed"
		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.statusCode,
			"duration_ms", duration.Milliseconds(),
			"bytes", recorder.bytes,
			"remote_addr", r.RemoteAddr,
			"user_agent", r.UserAgent(),
		}

		switch {
		case recorder.statusCode >= 500:
			logger.Error(message, attrs...)
		case recorder.statusCode >= 400:
			logger.Warn(message, attrs...)
		default:
			logger.Info(message, attrs...)
		}
	})
}

func (app *App) runManagedScan(ctx context.Context, trigger string) (ScanResult, string, error) {
	if !app.scanMu.TryLock() {
		metrics.scanSkippedTotal.Add(1)
		loggerFromContext(ctx).Warn("scan skipped: already running", "trigger", trigger)
		return ScanResult{}, "", errScanAlreadyRunning
	}
	defer app.scanMu.Unlock()

	scanID := newTraceID("scan")
	logger := loggerFromContext(ctx).With("scan_id", scanID, "trigger", trigger)
	scanCtx := withLogger(withScanID(ctx, scanID), logger)
	startedAt := time.Now()

	logger.Info("scan started")
	result, err := app.scanOnce(scanCtx)
	if err != nil {
		logger.Error("scan failed", "duration_ms", time.Since(startedAt).Milliseconds(), "err", err)
		return result, scanID, err
	}

	logger.Info("scan completed",
		"duration_ms", time.Since(startedAt).Milliseconds(),
		"addresses", result.AddressesScanned,
		"txs", result.DetectedTxs,
		"queued", result.QueuedCallbacks,
		"callbacks", result.CallbacksSent,
		"duplicates", result.DuplicateTxs,
		"failed", result.FailedCallbacks,
		"dead", result.DeadCallbacks,
	)
	return result, scanID, nil
}

func (app *App) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}

	response, status := app.evaluateReadiness(r.Context())
	writeJSON(w, status, response)
}

func (app *App) evaluateReadiness(ctx context.Context) (readinessResponse, int) {
	now := time.Now().UTC()
	response := readinessResponse{
		Status:            "ok",
		RequestID:         requestIDFromContext(ctx),
		DeadCallbackTasks: 0,
		Checks:            make([]readinessCheck, 0, 3),
	}
	statusCode := http.StatusOK

	sqlDB, err := app.db.DB()
	if err != nil {
		response.Status = "degraded"
		statusCode = http.StatusServiceUnavailable
		response.Checks = append(response.Checks, readinessCheck{
			Name:   "database",
			Status: "failed",
			Detail: "resolve sql db handle failed",
		})
	} else if err := sqlDB.PingContext(ctx); err != nil {
		response.Status = "degraded"
		statusCode = http.StatusServiceUnavailable
		response.Checks = append(response.Checks, readinessCheck{
			Name:   "database",
			Status: "failed",
			Detail: truncateText(err.Error(), 160),
		})
	} else {
		response.Checks = append(response.Checks, readinessCheck{Name: "database", Status: "ok"})
	}

	lastScanUnix := metrics.lastScanTimestampSec.Load()
	response.LastSuccessfulScanUnix = lastScanUnix
	if lastScanUnix > 0 {
		ageSecs := now.Sub(time.Unix(lastScanUnix, 0).UTC()).Seconds()
		response.LastSuccessfulScanAgeSecs = ageSecs
	}

	switch {
	case app.readyMaxScanAge <= 0:
		response.Checks = append(response.Checks, readinessCheck{
			Name:   "scan_freshness",
			Status: "skipped",
			Detail: "disabled",
		})
	case lastScanUnix == 0:
		response.Status = "degraded"
		statusCode = http.StatusServiceUnavailable
		response.Checks = append(response.Checks, readinessCheck{
			Name:   "scan_freshness",
			Status: "failed",
			Detail: "no successful scan recorded yet",
		})
	default:
		age := now.Sub(time.Unix(lastScanUnix, 0).UTC())
		if age > app.readyMaxScanAge {
			response.Status = "degraded"
			statusCode = http.StatusServiceUnavailable
			response.Checks = append(response.Checks, readinessCheck{
				Name:   "scan_freshness",
				Status: "failed",
				Detail: fmt.Sprintf("last successful scan age %s exceeds %s", age.Round(time.Second), app.readyMaxScanAge),
			})
		} else {
			response.Checks = append(response.Checks, readinessCheck{
				Name:   "scan_freshness",
				Status: "ok",
				Detail: fmt.Sprintf("last successful scan age %s", age.Round(time.Second)),
			})
		}
	}

	var deadCount int64
	if err := app.db.WithContext(ctx).Model(&CallbackTask{}).Where("status = ?", "dead").Count(&deadCount).Error; err != nil {
		response.Status = "degraded"
		statusCode = http.StatusServiceUnavailable
		response.Checks = append(response.Checks, readinessCheck{
			Name:   "dead_callbacks",
			Status: "failed",
			Detail: "count dead callbacks failed",
		})
	} else {
		response.DeadCallbackTasks = deadCount
		switch {
		case app.readyMaxDeadTasks < 0:
			response.Checks = append(response.Checks, readinessCheck{
				Name:   "dead_callbacks",
				Status: "skipped",
				Detail: fmt.Sprintf("disabled (current=%d)", deadCount),
			})
		case deadCount > int64(app.readyMaxDeadTasks):
			response.Status = "degraded"
			statusCode = http.StatusServiceUnavailable
			response.Checks = append(response.Checks, readinessCheck{
				Name:   "dead_callbacks",
				Status: "failed",
				Detail: fmt.Sprintf("dead callbacks=%d exceeds threshold=%d", deadCount, app.readyMaxDeadTasks),
			})
		default:
			response.Checks = append(response.Checks, readinessCheck{
				Name:   "dead_callbacks",
				Status: "ok",
				Detail: fmt.Sprintf("dead callbacks=%d", deadCount),
			})
		}
	}

	return response, statusCode
}

func marshalJSON(value any) string {
	body, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(body)
}

func applySQLiteSettings(db *gorm.DB, journalMode string, busyTimeout time.Duration, maxOpenConns int) error {
	mode := strings.ToUpper(strings.TrimSpace(journalMode))
	switch mode {
	case "", "DELETE", "TRUNCATE", "PERSIST", "MEMORY", "WAL", "OFF":
	default:
		return fmt.Errorf("unsupported sqlite journal mode %q", journalMode)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	if maxOpenConns > 0 {
		sqlDB.SetMaxOpenConns(maxOpenConns)
		sqlDB.SetMaxIdleConns(maxOpenConns)
	}

	if mode != "" {
		if err := db.Exec("PRAGMA journal_mode = " + mode).Error; err != nil {
			return err
		}
	}
	if busyTimeout > 0 {
		timeoutMillis := busyTimeout.Milliseconds()
		if timeoutMillis <= 0 {
			timeoutMillis = 1
		}
		if err := db.Exec(fmt.Sprintf("PRAGMA busy_timeout = %d", timeoutMillis)).Error; err != nil {
			return err
		}
	}
	if mode == "WAL" {
		if err := db.Exec("PRAGMA synchronous = NORMAL").Error; err != nil {
			return err
		}
	}
	return nil
}
