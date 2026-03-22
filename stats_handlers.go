package main

import (
	"context"
	"net/http"
)

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

func (app *App) handleStats(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}

	stats, err := app.collectStats(r.Context())
	if err != nil {
		logAndRespondError(r.Context(), w, http.StatusInternalServerError, "database error", "collect stats failed", err)
		return
	}

	writeJSON(w, http.StatusOK, stats)
}

func (app *App) collectStats(ctx context.Context) (StatsResponse, error) {
	stats := StatsResponse{}
	specs := []statCountSpec{
		{dest: &stats.WatchedTotal, model: &WatchedAddress{}},
		{dest: &stats.WatchedEnabled, model: &WatchedAddress{}, where: "enabled = ?", args: []any{true}},
		{dest: &stats.WatchedDisabled, model: &WatchedAddress{}, where: "enabled = ?", args: []any{false}},
		{dest: &stats.ProcessedTxTotal, model: &ProcessedTx{}},
		{dest: &stats.CallbackPending, model: &CallbackTask{}, where: "status = ?", args: []any{"pending"}},
		{dest: &stats.CallbackRetrying, model: &CallbackTask{}, where: "status = ?", args: []any{"retrying"}},
		{dest: &stats.CallbackSuccess, model: &CallbackTask{}, where: "status = ?", args: []any{"success"}},
		{dest: &stats.CallbackDead, model: &CallbackTask{}, where: "status = ?", args: []any{"dead"}},
		{dest: &stats.DebugCallbacks, model: &ReceivedCallback{}},
	}

	if err := app.applyStatCountSpecs(ctx, specs); err != nil {
		return stats, err
	}
	return stats, nil
}
