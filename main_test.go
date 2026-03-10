package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveTronAPIURL(t *testing.T) {
	if got := resolveTronAPIURL(" https://custom.example ", "mainnet"); got != "https://custom.example" {
		t.Fatalf("custom rpc url mismatch: got=%q", got)
	}
	if got := resolveTronAPIURL("", "shasta"); got != "https://api.shasta.trongrid.io" {
		t.Fatalf("shasta mismatch: got=%q", got)
	}
	if got := resolveTronAPIURL("", "nile"); got != "https://nile.trongrid.io" {
		t.Fatalf("nile mismatch: got=%q", got)
	}
	if got := resolveTronAPIURL("", "mainnet"); got != "https://api.trongrid.io" {
		t.Fatalf("default mismatch: got=%q", got)
	}
}

func TestRequireAdmin(t *testing.T) {
	app := &App{adminToken: "token123"}

	called := 0
	handler := app.requireAdmin(func(w http.ResponseWriter, _ *http.Request) {
		called++
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/addresses", nil)
	rr := httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized, got=%d", rr.Code)
	}
	if called != 0 {
		t.Fatalf("handler should not be called, called=%d", called)
	}

	req = httptest.NewRequest(http.MethodGet, "http://example.com/addresses", nil)
	req.Header.Set("Authorization", "Bearer token123")
	rr = httptest.NewRecorder()
	handler(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected success, got=%d", rr.Code)
	}
	if called != 1 {
		t.Fatalf("handler should be called once, called=%d", called)
	}
}
