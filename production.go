package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

type CallbackTask struct {
	ID            uint64     `gorm:"primaryKey" json:"id"`
	Chain         string     `gorm:"uniqueIndex:uniq_callback_task;size:32;not null" json:"chain"`
	Network       string     `gorm:"uniqueIndex:uniq_callback_task;size:32;not null" json:"network"`
	Address       string     `gorm:"uniqueIndex:uniq_callback_task;size:128;not null" json:"address"`
	AssetType     string     `gorm:"uniqueIndex:uniq_callback_task;size:32;not null" json:"asset_type"`
	TokenContract string     `gorm:"uniqueIndex:uniq_callback_task;size:128" json:"token_contract"`
	TxHash        string     `gorm:"uniqueIndex:uniq_callback_task;size:128;not null" json:"tx_hash"`
	BlockHeight   uint64     `json:"block_height"`
	CallbackURL   string     `gorm:"size:256;not null" json:"callback_url"`
	Status        string     `gorm:"index;size:16;not null;default:pending" json:"status"`
	Payload       string     `gorm:"type:text;not null" json:"payload"`
	RetryCount    int        `gorm:"not null;default:0" json:"retry_count"`
	MaxRetries    int        `gorm:"not null;default:5" json:"max_retries"`
	NextRetryAt   time.Time  `gorm:"index" json:"next_retry_at"`
	LastAttemptAt *time.Time `json:"last_attempt_at"`
	DeliveredAt   *time.Time `json:"delivered_at"`
	LastError     string     `gorm:"type:text" json:"last_error"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type CallbackTaskRunResult struct {
	CallbacksSent   int `json:"callbacks_sent"`
	FailedCallbacks int `json:"failed_callbacks"`
	DeadCallbacks   int `json:"dead_callbacks"`
}

type UpdateAddressRequest struct {
	CallbackURL      *string `json:"callback_url"`
	Enabled          *bool   `json:"enabled"`
	MinConfirmations *int    `json:"min_confirmations"`
	LastSeenHeight   *uint64 `json:"last_seen_height"`
}

type StatsResponse struct {
	WatchedTotal     int64 `json:"watched_total"`
	WatchedEnabled   int64 `json:"watched_enabled"`
	WatchedDisabled  int64 `json:"watched_disabled"`
	ProcessedTxTotal int64 `json:"processed_tx_total"`
	CallbackPending  int64 `json:"callback_pending"`
	CallbackRetrying int64 `json:"callback_retrying"`
	CallbackSuccess  int64 `json:"callback_success"`
	CallbackDead     int64 `json:"callback_dead"`
	DebugCallbacks   int64 `json:"debug_callbacks"`
}

func calculateConfirmedCutoff(currentBlock uint64, minConfirmations int) uint64 {
	if minConfirmations <= 1 {
		return currentBlock
	}
	requiredGap := uint64(minConfirmations - 1)
	if currentBlock < requiredGap {
		return 0
	}
	return currentBlock - requiredGap
}

func (app *App) enqueueCallbackTask(ctx context.Context, addr WatchedAddress, callbackURL string, tx Tx) (bool, error) {
	payload := app.buildCallbackPayload(addr, tx)
	body, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}

	task := CallbackTask{
		Chain:         addr.Chain,
		Network:       addr.Network,
		Address:       addr.Address,
		AssetType:     addr.AssetType,
		TokenContract: addr.TokenContract,
		TxHash:        tx.Hash,
		BlockHeight:   tx.BlockHeight,
		CallbackURL:   callbackURL,
		Status:        "pending",
		Payload:       string(body),
		MaxRetries:    app.maxCallbackRetries,
		NextRetryAt:   time.Now().UTC(),
	}

	if err := app.db.WithContext(ctx).Create(&task).Error; err != nil {
		if isUniqueConstraintError(err) {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

func (app *App) buildCallbackPayload(addr WatchedAddress, tx Tx) CallbackPayload {
	return CallbackPayload{
		Chain:         addr.Chain,
		Network:       addr.Network,
		AssetType:     addr.AssetType,
		TokenContract: tx.TokenContract,
		TokenSymbol:   tx.TokenSymbol,
		TokenDecimals: tx.TokenDecimals,
		Address:       addr.Address,
		TxHash:        tx.Hash,
		From:          tx.From,
		To:            tx.To,
		Amount:        tx.Amount,
		BlockHeight:   tx.BlockHeight,
	}
}

func (app *App) processDueCallbackTasks(ctx context.Context, limit int) (CallbackTaskRunResult, error) {
	if limit <= 0 {
		limit = 100
	}

	var tasks []CallbackTask
	now := time.Now().UTC()
	if err := app.db.WithContext(ctx).
		Where("status IN ? AND next_retry_at <= ?", []string{"pending", "retrying"}, now).
		Order("id asc").
		Limit(limit).
		Find(&tasks).Error; err != nil {
		return CallbackTaskRunResult{}, err
	}

	result := CallbackTaskRunResult{}
	if len(tasks) == 0 {
		return result, nil
	}

	workers := app.callbackWorkers
	if workers <= 0 {
		workers = 1
	}
	if workers > len(tasks) {
		workers = len(tasks)
	}

	taskCh := make(chan CallbackTask)
	resCh := make(chan error, len(tasks))

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range taskCh {
				if app.callbackLimiter != nil {
					if err := app.callbackLimiter.Wait(ctx); err != nil {
						resCh <- err
						continue
					}
				}
				resCh <- app.deliverCallbackTask(ctx, task)
			}
		}()
	}

	go func() {
		for _, task := range tasks {
			taskCh <- task
		}
		close(taskCh)
		wg.Wait()
		close(resCh)
	}()

	for err := range resCh {
		if err != nil {
			result.FailedCallbacks++
			if errors.Is(err, errCallbackDead) {
				result.DeadCallbacks++
			}
			continue
		}
		result.CallbacksSent++
	}

	return result, nil
}

var errCallbackDead = errors.New("callback task moved to dead state")

func (app *App) deliverCallbackTask(ctx context.Context, task CallbackTask) error {
	now := time.Now().UTC()
	if err := app.sendCallbackBody(ctx, task.CallbackURL, task.Payload, task.ID); err != nil {
		nextRetryCount := task.RetryCount + 1
		status := "retrying"
		nextRetryAt := now.Add(calculateRetryDelay(app.callbackRetryBase, nextRetryCount))
		if nextRetryCount >= task.MaxRetries {
			status = "dead"
		}

		update := map[string]any{
			"status":          status,
			"retry_count":     nextRetryCount,
			"next_retry_at":   nextRetryAt,
			"last_attempt_at": now,
			"last_error":      err.Error(),
		}
		if dbErr := app.db.WithContext(ctx).Model(&CallbackTask{}).Where("id = ?", task.ID).Updates(update).Error; dbErr != nil {
			return dbErr
		}
		if status == "dead" {
			return errCallbackDead
		}
		return err
	}

	update := map[string]any{
		"status":          "success",
		"last_attempt_at": now,
		"delivered_at":    now,
		"last_error":      "",
	}
	if err := app.db.WithContext(ctx).Model(&CallbackTask{}).Where("id = ?", task.ID).Updates(update).Error; err != nil {
		return err
	}

	row := ProcessedTx{
		Chain:         task.Chain,
		Network:       task.Network,
		Address:       task.Address,
		AssetType:     task.AssetType,
		TokenContract: task.TokenContract,
		TxHash:        task.TxHash,
		BlockHeight:   task.BlockHeight,
	}
	if err := app.db.WithContext(ctx).Create(&row).Error; err != nil && !isUniqueConstraintError(err) {
		return err
	}

	return nil
}

func (app *App) sendCallbackBody(ctx context.Context, callbackURL, payload string, taskID uint64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, callbackURL, strings.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-WalletMonitor-Event-ID", strconv.FormatUint(taskID, 10))

	if app.callbackSecret != "" {
		timestamp := strconv.FormatInt(time.Now().UTC().Unix(), 10)
		req.Header.Set("X-WalletMonitor-Timestamp", timestamp)
		req.Header.Set("X-WalletMonitor-Signature", signCallbackPayload(app.callbackSecret, timestamp, payload))
	}

	resp, err := app.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("callback returned status %s", resp.Status)
	}

	return nil
}

func signCallbackPayload(secret, timestamp, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func calculateRetryDelay(base time.Duration, retryCount int) time.Duration {
	if base <= 0 {
		base = 10 * time.Second
	}
	if retryCount <= 1 {
		return base
	}
	multiplier := 1 << min(retryCount-1, 6)
	return time.Duration(multiplier) * base
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	errText := strings.ToLower(err.Error())
	return strings.Contains(errText, "unique constraint") || strings.Contains(errText, "duplicated key") || strings.Contains(errText, "duplicate key")
}

func (app *App) handleAddressByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/addresses/")
	path = strings.Trim(path, "/")
	if path == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	parts := strings.Split(path, "/")
	id, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "invalid address id", http.StatusBadRequest)
		return
	}

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			app.getAddressByID(w, r, id)
		case http.MethodPatch:
			app.updateAddress(w, r, id)
		case http.MethodDelete:
			app.deleteAddress(w, r, id)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	switch parts[1] {
	case "enable":
		app.setAddressEnabled(w, r, id, true)
	case "disable":
		app.setAddressEnabled(w, r, id, false)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (app *App) getAddressByID(w http.ResponseWriter, r *http.Request, id uint64) {
	var addr WatchedAddress
	if err := app.db.WithContext(r.Context()).First(&addr, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			http.Error(w, "address not found", http.StatusNotFound)
			return
		}
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, addr)
}

func (app *App) updateAddress(w http.ResponseWriter, r *http.Request, id uint64) {
	var req UpdateAddressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	updates := map[string]any{}
	if req.CallbackURL != nil {
		updates["callback_url"] = strings.TrimSpace(*req.CallbackURL)
	}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if req.MinConfirmations != nil {
		if *req.MinConfirmations <= 0 {
			http.Error(w, "min_confirmations must be >= 1", http.StatusBadRequest)
			return
		}
		updates["min_confirmations"] = *req.MinConfirmations
	}
	if req.LastSeenHeight != nil {
		updates["last_seen_height"] = *req.LastSeenHeight
	}
	if len(updates) == 0 {
		http.Error(w, "no updatable fields provided", http.StatusBadRequest)
		return
	}

	if err := app.db.WithContext(r.Context()).Model(&WatchedAddress{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	app.getAddressByID(w, r, id)
}

func (app *App) deleteAddress(w http.ResponseWriter, r *http.Request, id uint64) {
	var addr WatchedAddress
	if err := app.db.WithContext(r.Context()).First(&addr, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			http.Error(w, "address not found", http.StatusNotFound)
			return
		}
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	if err := app.db.WithContext(r.Context()).
		Where("chain = ? AND network = ? AND address = ? AND asset_type = ? AND token_contract = ? AND status IN ?",
			addr.Chain, addr.Network, addr.Address, addr.AssetType, addr.TokenContract, []string{"pending", "retrying", "dead"}).
		Delete(&CallbackTask{}).Error; err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	if err := app.db.WithContext(r.Context()).Delete(&WatchedAddress{}, id).Error; err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (app *App) setAddressEnabled(w http.ResponseWriter, r *http.Request, id uint64, enabled bool) {
	if err := app.db.WithContext(r.Context()).Model(&WatchedAddress{}).Where("id = ?", id).Update("enabled", enabled).Error; err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	app.getAddressByID(w, r, id)
}

func (app *App) handleCallbackTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	query := app.db.WithContext(r.Context()).Order("id asc")
	if value := strings.TrimSpace(r.URL.Query().Get("status")); value != "" {
		query = query.Where("status = ?", strings.ToLower(value))
	}
	if value := strings.TrimSpace(r.URL.Query().Get("chain")); value != "" {
		query = query.Where("chain = ?", strings.ToLower(value))
	}
	if value := strings.TrimSpace(r.URL.Query().Get("address")); value != "" {
		query = query.Where("address = ?", value)
	}

	var tasks []CallbackTask
	if err := query.Find(&tasks).Error; err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (app *App) handleRetryCallbackTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	now := time.Now().UTC()
	if err := app.db.WithContext(r.Context()).
		Model(&CallbackTask{}).
		Where("status IN ?", []string{"retrying", "dead"}).
		Updates(map[string]any{"status": "retrying", "next_retry_at": now}).Error; err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	result, err := app.processDueCallbackTasks(r.Context(), 100)
	if err != nil {
		http.Error(w, "retry failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (app *App) handleCallbackTaskByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/callback-tasks/")
	path = strings.Trim(path, "/")
	if path == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	parts := strings.Split(path, "/")
	id, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "invalid callback task id", http.StatusBadRequest)
		return
	}

	if len(parts) == 1 && r.Method == http.MethodGet {
		var task CallbackTask
		if err := app.db.WithContext(r.Context()).First(&task, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				http.Error(w, "callback task not found", http.StatusNotFound)
				return
			}
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, task)
		return
	}

	if len(parts) == 2 && parts[1] == "retry" && r.Method == http.MethodPost {
		now := time.Now().UTC()
		if err := app.db.WithContext(r.Context()).
			Model(&CallbackTask{}).
			Where("id = ?", id).
			Updates(map[string]any{"status": "retrying", "next_retry_at": now}).Error; err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		result, err := app.processDueCallbackTasks(r.Context(), 1)
		if err != nil {
			http.Error(w, "retry failed", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}

	w.WriteHeader(http.StatusMethodNotAllowed)
}

func (app *App) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	stats, err := app.collectStats(r.Context())
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, stats)
}

func (app *App) collectStats(ctx context.Context) (StatsResponse, error) {
	stats := StatsResponse{}
	count := func(model any, where string, args ...any) (int64, error) {
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

	var err error
	if stats.WatchedTotal, err = count(&WatchedAddress{}, ""); err != nil {
		return stats, err
	}
	if stats.WatchedEnabled, err = count(&WatchedAddress{}, "enabled = ?", true); err != nil {
		return stats, err
	}
	if stats.WatchedDisabled, err = count(&WatchedAddress{}, "enabled = ?", false); err != nil {
		return stats, err
	}
	if stats.ProcessedTxTotal, err = count(&ProcessedTx{}, ""); err != nil {
		return stats, err
	}
	if stats.CallbackPending, err = count(&CallbackTask{}, "status = ?", "pending"); err != nil {
		return stats, err
	}
	if stats.CallbackRetrying, err = count(&CallbackTask{}, "status = ?", "retrying"); err != nil {
		return stats, err
	}
	if stats.CallbackSuccess, err = count(&CallbackTask{}, "status = ?", "success"); err != nil {
		return stats, err
	}
	if stats.CallbackDead, err = count(&CallbackTask{}, "status = ?", "dead"); err != nil {
		return stats, err
	}
	if stats.DebugCallbacks, err = count(&ReceivedCallback{}, ""); err != nil {
		return stats, err
	}

	return stats, nil
}
