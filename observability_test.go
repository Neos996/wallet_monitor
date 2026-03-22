package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db failed: %v", err)
	}
	if err := db.AutoMigrate(&WatchedAddress{}, &TokenMetadata{}, &ProcessedTx{}, &MockIncomingTx{}, &ReceivedCallback{}, &CallbackTask{}); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}
	if err := migrateLegacyIndexes(db); err != nil {
		t.Fatalf("migrate legacy indexes failed: %v", err)
	}
	return db
}

func TestWithObservabilitySetsRequestID(t *testing.T) {
	app := &App{}
	handler := app.withObservability(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requestIDFromContext(r.Context()) == "" {
			t.Fatalf("expected request id in context")
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "http://example.com/healthz", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if got := rr.Header().Get("X-Request-ID"); got == "" {
		t.Fatalf("expected X-Request-ID header")
	}
	if rr.Code != http.StatusNoContent {
		t.Fatalf("unexpected status: got=%d", rr.Code)
	}
}

func TestRunManagedScanBusy(t *testing.T) {
	app := &App{}
	app.scanMu.Lock()
	defer app.scanMu.Unlock()

	_, _, err := app.runManagedScan(context.Background(), "manual")
	if !errors.Is(err, errScanAlreadyRunning) {
		t.Fatalf("expected errScanAlreadyRunning, got=%v", err)
	}
}

func TestProcessDueCallbackTasksBusy(t *testing.T) {
	app := &App{}
	app.callbackDispatchMu.Lock()
	defer app.callbackDispatchMu.Unlock()

	_, err := app.processDueCallbackTasks(context.Background(), 10)
	if !errors.Is(err, errCallbackDispatchBusy) {
		t.Fatalf("expected errCallbackDispatchBusy, got=%v", err)
	}
}

func TestListAddressesPaginationHeaders(t *testing.T) {
	db := newTestDB(t)
	app := &App{db: db}

	rows := []WatchedAddress{
		{Chain: "mock", Network: "local", Address: "addr_1", AssetType: "native", Enabled: true, MinConfirmations: 1},
		{Chain: "mock", Network: "local", Address: "addr_2", AssetType: "native", Enabled: true, MinConfirmations: 1},
	}
	for _, row := range rows {
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("create address failed: %v", err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/addresses?limit=1&offset=1", nil)
	rr := httptest.NewRecorder()
	app.listAddresses(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status: got=%d", rr.Code)
	}
	if got := rr.Header().Get("X-Total-Count"); got != "2" {
		t.Fatalf("unexpected X-Total-Count: got=%q", got)
	}
	body := rr.Body.String()
	if strings.Count(body, "\"id\"") != 1 {
		t.Fatalf("expected one row in body, got=%s", body)
	}
}

func TestSendCallbackBodyNon2xxIncludesBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}))
	defer server.Close()

	app := &App{httpClient: server.Client()}
	status, err := app.sendCallbackBody(context.Background(), server.URL, `{"ok":false}`, 42)
	if status != http.StatusBadGateway {
		t.Fatalf("unexpected status: got=%d", status)
	}
	var cbErr *CallbackError
	if !errors.As(err, &cbErr) {
		t.Fatalf("expected CallbackError, got=%T", err)
	}
	if cbErr.ResponseBody == "" || !strings.Contains(cbErr.ResponseBody, "upstream unavailable") {
		t.Fatalf("expected response body in error, got=%q", cbErr.ResponseBody)
	}
}

func TestEvaluateReadinessScanFreshness(t *testing.T) {
	db := newTestDB(t)
	app := &App{
		db:                db,
		readyMaxScanAge:   time.Minute,
		readyMaxDeadTasks: -1,
	}

	previous := metrics.lastScanTimestampSec.Load()
	defer metrics.lastScanTimestampSec.Store(previous)

	metrics.lastScanTimestampSec.Store(time.Now().Add(-2 * time.Minute).Unix())
	response, status := app.evaluateReadiness(context.Background())
	if status != http.StatusServiceUnavailable {
		t.Fatalf("expected readiness failure, got=%d", status)
	}
	if response.Status != "degraded" {
		t.Fatalf("unexpected readiness status: got=%q", response.Status)
	}
}

func TestRequireMethodSetsAllowHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "http://example.com/stats", nil)
	rr := httptest.NewRecorder()

	if requireMethod(rr, req, http.MethodGet) {
		t.Fatalf("expected requireMethod to reject request")
	}
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unexpected status: got=%d", rr.Code)
	}
	if got := rr.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("unexpected Allow header: got=%q", got)
	}
}

func TestDispatchMethodsSetsSortedAllowHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodPatch, "http://example.com/mock/transactions", nil)
	rr := httptest.NewRecorder()

	if dispatchMethods(rr, req, map[string]http.HandlerFunc{
		http.MethodDelete: func(http.ResponseWriter, *http.Request) {},
		http.MethodGet:    func(http.ResponseWriter, *http.Request) {},
		http.MethodPost:   func(http.ResponseWriter, *http.Request) {},
	}) {
		t.Fatalf("expected dispatchMethods to reject request")
	}
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("unexpected status: got=%d", rr.Code)
	}
	if got := rr.Header().Get("Allow"); got != "DELETE, GET, POST" {
		t.Fatalf("unexpected Allow header: got=%q", got)
	}
}
