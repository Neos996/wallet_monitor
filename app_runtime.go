package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

func runScanner(ctx context.Context, app *App, interval time.Duration) error {
	if interval <= 0 {
		interval = 15 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	{
		_, _, err := app.runManagedScan(ctx, "startup")
		if err != nil {
			if errors.Is(err, errScanAlreadyRunning) {
				goto tickerLoop
			}
			return err
		}
	}

tickerLoop:
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			_, _, err := app.runManagedScan(ctx, "ticker")
			if err != nil {
				if errors.Is(err, errScanAlreadyRunning) {
					continue
				}
				continue
			}
		}
	}
}

func (app *App) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", app.handleHealthz)
	mux.HandleFunc("/readyz", app.handleReadyz)

	adminRoutes := map[string]http.HandlerFunc{
		"/addresses":                  app.handleAddresses,
		"/addresses/":                 app.handleAddressByID,
		"/scan/once":                  app.handleScanOnce,
		"/callback-tasks":             app.handleCallbackTasks,
		"/callback-tasks/":            app.handleCallbackTaskByID,
		"/callback-tasks/retry":       app.handleRetryCallbackTasks,
		"/callback-tasks/dead/export": app.handleExportDeadCallbackTasks,
		"/stats":                      app.handleStats,
		"/metrics":                    app.handleMetrics,
	}
	if app.enableDebugRoutes {
		adminRoutes["/mock/transactions"] = app.handleMockTransactions
		adminRoutes["/debug/callbacks"] = app.handleDebugCallbacks
	}

	for pattern, handler := range adminRoutes {
		mux.HandleFunc(pattern, app.requireAdmin(handler))
	}
}

func (app *App) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (app *App) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(app.adminToken) == "" {
			next(w, r)
			return
		}

		token := strings.TrimSpace(r.Header.Get("X-Admin-Token"))
		if token == "" {
			auth := strings.TrimSpace(r.Header.Get("Authorization"))
			if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
				token = strings.TrimSpace(auth[len("bearer "):])
			}
		}

		if token == "" || token != app.adminToken {
			loggerFromContext(r.Context()).Warn("admin request unauthorized", "path", r.URL.Path, "method", r.Method)
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		next(w, r)
	}
}
