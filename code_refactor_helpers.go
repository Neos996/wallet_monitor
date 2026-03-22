package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

type detectedTxQueueOutcome int

const (
	detectedTxQueued detectedTxQueueOutcome = iota
	detectedTxDuplicate
	detectedTxMissingCallback
)

func normalizeWorkerCount(requested, total int) int {
	if total <= 0 {
		return 1
	}
	if requested <= 0 {
		requested = 1
	}
	if requested > total {
		requested = total
	}
	if requested <= 0 {
		return 1
	}
	return requested
}

func (result *addressResult) merge(other addressResult) {
	result.addressesScanned += other.addressesScanned
	result.detectedTxs += other.detectedTxs
	result.queuedCallbacks += other.queuedCallbacks
	result.duplicateTxs += other.duplicateTxs
	result.failedCallbacks += other.failedCallbacks
	result.updatedAddresses += other.updatedAddresses
}

func (result *addressResult) applyQueueOutcome(outcome detectedTxQueueOutcome) {
	switch outcome {
	case detectedTxQueued:
		result.queuedCallbacks++
	case detectedTxDuplicate:
		result.duplicateTxs++
	case detectedTxMissingCallback:
		result.failedCallbacks++
	}
}

func (result *ScanResult) mergeAddressResult(other addressResult) {
	result.AddressesScanned += other.addressesScanned
	result.DetectedTxs += other.detectedTxs
	result.QueuedCallbacks += other.queuedCallbacks
	result.DuplicateTxs += other.duplicateTxs
	result.FailedCallbacks += other.failedCallbacks
	result.UpdatedAddresses += other.updatedAddresses
}

func (result *ScanResult) mergeCallbackTaskResult(other CallbackTaskRunResult) {
	result.CallbacksSent += other.CallbacksSent
	result.FailedCallbacks += other.FailedCallbacks
	result.DeadCallbacks += other.DeadCallbacks
}

func (app *App) resolveCallbackURL(addr WatchedAddress) string {
	if addr.CallbackURL != "" {
		return addr.CallbackURL
	}
	return app.defaultCallbackURL
}

func (app *App) enqueueDetectedTx(ctx context.Context, watched WatchedAddress, tx Tx) (detectedTxQueueOutcome, error) {
	logger := loggerFromContext(ctx)

	processed, err := app.isProcessed(ctx, watched, tx.Hash, tx.LogIndex)
	if err != nil {
		logger.Error("check processed tx failed",
			"tx_hash", tx.Hash,
			"log_index", tx.LogIndex,
			"address", watched.Address,
			"err", err,
		)
		return detectedTxDuplicate, err
	}
	if processed {
		return detectedTxDuplicate, nil
	}

	callbackURL := app.resolveCallbackURL(watched)
	if callbackURL == "" {
		logger.Warn("skip tx: no callback URL configured",
			"tx_hash", tx.Hash,
			"log_index", tx.LogIndex,
			"address", watched.Address,
		)
		return detectedTxMissingCallback, nil
	}

	created, err := app.enqueueCallbackTask(ctx, watched, callbackURL, tx)
	if err != nil {
		logger.Error("enqueue callback tx failed",
			"tx_hash", tx.Hash,
			"log_index", tx.LogIndex,
			"address", watched.Address,
			"err", err,
		)
		return detectedTxDuplicate, err
	}
	if created {
		return detectedTxQueued, nil
	}

	return detectedTxDuplicate, nil
}

func (app *App) retryCallbackTasks(ctx context.Context, query *gorm.DB, limit int) (CallbackTaskRunResult, error) {
	now := time.Now().UTC()
	if err := query.Updates(map[string]any{"status": "retrying", "next_retry_at": now}).Error; err != nil {
		return CallbackTaskRunResult{}, err
	}
	return app.processDueCallbackTasks(ctx, limit)
}

func respondWithPaginatedQuery[T any](
	w http.ResponseWriter,
	r *http.Request,
	baseQuery *gorm.DB,
	order string,
	defaultLimit int,
	maxLimit int,
	resource string,
) bool {
	page, err := parsePagination(r, defaultLimit, maxLimit)
	if err != nil {
		logAndRespondError(
			r.Context(),
			w,
			http.StatusBadRequest,
			err.Error(),
			fmt.Sprintf("parse %s pagination failed", resource),
			err,
		)
		return false
	}

	countQuery := baseQuery.Session(&gorm.Session{})
	var total int64
	if err := countQuery.Count(&total).Error; err != nil {
		logAndRespondError(
			r.Context(),
			w,
			http.StatusInternalServerError,
			"database error",
			fmt.Sprintf("count %s failed", resource),
			err,
		)
		return false
	}

	listQuery := baseQuery.Session(&gorm.Session{})
	if order != "" {
		listQuery = listQuery.Order(order)
	}

	var rows []T
	if err := applyPagination(listQuery, page, &rows); err != nil {
		logAndRespondError(
			r.Context(),
			w,
			http.StatusInternalServerError,
			"database error",
			fmt.Sprintf("list %s failed", resource),
			err,
		)
		return false
	}

	writePaginationHeaders(w, total, page)
	loggerFromContext(r.Context()).Info(
		"resource listed",
		"resource", resource,
		"total", total,
		"returned", len(rows),
		"paginated", page.Enabled,
		"limit", page.Limit,
		"offset", page.Offset,
	)
	writeJSON(w, http.StatusOK, rows)
	return true
}

func parseResourcePathID(w http.ResponseWriter, r *http.Request, prefix, idLabel string) (uint64, []string, bool) {
	path := strings.TrimPrefix(r.URL.Path, prefix)
	path = strings.Trim(path, "/")
	if path == "" {
		w.WriteHeader(http.StatusNotFound)
		return 0, nil, false
	}

	parts := strings.Split(path, "/")
	id, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid %s", idLabel), http.StatusBadRequest)
		return 0, nil, false
	}
	return id, parts[1:], true
}

func loadModelByID[T any](
	ctx context.Context,
	w http.ResponseWriter,
	db *gorm.DB,
	id uint64,
	resource string,
	logMessage string,
	attrs ...any,
) (T, bool) {
	var model T
	if err := db.WithContext(ctx).First(&model, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			http.Error(w, fmt.Sprintf("%s not found", resource), http.StatusNotFound)
			return model, false
		}
		logAndRespondError(ctx, w, http.StatusInternalServerError, "database error", logMessage, err, attrs...)
		return model, false
	}
	return model, true
}

func writeCallbackTasksCSV(w http.ResponseWriter, tasks []CallbackTask) error {
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", "attachment; filename=\"dead_callback_tasks.csv\"")

	writer := csv.NewWriter(w)
	if err := writer.Write([]string{
		"id", "chain", "network", "address", "asset_type", "token_contract",
		"tx_hash", "block_height", "callback_url", "status", "retry_count", "max_retries",
		"last_error_type", "last_status_code", "last_error", "created_at", "updated_at",
	}); err != nil {
		return err
	}
	for _, task := range tasks {
		if err := writer.Write([]string{
			strconv.FormatUint(task.ID, 10),
			task.Chain,
			task.Network,
			task.Address,
			task.AssetType,
			task.TokenContract,
			task.TxHash,
			strconv.FormatUint(task.BlockHeight, 10),
			task.CallbackURL,
			task.Status,
			strconv.Itoa(task.RetryCount),
			strconv.Itoa(task.MaxRetries),
			task.LastErrorType,
			strconv.Itoa(task.LastStatus),
			task.LastError,
			task.CreatedAt.Format(time.RFC3339),
			task.UpdatedAt.Format(time.RFC3339),
		}); err != nil {
			return err
		}
	}
	writer.Flush()
	return writer.Error()
}

func decodeJSONBody(ctx context.Context, w http.ResponseWriter, r *http.Request, dest any, logMessage string, attrs ...any) bool {
	if err := json.NewDecoder(r.Body).Decode(dest); err != nil {
		logAndRespondError(ctx, w, http.StatusBadRequest, "invalid json", logMessage, err, attrs...)
		return false
	}
	return true
}

func requireNonEmptyField(w http.ResponseWriter, value, field string) bool {
	if strings.TrimSpace(value) != "" {
		return true
	}
	http.Error(w, fmt.Sprintf("%s is required", field), http.StatusBadRequest)
	return false
}

func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	w.WriteHeader(http.StatusMethodNotAllowed)
	return false
}

func dispatchMethods(w http.ResponseWriter, r *http.Request, handlers map[string]http.HandlerFunc) bool {
	handler, ok := handlers[r.Method]
	if !ok {
		allow := make([]string, 0, len(handlers))
		for method := range handlers {
			allow = append(allow, method)
		}
		sort.Strings(allow)
		w.Header().Set("Allow", strings.Join(allow, ", "))
		w.WriteHeader(http.StatusMethodNotAllowed)
		return false
	}
	handler(w, r)
	return true
}

type statCountSpec struct {
	dest  *int64
	model any
	where string
	args  []any
}

type callbackURLPolicy struct {
	raw          string
	host         string
	wildcard     bool
	wildcardBase string
}

func applyCreateAddressDefaults(req *CreateAddressRequest) {
	if req.Chain == "" {
		req.Chain = "tron"
	}
	if req.AssetType == "" {
		if strings.EqualFold(req.Chain, "evm") {
			req.AssetType = "erc20"
		} else {
			req.AssetType = "native"
		}
	}
	if req.MinConfirmations <= 0 {
		req.MinConfirmations = 1
	}

	req.Chain = strings.ToLower(strings.TrimSpace(req.Chain))
	if req.Network == "" {
		if req.Chain == "mock" {
			req.Network = "local"
		} else {
			req.Network = "mainnet"
		}
	}
	req.Network = strings.ToLower(strings.TrimSpace(req.Network))
	req.AssetType = strings.ToLower(strings.TrimSpace(req.AssetType))
	req.Address = strings.TrimSpace(req.Address)
	req.TokenContract = strings.TrimSpace(req.TokenContract)
	req.CallbackURL = strings.TrimSpace(req.CallbackURL)
}

func validateAndNormalizeCreateAddressRequest(req *CreateAddressRequest) error {
	switch req.Chain {
	case "tron":
		switch req.AssetType {
		case "native", "trc20":
		default:
			return errors.New("unsupported tron asset_type")
		}
		normalizedAddress, err := normalizeTronAddress(req.Address)
		if err != nil {
			return errors.New("invalid tron address")
		}
		req.Address = normalizedAddress
		if req.TokenContract != "" {
			normalizedContract, err := normalizeTronAddress(req.TokenContract)
			if err != nil {
				return errors.New("invalid tron token_contract")
			}
			req.TokenContract = normalizedContract
		}
		if req.AssetType == "trc20" && req.TokenContract == "" {
			return errors.New("token_contract is required for tron trc20 watcher")
		}
	case "evm":
		if req.AssetType != "erc20" {
			return errors.New("unsupported evm asset_type; supported: erc20")
		}
		normalizedAddress, err := normalizeEVMAddress(req.Address)
		if err != nil {
			return errors.New("invalid evm address")
		}
		req.Address = normalizedAddress
		if req.TokenContract == "" {
			return errors.New("token_contract is required for evm erc20 watcher")
		}
		normalizedContract, err := normalizeEVMAddress(req.TokenContract)
		if err != nil {
			return errors.New("invalid evm token_contract")
		}
		req.TokenContract = normalizedContract
	case "mock":
		switch req.AssetType {
		case "native", "trc20", "erc20":
		default:
			return errors.New("unsupported mock asset_type")
		}
	default:
		return errors.New("unsupported chain")
	}
	return nil
}

func buildAddressUpdates(req UpdateAddressRequest) (map[string]any, error) {
	updates := map[string]any{}
	if req.CallbackURL != nil {
		updates["callback_url"] = strings.TrimSpace(*req.CallbackURL)
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if req.MinConfirmations != nil {
		if *req.MinConfirmations <= 0 {
			return nil, errors.New("min_confirmations must be >= 1")
		}
		updates["min_confirmations"] = *req.MinConfirmations
	}
	if req.LastSeenHeight != nil {
		updates["last_seen_height"] = *req.LastSeenHeight
	}
	if len(updates) == 0 {
		return nil, errors.New("no updatable fields provided")
	}
	return updates, nil
}

func (app *App) normalizeMockTxRequest(ctx context.Context, req *CreateMockTxRequest) error {
	if req.Chain == "" {
		req.Chain = "mock"
	}
	if req.Network == "" {
		req.Network = "local"
	}
	req.Chain = strings.ToLower(strings.TrimSpace(req.Chain))
	req.Network = strings.ToLower(strings.TrimSpace(req.Network))
	req.Address = strings.TrimSpace(req.Address)
	req.Amount = strings.TrimSpace(req.Amount)
	req.From = strings.TrimSpace(req.From)
	req.To = strings.TrimSpace(req.To)
	req.TxHash = strings.TrimSpace(req.TxHash)

	if req.TxHash == "" {
		req.TxHash = time.Now().UTC().Format("20060102150405.000000000")
	}
	if req.From == "" {
		req.From = "mock_sender"
	}
	if req.To == "" {
		req.To = req.Address
	}
	if req.BlockHeight != 0 {
		return nil
	}

	var maxHeight uint64
	if err := app.db.WithContext(ctx).
		Model(&MockIncomingTx{}).
		Select("COALESCE(MAX(block_height), 0)").
		Scan(&maxHeight).Error; err != nil {
		return err
	}
	req.BlockHeight = maxHeight + 1
	return nil
}

func parseCallbackURLAllowlist(input string) ([]callbackURLPolicy, error) {
	if strings.TrimSpace(input) == "" {
		return nil, nil
	}

	var policies []callbackURLPolicy
	for _, raw := range strings.Split(input, ",") {
		value := strings.ToLower(strings.TrimSpace(raw))
		if value == "" {
			continue
		}

		policy := callbackURLPolicy{raw: value}
		if strings.HasPrefix(value, "*.") {
			base := strings.TrimPrefix(value, "*.")
			if base == "" || strings.Contains(base, "*") {
				return nil, fmt.Errorf("invalid callback URL allowlist entry %q", raw)
			}
			policy.wildcard = true
			policy.wildcardBase = base
		} else {
			if strings.Contains(value, "*") {
				return nil, fmt.Errorf("invalid callback URL allowlist entry %q", raw)
			}
			policy.host = value
		}
		policies = append(policies, policy)
	}

	return policies, nil
}

func validateCallbackURL(rawURL string, policies []callbackURLPolicy) error {
	value := strings.TrimSpace(rawURL)
	if value == "" {
		return nil
	}

	parsed, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("invalid callback_url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("callback_url must use http or https")
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "" {
		return errors.New("callback_url host is required")
	}
	if len(policies) == 0 {
		return nil
	}
	for _, policy := range policies {
		if policy.wildcard {
			if host != policy.wildcardBase && strings.HasSuffix(host, "."+policy.wildcardBase) {
				return nil
			}
			continue
		}
		if host == policy.host {
			return nil
		}
	}
	return fmt.Errorf("callback_url host %q is not in allowlist", host)
}

func (app *App) countModel(ctx context.Context, model any, where string, args ...any) (int64, error) {
	var total int64
	query := app.db.WithContext(ctx).Model(model)
	if where != "" {
		query = query.Where(where, args...)
	}
	if err := query.Count(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}

func (app *App) applyStatCountSpecs(ctx context.Context, specs []statCountSpec) error {
	for _, spec := range specs {
		total, err := app.countModel(ctx, spec.model, spec.where, spec.args...)
		if err != nil {
			return err
		}
		*spec.dest = total
	}
	return nil
}
