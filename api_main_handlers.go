package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"gorm.io/gorm"
	evmclient "wallet_monitor/internal/evm"
	tronclient "wallet_monitor/internal/tron"
)

func (app *App) handleAddresses(w http.ResponseWriter, r *http.Request) {
	dispatchMethods(w, r, map[string]http.HandlerFunc{
		http.MethodGet:  app.listAddresses,
		http.MethodPost: app.createAddress,
	})
}

func (app *App) listAddresses(w http.ResponseWriter, r *http.Request) {
	baseQuery := app.db.WithContext(r.Context()).Model(&WatchedAddress{})
	if value := r.URL.Query().Get("chain"); value != "" {
		baseQuery = baseQuery.Where("chain = ?", strings.ToLower(strings.TrimSpace(value)))
	}
	if value := r.URL.Query().Get("network"); value != "" {
		baseQuery = baseQuery.Where("network = ?", strings.ToLower(strings.TrimSpace(value)))
	}
	if value := r.URL.Query().Get("asset_type"); value != "" {
		baseQuery = baseQuery.Where("asset_type = ?", strings.ToLower(strings.TrimSpace(value)))
	}
	if value := strings.TrimSpace(r.URL.Query().Get("address")); value != "" {
		baseQuery = baseQuery.Where("address = ?", value)
	}
	if value := strings.TrimSpace(r.URL.Query().Get("enabled")); value != "" {
		switch strings.ToLower(value) {
		case "true", "1":
			baseQuery = baseQuery.Where("enabled = ?", true)
		case "false", "0":
			baseQuery = baseQuery.Where("enabled = ?", false)
		}
	}
	respondWithPaginatedQuery[WatchedAddress](w, r, baseQuery, "id asc", 100, 1000, "addresses")
}

func (app *App) createAddress(w http.ResponseWriter, r *http.Request) {
	var req CreateAddressRequest
	if !decodeJSONBody(r.Context(), w, r, &req, "decode create address request failed") {
		return
	}
	if !requireNonEmptyField(w, req.Address, "address") {
		return
	}
	applyCreateAddressDefaults(&req)
	if err := validateAndNormalizeCreateAddressRequest(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateCallbackURL(req.CallbackURL, app.callbackURLPolicies); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	startHeight := uint64(0)
	if req.StartHeight != nil {
		startHeight = *req.StartHeight
	} else if req.Chain == "tron" {
		apiURL := resolveTronAPIURL(app.rpcURL, req.Network)
		client := tronclient.NewClient(apiURL).WithAPIKey(app.tronAPIKey)
		currentBlock, err := client.GetNowBlockNumber(r.Context())
		if err != nil {
			logAndRespondError(r.Context(), w, http.StatusBadGateway, "unable to resolve start_height from tron rpc (provide start_height explicitly or check rpc-url)", "resolve tron start height failed", err, "network", req.Network)
			return
		}
		startHeight = calculateConfirmedCutoff(currentBlock, req.MinConfirmations)
	} else if req.Chain == "evm" {
		if app.evmRPCURL == "" {
			http.Error(w, "evm rpc url not configured; provide -evm-rpc-url or start_height explicitly", http.StatusBadGateway)
			return
		}
		client := evmclient.NewClient(app.evmRPCURL)
		blockHex, err := client.GetBlockNumber(r.Context())
		if err != nil {
			logAndRespondError(r.Context(), w, http.StatusBadGateway, "unable to resolve start_height from evm rpc (provide start_height explicitly or check evm-rpc-url)", "resolve evm start height failed", err, "network", req.Network)
			return
		}
		currentBlock, err := parseHexUint64(blockHex)
		if err != nil {
			logAndRespondError(r.Context(), w, http.StatusBadGateway, "invalid evm block number from rpc", "parse evm start height failed", err, "value", blockHex)
			return
		}
		startHeight = calculateConfirmedCutoff(currentBlock, req.MinConfirmations)
	}

	addr := WatchedAddress{
		Chain:            req.Chain,
		Network:          req.Network,
		Address:          req.Address,
		AssetType:        req.AssetType,
		TokenContract:    req.TokenContract,
		CallbackURL:      req.CallbackURL,
		Enabled:          true,
		MinConfirmations: req.MinConfirmations,
		LastSeenHeight:   startHeight,
	}

	if err := app.db.WithContext(r.Context()).Create(&addr).Error; err != nil {
		logAndRespondError(r.Context(), w, http.StatusInternalServerError, "database error", "create address failed", err,
			"chain", addr.Chain,
			"network", addr.Network,
			"address", addr.Address,
			"asset_type", addr.AssetType,
			"token_contract", addr.TokenContract,
		)
		return
	}
	loggerFromContext(r.Context()).Info("address created",
		"address_id", addr.ID,
		"chain", addr.Chain,
		"network", addr.Network,
		"address", addr.Address,
		"asset_type", addr.AssetType,
		"token_contract", addr.TokenContract,
		"start_height", addr.LastSeenHeight,
		"min_confirmations", addr.MinConfirmations,
	)

	writeJSON(w, http.StatusCreated, addr)
}

func (app *App) handleScanOnce(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	result, scanID, err := app.runManagedScan(r.Context(), "manual")
	if err != nil {
		if errors.Is(err, errScanAlreadyRunning) {
			logAndRespondError(r.Context(), w, http.StatusConflict, "scan already running", "manual scan rejected: already running", err)
			return
		}
		logAndRespondError(r.Context(), w, http.StatusInternalServerError, "scan failed", "manual scan failed", err)
		return
	}
	if scanID != "" {
		w.Header().Set("X-Scan-ID", scanID)
	}

	writeJSON(w, http.StatusOK, result)
}

func (app *App) handleMockTransactions(w http.ResponseWriter, r *http.Request) {
	dispatchMethods(w, r, map[string]http.HandlerFunc{
		http.MethodGet:    app.listMockTransactions,
		http.MethodPost:   app.createMockTransaction,
		http.MethodDelete: app.clearMockTransactions,
	})
}

func (app *App) listMockTransactions(w http.ResponseWriter, r *http.Request) {
	baseQuery := app.db.WithContext(r.Context()).Model(&MockIncomingTx{})
	respondWithPaginatedQuery[MockIncomingTx](w, r, baseQuery, "block_height asc, id asc", 100, 1000, "mock transactions")
}

func (app *App) createMockTransaction(w http.ResponseWriter, r *http.Request) {
	var req CreateMockTxRequest
	if !decodeJSONBody(r.Context(), w, r, &req, "decode create mock transaction request failed") {
		return
	}
	if !requireNonEmptyField(w, req.Address, "address") {
		return
	}
	if !requireNonEmptyField(w, req.Amount, "amount") {
		return
	}
	if err := app.normalizeMockTxRequest(r.Context(), &req); err != nil {
		logAndRespondError(r.Context(), w, http.StatusInternalServerError, "database error", "normalize mock transaction request failed", err)
		return
	}

	row := MockIncomingTx{
		Chain:       req.Chain,
		Network:     req.Network,
		Address:     req.Address,
		TxHash:      req.TxHash,
		From:        req.From,
		To:          req.To,
		Amount:      req.Amount,
		BlockHeight: req.BlockHeight,
	}

	if err := app.db.WithContext(r.Context()).Create(&row).Error; err != nil {
		logAndRespondError(r.Context(), w, http.StatusInternalServerError, "database error", "create mock transaction failed", err, "address", row.Address, "tx_hash", row.TxHash)
		return
	}
	loggerFromContext(r.Context()).Info("mock transaction created", "tx_hash", row.TxHash, "address", row.Address, "block_height", row.BlockHeight)

	writeJSON(w, http.StatusCreated, row)
}

func (app *App) clearMockTransactions(w http.ResponseWriter, r *http.Request) {
	if err := app.db.WithContext(r.Context()).Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&MockIncomingTx{}).Error; err != nil {
		logAndRespondError(r.Context(), w, http.StatusInternalServerError, "database error", "clear mock transactions failed", err)
		return
	}
	loggerFromContext(r.Context()).Info("mock transactions cleared")

	w.WriteHeader(http.StatusNoContent)
}

func (app *App) handleDebugCallbacks(w http.ResponseWriter, r *http.Request) {
	dispatchMethods(w, r, map[string]http.HandlerFunc{
		http.MethodGet:    app.listReceivedCallbacks,
		http.MethodPost:   app.receiveDebugCallback,
		http.MethodDelete: app.clearReceivedCallbacks,
	})
}

func (app *App) listReceivedCallbacks(w http.ResponseWriter, r *http.Request) {
	baseQuery := app.db.WithContext(r.Context()).Model(&ReceivedCallback{})
	respondWithPaginatedQuery[ReceivedCallback](w, r, baseQuery, "id asc", 100, 1000, "received callbacks")
}

func (app *App) receiveDebugCallback(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		logAndRespondError(r.Context(), w, http.StatusBadRequest, "read body failed", "read debug callback body failed", err)
		return
	}

	var payload CallbackPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		logAndRespondError(r.Context(), w, http.StatusBadRequest, "invalid json", "decode debug callback failed", err)
		return
	}

	row := ReceivedCallback{
		Address:     payload.Address,
		TxHash:      payload.TxHash,
		BlockHeight: payload.BlockHeight,
		Payload:     string(body),
	}

	if err := app.db.WithContext(r.Context()).Create(&row).Error; err != nil {
		logAndRespondError(r.Context(), w, http.StatusInternalServerError, "database error", "store debug callback failed", err, "tx_hash", row.TxHash, "address", row.Address)
		return
	}
	loggerFromContext(r.Context()).Info("debug callback stored", "tx_hash", row.TxHash, "address", row.Address, "block_height", row.BlockHeight)

	w.WriteHeader(http.StatusNoContent)
}

func (app *App) clearReceivedCallbacks(w http.ResponseWriter, r *http.Request) {
	if err := app.db.WithContext(r.Context()).Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&ReceivedCallback{}).Error; err != nil {
		logAndRespondError(r.Context(), w, http.StatusInternalServerError, "database error", "clear debug callbacks failed", err)
		return
	}
	loggerFromContext(r.Context()).Info("debug callbacks cleared")

	w.WriteHeader(http.StatusNoContent)
}
