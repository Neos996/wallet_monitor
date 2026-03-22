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
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type CallbackTask struct {
	ID            uint64     `gorm:"primaryKey" json:"id"`
	Chain         string     `gorm:"uniqueIndex:uniq_callback_task;size:32;not null" json:"chain"`
	Network       string     `gorm:"uniqueIndex:uniq_callback_task;size:32;not null" json:"network"`
	Address       string     `gorm:"uniqueIndex:uniq_callback_task;size:128;not null" json:"address"`
	AssetType     string     `gorm:"uniqueIndex:uniq_callback_task;size:32;not null" json:"asset_type"`
	TokenContract string     `gorm:"uniqueIndex:uniq_callback_task;size:128" json:"token_contract"`
	TxHash        string     `gorm:"uniqueIndex:uniq_callback_task;size:128;not null" json:"tx_hash"`
	LogIndex      uint64     `gorm:"uniqueIndex:uniq_callback_task;not null;default:0" json:"log_index"`
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
	LastErrorType string     `gorm:"size:32" json:"last_error_type"`
	LastStatus    int        `json:"last_status_code"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type CallbackTaskRunResult struct {
	CallbacksSent   int `json:"callbacks_sent"`
	FailedCallbacks int `json:"failed_callbacks"`
	DeadCallbacks   int `json:"dead_callbacks"`
}

type CallbackError struct {
	Kind         string
	StatusCode   int
	ResponseBody string
	Err          error
}

func (e *CallbackError) Error() string {
	if e == nil {
		return ""
	}
	if e.StatusCode > 0 {
		if e.ResponseBody != "" {
			return fmt.Sprintf("%s (status=%d): %v body=%q", e.Kind, e.StatusCode, e.Err, e.ResponseBody)
		}
		return fmt.Sprintf("%s (status=%d): %v", e.Kind, e.StatusCode, e.Err)
	}
	return fmt.Sprintf("%s: %v", e.Kind, e.Err)
}

func (e *CallbackError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
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
		LogIndex:      tx.LogIndex,
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
	payload := CallbackPayload{
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

	if strings.ToLower(addr.Chain) == "evm" {
		value := tx.LogIndex
		payload.LogIndex = &value
	}

	return payload
}

func (app *App) processDueCallbackTasks(ctx context.Context, limit int) (CallbackTaskRunResult, error) {
	if limit <= 0 {
		limit = 100
	}
	if !app.callbackDispatchMu.TryLock() {
		metrics.callbackDispatchSkippedTotal.Add(1)
		loggerFromContext(ctx).Warn("callback dispatch skipped: already running")
		return CallbackTaskRunResult{}, errCallbackDispatchBusy
	}
	defer app.callbackDispatchMu.Unlock()

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

	workers := normalizeWorkerCount(app.callbackWorkers, len(tasks))

	taskCh := make(chan CallbackTask)
	resCh := make(chan error, len(tasks))

	var wg sync.WaitGroup
	for index := 0; index < workers; index++ {
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
	taskLogger := loggerFromContext(ctx).With(
		"task_id", task.ID,
		"chain", task.Chain,
		"network", task.Network,
		"address", task.Address,
		"asset_type", task.AssetType,
		"token_contract", task.TokenContract,
		"tx_hash", task.TxHash,
		"log_index", task.LogIndex,
		"retry_count", task.RetryCount,
		"callback_url", task.CallbackURL,
	)
	taskCtx := withLogger(ctx, taskLogger)
	startedAt := time.Now()
	now := time.Now().UTC()
	statusCode, err := app.sendCallbackBody(taskCtx, task.CallbackURL, task.Payload, task.ID)
	latency := time.Since(startedAt)
	if err != nil {
		cbErr := asCallbackError(err)
		nextRetryCount := task.RetryCount + 1
		status := "retrying"
		nextRetryAt := now.Add(calculateRetryDelay(app.callbackRetryBase, nextRetryCount))
		if !app.shouldRetryCallback(cbErr, nextRetryCount, task.MaxRetries) {
			status = "dead"
		}

		update := map[string]any{
			"status":          status,
			"retry_count":     nextRetryCount,
			"next_retry_at":   nextRetryAt,
			"last_attempt_at": now,
			"last_error":      err.Error(),
			"last_error_type": cbErr.Kind,
			"last_status":     cbErr.StatusCode,
		}
		if dbErr := app.db.WithContext(ctx).Model(&CallbackTask{}).Where("id = ?", task.ID).Updates(update).Error; dbErr != nil {
			return dbErr
		}
		recordCallbackAttempt(latency, cbErr, status == "dead")
		logArgs := []any{
			"status", status,
			"next_retry_count", nextRetryCount,
			"next_retry_at", nextRetryAt,
			"latency_ms", latency.Milliseconds(),
			"error_kind", cbErr.Kind,
			"http_status", cbErr.StatusCode,
		}
		if cbErr.ResponseBody != "" {
			logArgs = append(logArgs, "response_body", cbErr.ResponseBody)
		}
		if status == "dead" {
			taskLogger.Error("callback delivery moved to dead state", append(logArgs, "err", err)...)
		} else {
			taskLogger.Warn("callback delivery failed", append(logArgs, "err", err)...)
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
		LogIndex:      task.LogIndex,
		BlockHeight:   task.BlockHeight,
	}
	if err := app.db.WithContext(ctx).Create(&row).Error; err != nil && !isUniqueConstraintError(err) {
		return err
	}
	recordCallbackAttempt(latency, nil, false)
	taskLogger.Info("callback delivered", "latency_ms", latency.Milliseconds(), "http_status", statusCode)

	return nil
}

func (app *App) sendCallbackBody(ctx context.Context, callbackURL, payload string, taskID uint64) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, callbackURL, strings.NewReader(payload))
	if err != nil {
		return 0, &CallbackError{Kind: "request", Err: err}
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
		if errors.Is(err, context.DeadlineExceeded) {
			return 0, &CallbackError{Kind: "timeout", Err: err}
		}
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			return 0, &CallbackError{Kind: "timeout", Err: err}
		}
		if errors.Is(err, context.Canceled) {
			return 0, &CallbackError{Kind: "context", Err: err}
		}
		return 0, &CallbackError{Kind: "transport", Err: err}
	}
	defer resp.Body.Close()

	responseBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return resp.StatusCode, &CallbackError{Kind: "response_read", StatusCode: resp.StatusCode, Err: readErr}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, &CallbackError{
			Kind:         "non_2xx",
			StatusCode:   resp.StatusCode,
			ResponseBody: truncateText(string(responseBody), 512),
			Err:          fmt.Errorf("callback returned status %s", resp.Status),
		}
	}

	return resp.StatusCode, nil
}

func asCallbackError(err error) *CallbackError {
	if err == nil {
		return &CallbackError{Kind: "none"}
	}
	var cbErr *CallbackError
	if errors.As(err, &cbErr) && cbErr != nil {
		return cbErr
	}
	return &CallbackError{Kind: "unknown", Err: err}
}

func (app *App) shouldRetryCallback(err *CallbackError, retryCount, maxRetries int) bool {
	if retryCount >= maxRetries {
		return false
	}
	if err == nil {
		return false
	}

	switch err.Kind {
	case "timeout", "transport", "context":
		return true
	case "non_2xx":
		code := err.StatusCode
		if code >= 500 || code == 408 || code == 429 {
			return true
		}
		if app.retryOn4xx && code >= 400 && code < 500 {
			return true
		}
		if app.retryStatusCodes != nil && app.retryStatusCodes[code] {
			return true
		}
		return false
	default:
		return true
	}
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
