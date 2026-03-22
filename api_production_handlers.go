package main

import (
	"errors"
	"net/http"
	"strings"
)

type UpdateAddressRequest struct {
	CallbackURL      *string `json:"callback_url"`
	Enabled          *bool   `json:"enabled"`
	MinConfirmations *int    `json:"min_confirmations"`
	LastSeenHeight   *uint64 `json:"last_seen_height"`
}

func (app *App) handleAddressByID(w http.ResponseWriter, r *http.Request) {
	id, rest, ok := parseResourcePathID(w, r, "/addresses/", "address id")
	if !ok {
		return
	}

	if len(rest) == 0 {
		dispatchMethods(w, r, map[string]http.HandlerFunc{
			http.MethodGet:    func(w http.ResponseWriter, r *http.Request) { app.getAddressByID(w, r, id) },
			http.MethodPatch:  func(w http.ResponseWriter, r *http.Request) { app.updateAddress(w, r, id) },
			http.MethodDelete: func(w http.ResponseWriter, r *http.Request) { app.deleteAddress(w, r, id) },
		})
		return
	}

	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	switch rest[0] {
	case "enable":
		app.setAddressEnabled(w, r, id, true)
	case "disable":
		app.setAddressEnabled(w, r, id, false)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (app *App) getAddressByID(w http.ResponseWriter, r *http.Request, id uint64) {
	addr, ok := loadModelByID[WatchedAddress](
		r.Context(),
		w,
		app.db,
		id,
		"address",
		"get address failed",
		"address_id", id,
	)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, addr)
}

func (app *App) updateAddress(w http.ResponseWriter, r *http.Request, id uint64) {
	var req UpdateAddressRequest
	if !decodeJSONBody(r.Context(), w, r, &req, "decode update address request failed", "address_id", id) {
		return
	}

	updates, err := buildAddressUpdates(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if callbackURL, ok := updates["callback_url"].(string); ok {
		if err := validateCallbackURL(callbackURL, app.callbackURLPolicies); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	if err := app.db.WithContext(r.Context()).Model(&WatchedAddress{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		logAndRespondError(r.Context(), w, http.StatusInternalServerError, "database error", "update address failed", err, "address_id", id)
		return
	}
	loggerFromContext(r.Context()).Info("address updated", "address_id", id, "updates", marshalJSON(updates))
	app.getAddressByID(w, r, id)
}

func (app *App) deleteAddress(w http.ResponseWriter, r *http.Request, id uint64) {
	addr, ok := loadModelByID[WatchedAddress](
		r.Context(),
		w,
		app.db,
		id,
		"address",
		"load address before delete failed",
		"address_id", id,
	)
	if !ok {
		return
	}

	if err := app.db.WithContext(r.Context()).
		Where("chain = ? AND network = ? AND address = ? AND asset_type = ? AND token_contract = ? AND status IN ?",
			addr.Chain, addr.Network, addr.Address, addr.AssetType, addr.TokenContract, []string{"pending", "retrying", "dead"}).
		Delete(&CallbackTask{}).Error; err != nil {
		logAndRespondError(r.Context(), w, http.StatusInternalServerError, "database error", "delete callback tasks for address failed", err, "address_id", id)
		return
	}

	if err := app.db.WithContext(r.Context()).Delete(&WatchedAddress{}, id).Error; err != nil {
		logAndRespondError(r.Context(), w, http.StatusInternalServerError, "database error", "delete address failed", err, "address_id", id)
		return
	}
	loggerFromContext(r.Context()).Info("address deleted",
		"address_id", addr.ID,
		"chain", addr.Chain,
		"network", addr.Network,
		"address", addr.Address,
		"asset_type", addr.AssetType,
		"token_contract", addr.TokenContract,
	)
	w.WriteHeader(http.StatusNoContent)
}

func (app *App) setAddressEnabled(w http.ResponseWriter, r *http.Request, id uint64, enabled bool) {
	if err := app.db.WithContext(r.Context()).Model(&WatchedAddress{}).Where("id = ?", id).Update("enabled", enabled).Error; err != nil {
		logAndRespondError(r.Context(), w, http.StatusInternalServerError, "database error", "set address enabled failed", err, "address_id", id, "enabled", enabled)
		return
	}
	loggerFromContext(r.Context()).Info("address enable state changed", "address_id", id, "enabled", enabled)
	app.getAddressByID(w, r, id)
}

func (app *App) handleCallbackTasks(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}

	baseQuery := app.db.WithContext(r.Context()).Model(&CallbackTask{})
	if value := strings.TrimSpace(r.URL.Query().Get("status")); value != "" {
		baseQuery = baseQuery.Where("status = ?", strings.ToLower(value))
	}
	if value := strings.TrimSpace(r.URL.Query().Get("chain")); value != "" {
		baseQuery = baseQuery.Where("chain = ?", strings.ToLower(value))
	}
	if value := strings.TrimSpace(r.URL.Query().Get("address")); value != "" {
		baseQuery = baseQuery.Where("address = ?", value)
	}
	respondWithPaginatedQuery[CallbackTask](w, r, baseQuery, "id asc", 100, 1000, "callback tasks")
}

func (app *App) handleRetryCallbackTasks(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	result, err := app.retryCallbackTasks(
		r.Context(),
		app.db.WithContext(r.Context()).
			Model(&CallbackTask{}).
			Where("status IN ?", []string{"retrying", "dead"}),
		100,
	)
	if err != nil {
		if errors.Is(err, errCallbackDispatchBusy) {
			logAndRespondError(r.Context(), w, http.StatusConflict, "callback dispatch already running", "retry callback tasks rejected: dispatcher busy", err)
			return
		}
		logAndRespondError(r.Context(), w, http.StatusInternalServerError, "retry failed", "retry callback tasks failed", err)
		return
	}
	loggerFromContext(r.Context()).Info("callback tasks retried", "callbacks_sent", result.CallbacksSent, "failed_callbacks", result.FailedCallbacks, "dead_callbacks", result.DeadCallbacks)
	writeJSON(w, http.StatusOK, result)
}

func (app *App) handleExportDeadCallbackTasks(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}

	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "json"
	}

	var tasks []CallbackTask
	if err := app.db.WithContext(r.Context()).
		Where("status = ?", "dead").
		Order("id asc").
		Find(&tasks).Error; err != nil {
		logAndRespondError(r.Context(), w, http.StatusInternalServerError, "database error", "export dead callback tasks failed", err)
		return
	}

	switch format {
	case "csv":
		if err := writeCallbackTasksCSV(w, tasks); err != nil {
			logAndRespondError(r.Context(), w, http.StatusInternalServerError, "export failed", "write dead callback tasks csv failed", err)
			return
		}
		loggerFromContext(r.Context()).Info("dead callback tasks exported", "format", format, "count", len(tasks))
		return
	default:
		loggerFromContext(r.Context()).Info("dead callback tasks exported", "format", format, "count", len(tasks))
		writeJSON(w, http.StatusOK, tasks)
	}
}

func (app *App) handleCallbackTaskByID(w http.ResponseWriter, r *http.Request) {
	id, rest, ok := parseResourcePathID(w, r, "/callback-tasks/", "callback task id")
	if !ok {
		return
	}

	if len(rest) == 0 && r.Method == http.MethodGet {
		task, ok := loadModelByID[CallbackTask](
			r.Context(),
			w,
			app.db,
			id,
			"callback task",
			"get callback task failed",
			"task_id", id,
		)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, task)
		return
	}

	if len(rest) == 1 && rest[0] == "retry" && r.Method == http.MethodPost {
		result, err := app.retryCallbackTasks(
			r.Context(),
			app.db.WithContext(r.Context()).
				Model(&CallbackTask{}).
				Where("id = ?", id),
			1,
		)
		if err != nil {
			if errors.Is(err, errCallbackDispatchBusy) {
				logAndRespondError(r.Context(), w, http.StatusConflict, "callback dispatch already running", "retry callback task rejected: dispatcher busy", err, "task_id", id)
				return
			}
			logAndRespondError(r.Context(), w, http.StatusInternalServerError, "retry failed", "retry callback task failed", err, "task_id", id)
			return
		}
		loggerFromContext(r.Context()).Info("callback task retried", "task_id", id, "callbacks_sent", result.CallbacksSent, "failed_callbacks", result.FailedCallbacks, "dead_callbacks", result.DeadCallbacks)
		writeJSON(w, http.StatusOK, result)
		return
	}

	w.WriteHeader(http.StatusMethodNotAllowed)
}
