package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBuildAddressUpdatesValidation(t *testing.T) {
	updates, err := buildAddressUpdates(UpdateAddressRequest{})
	if err == nil || err.Error() != "no updatable fields provided" {
		t.Fatalf("expected empty updates error, got updates=%v err=%v", updates, err)
	}

	zero := 0
	_, err = buildAddressUpdates(UpdateAddressRequest{MinConfirmations: &zero})
	if err == nil || err.Error() != "min_confirmations must be >= 1" {
		t.Fatalf("expected min confirmations error, got=%v", err)
	}

	callbackURL := " https://example.com/cb "
	enabled := true
	height := uint64(99)
	confirmations := 3
	updates, err = buildAddressUpdates(UpdateAddressRequest{
		CallbackURL:      &callbackURL,
		Enabled:          &enabled,
		MinConfirmations: &confirmations,
		LastSeenHeight:   &height,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updates["callback_url"] != "https://example.com/cb" {
		t.Fatalf("unexpected callback_url: %v", updates["callback_url"])
	}
	if updates["enabled"] != true || updates["min_confirmations"] != 3 || updates["last_seen_height"] != uint64(99) {
		t.Fatalf("unexpected updates: %v", updates)
	}
}

func TestApplyCreateAddressDefaultsAndValidationForMock(t *testing.T) {
	req := CreateAddressRequest{
		Chain:   "mock",
		Address: "mock_wallet_001",
	}

	applyCreateAddressDefaults(&req)
	if req.AssetType != "native" {
		t.Fatalf("unexpected asset type: %q", req.AssetType)
	}
	if req.Network != "local" {
		t.Fatalf("unexpected network: %q", req.Network)
	}
	if req.MinConfirmations != 1 {
		t.Fatalf("unexpected min confirmations: %d", req.MinConfirmations)
	}
	if err := validateAndNormalizeCreateAddressRequest(&req); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestNormalizeMockTxRequestDefaults(t *testing.T) {
	db := newTestDB(t)
	app := &App{db: db}

	req := CreateMockTxRequest{
		Address: "mock_wallet_001",
		Amount:  "12.5",
	}
	if err := app.normalizeMockTxRequest(context.Background(), &req); err != nil {
		t.Fatalf("normalizeMockTxRequest failed: %v", err)
	}
	if req.Chain != "mock" || req.Network != "local" {
		t.Fatalf("unexpected chain/network: %s/%s", req.Chain, req.Network)
	}
	if req.From != "mock_sender" || req.To != req.Address || req.TxHash == "" {
		t.Fatalf("unexpected defaults: %+v", req)
	}
	if req.BlockHeight != 1 {
		t.Fatalf("unexpected block height: %d", req.BlockHeight)
	}
}

func TestValidateCallbackURLAllowlist(t *testing.T) {
	policies, err := parseCallbackURLAllowlist("pay.example.com,*.internal.example")
	if err != nil {
		t.Fatalf("parseCallbackURLAllowlist failed: %v", err)
	}

	if err := validateCallbackURL("https://pay.example.com/callback", policies); err != nil {
		t.Fatalf("expected exact host allowed, got=%v", err)
	}
	if err := validateCallbackURL("https://api.internal.example/hook", policies); err != nil {
		t.Fatalf("expected wildcard host allowed, got=%v", err)
	}
	if err := validateCallbackURL("https://internal.example/hook", policies); err == nil {
		t.Fatalf("expected wildcard base domain to be rejected")
	}
	if err := validateCallbackURL("ftp://pay.example.com/callback", policies); err == nil {
		t.Fatalf("expected non-http scheme to be rejected")
	}
	if err := validateCallbackURL("https://evil.example.com/callback", policies); err == nil {
		t.Fatalf("expected non-allowlisted host to be rejected")
	}
}

func TestRegisterRoutesDebugDisabled(t *testing.T) {
	app := &App{enableDebugRoutes: false}
	mux := http.NewServeMux()
	app.registerRoutes(mux)

	for _, path := range []string{"/mock/transactions", "/debug/callbacks"} {
		req := httptest.NewRequest(http.MethodGet, "http://example.com"+path, nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("expected %s to be disabled with 404, got=%d", path, rr.Code)
		}
	}
}
