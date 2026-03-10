package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	tronclient "wallet_monitor/internal/tron"
)

type WatchedAddress struct {
	ID               uint64    `gorm:"primaryKey" json:"id"`
	Chain            string    `gorm:"uniqueIndex:uniq_watched_target;index;size:32;not null;default:tron" json:"chain"`
	Network          string    `gorm:"uniqueIndex:uniq_watched_target;index;size:32;not null;default:mainnet" json:"network"`
	Address          string    `gorm:"uniqueIndex:uniq_watched_target;size:128;not null" json:"address"`
	AssetType        string    `gorm:"uniqueIndex:uniq_watched_target;index;size:32;not null;default:native" json:"asset_type"`
	TokenContract    string    `gorm:"uniqueIndex:uniq_watched_target;size:128" json:"token_contract"`
	CallbackURL      string    `gorm:"size:256" json:"callback_url"`
	Enabled          bool      `gorm:"index;not null;default:true" json:"enabled"`
	MinConfirmations int       `gorm:"not null;default:1" json:"min_confirmations"`
	LastSeenHeight   uint64    `json:"last_seen_height"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type ProcessedTx struct {
	ID            uint64    `gorm:"primaryKey" json:"id"`
	Chain         string    `gorm:"uniqueIndex:uniq_processed_delivery;size:32;not null" json:"chain"`
	Network       string    `gorm:"uniqueIndex:uniq_processed_delivery;size:32;not null" json:"network"`
	Address       string    `gorm:"uniqueIndex:uniq_processed_delivery;size:128;not null" json:"address"`
	AssetType     string    `gorm:"uniqueIndex:uniq_processed_delivery;size:32;not null" json:"asset_type"`
	TokenContract string    `gorm:"uniqueIndex:uniq_processed_delivery;size:128" json:"token_contract"`
	TxHash        string    `gorm:"uniqueIndex:uniq_processed_delivery;size:128;not null" json:"tx_hash"`
	BlockHeight   uint64    `json:"block_height"`
	CreatedAt     time.Time `json:"created_at"`
}

type MockIncomingTx struct {
	ID          uint64    `gorm:"primaryKey" json:"id"`
	Chain       string    `gorm:"index;size:32;not null;default:mock" json:"chain"`
	Network     string    `gorm:"index;size:32;not null;default:local" json:"network"`
	Address     string    `gorm:"index;size:128;not null" json:"address"`
	TxHash      string    `gorm:"uniqueIndex:uniq_mock_tx;size:128;not null" json:"tx_hash"`
	From        string    `gorm:"size:128;not null" json:"from"`
	To          string    `gorm:"size:128;not null" json:"to"`
	Amount      string    `gorm:"size:64;not null" json:"amount"`
	BlockHeight uint64    `json:"block_height"`
	Delivered   bool      `gorm:"index;not null;default:false" json:"delivered"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type ReceivedCallback struct {
	ID          uint64    `gorm:"primaryKey" json:"id"`
	Address     string    `gorm:"index;size:128" json:"address"`
	TxHash      string    `gorm:"index;size:128" json:"tx_hash"`
	BlockHeight uint64    `json:"block_height"`
	Payload     string    `gorm:"type:text;not null" json:"payload"`
	CreatedAt   time.Time `json:"created_at"`
}

type Tx struct {
	SourceID      uint64
	Hash          string
	From          string
	To            string
	Amount        string
	BlockHeight   uint64
	AssetType     string
	TokenContract string
	TokenSymbol   string
	TokenDecimals int
}

type CallbackPayload struct {
	Chain         string `json:"chain"`
	Network       string `json:"network"`
	AssetType     string `json:"asset_type"`
	TokenContract string `json:"token_contract,omitempty"`
	TokenSymbol   string `json:"token_symbol,omitempty"`
	TokenDecimals int    `json:"token_decimals,omitempty"`
	Address       string `json:"address"`
	TxHash        string `json:"tx_hash"`
	From          string `json:"from"`
	To            string `json:"to"`
	Amount        string `json:"amount"`
	BlockHeight   uint64 `json:"block_height"`
}

type CreateAddressRequest struct {
	Chain            string  `json:"chain"`
	Network          string  `json:"network"`
	Address          string  `json:"address"`
	AssetType        string  `json:"asset_type"`
	TokenContract    string  `json:"token_contract"`
	CallbackURL      string  `json:"callback_url"`
	MinConfirmations int     `json:"min_confirmations"`
	StartHeight      *uint64 `json:"start_height"`
}

type CreateMockTxRequest struct {
	Chain       string `json:"chain"`
	Network     string `json:"network"`
	Address     string `json:"address"`
	TxHash      string `json:"tx_hash"`
	From        string `json:"from"`
	To          string `json:"to"`
	Amount      string `json:"amount"`
	BlockHeight uint64 `json:"block_height"`
}

type ScanResult struct {
	AddressesScanned int       `json:"addresses_scanned"`
	DetectedTxs      int       `json:"detected_txs"`
	QueuedCallbacks  int       `json:"queued_callbacks"`
	CallbacksSent    int       `json:"callbacks_sent"`
	DuplicateTxs     int       `json:"duplicate_txs"`
	FailedCallbacks  int       `json:"failed_callbacks"`
	DeadCallbacks    int       `json:"dead_callbacks"`
	UpdatedAddresses int       `json:"updated_addresses"`
	ScannedAt        time.Time `json:"scanned_at"`
}

type addressResult struct {
	addressesScanned int
	detectedTxs      int
	queuedCallbacks  int
	duplicateTxs     int
	failedCallbacks  int
	updatedAddresses int
}

type BlockchainClient interface {
	ScanAddress(ctx context.Context, watched WatchedAddress) (changed bool, newHeight uint64, txs []Tx, err error)
}

type MultiClient struct {
	db         *gorm.DB
	rpcURL     string
	tronAPIKey string
	tronQPS    float64
	tronRetry  int
}

func (c *MultiClient) ScanAddress(ctx context.Context, watched WatchedAddress) (bool, uint64, []Tx, error) {
	switch strings.ToLower(watched.Chain) {
	case "mock":
		return c.scanMockAddress(ctx, watched)
	case "tron":
		return c.scanTronAddress(ctx, watched)
	}

	return false, watched.LastSeenHeight, nil, errors.New("chain adapter not implemented; supported now: chain=mock for local validation, chain=tron for confirmed TRX and TRC20 transfers")
}

func (c *MultiClient) scanMockAddress(ctx context.Context, watched WatchedAddress) (bool, uint64, []Tx, error) {
	var rows []MockIncomingTx
	if err := c.db.WithContext(ctx).
		Where("chain = ? AND network = ? AND address = ? AND delivered = ?", "mock", watched.Network, watched.Address, false).
		Order("block_height asc, id asc").
		Find(&rows).Error; err != nil {
		return false, 0, nil, err
	}

	if len(rows) == 0 {
		return false, 0, nil, nil
	}

	txs := make([]Tx, 0, len(rows))
	var newHeight uint64
	for _, row := range rows {
		txs = append(txs, Tx{
			SourceID:      row.ID,
			Hash:          row.TxHash,
			From:          row.From,
			To:            row.To,
			Amount:        row.Amount,
			BlockHeight:   row.BlockHeight,
			AssetType:     watched.AssetType,
			TokenContract: watched.TokenContract,
		})
		if row.BlockHeight > newHeight {
			newHeight = row.BlockHeight
		}
	}

	return true, newHeight, txs, nil
}

func (c *MultiClient) scanTronAddress(ctx context.Context, watched WatchedAddress) (bool, uint64, []Tx, error) {
	apiURL := c.resolveTronAPIURL(strings.ToLower(watched.Network))
	client := tronclient.NewClient(apiURL).
		WithAPIKey(c.tronAPIKey).
		WithRateLimiter(tronclient.NewRateLimiter(c.tronQPS)).
		WithRetry429(c.tronRetry, 500*time.Millisecond)
	currentBlock, err := client.GetNowBlockNumber(ctx)
	if err != nil {
		return false, watched.LastSeenHeight, nil, err
	}
	confirmedCutoff := calculateConfirmedCutoff(currentBlock, watched.MinConfirmations)

	switch watched.AssetType {
	case "", "native":
		incomingTxs, _, err := client.GetIncomingTRXTransactions(ctx, watched.Address, watched.LastSeenHeight, 200)
		if err != nil {
			return false, watched.LastSeenHeight, nil, err
		}

		txs := make([]Tx, 0, len(incomingTxs))
		newHeight := watched.LastSeenHeight
		if confirmedCutoff > newHeight {
			newHeight = confirmedCutoff
		}
		for _, tx := range incomingTxs {
			if tx.BlockNumber > confirmedCutoff {
				continue
			}
			txs = append(txs, Tx{
				Hash:        tx.TxID,
				From:        tx.From,
				To:          tx.To,
				Amount:      tx.Amount,
				BlockHeight: tx.BlockNumber,
				AssetType:   "native",
				TokenSymbol: "TRX",
			})
		}
		return len(txs) > 0 || newHeight > watched.LastSeenHeight, newHeight, txs, nil
	case "trc20":
		incomingTxs, _, err := client.GetIncomingTRC20Transactions(ctx, watched.Address, watched.TokenContract, watched.LastSeenHeight, 200)
		if err != nil {
			return false, watched.LastSeenHeight, nil, err
		}

		txs := make([]Tx, 0, len(incomingTxs))
		newHeight := watched.LastSeenHeight
		if confirmedCutoff > newHeight {
			newHeight = confirmedCutoff
		}
		for _, tx := range incomingTxs {
			if tx.BlockNumber > confirmedCutoff {
				continue
			}
			txs = append(txs, Tx{
				Hash:          tx.TxID,
				From:          tx.From,
				To:            tx.To,
				Amount:        tx.Amount,
				BlockHeight:   tx.BlockNumber,
				AssetType:     "trc20",
				TokenContract: tx.TokenAddress,
				TokenSymbol:   tx.TokenSymbol,
				TokenDecimals: tx.TokenDecimals,
			})
		}
		return len(txs) > 0 || newHeight > watched.LastSeenHeight, newHeight, txs, nil
	default:
		return false, watched.LastSeenHeight, nil, errors.New("unsupported tron asset type; supported: native, trc20")
	}
}

func (c *MultiClient) resolveTronAPIURL(network string) string {
	return resolveTronAPIURL(c.rpcURL, network)
}

type App struct {
	db                 *gorm.DB
	scanner            BlockchainClient
	defaultCallbackURL string
	httpClient         *http.Client
	callbackSecret     string
	callbackRetryBase  time.Duration
	maxCallbackRetries int
	adminToken         string
	rpcURL             string
	tronAPIKey         string
	scanWorkers        int
	callbackBatch      int
	callbackWorkers    int
	callbackLimiter    *RateLimiter
	retryOn4xx         bool
	retryStatusCodes   map[int]bool
}

func main() {
	dbPath := flag.String("db", "wallets.db", "path to sqlite database file")
	callbackURL := flag.String("callback-url", "", "default HTTP callback URL for incoming payments (can be overridden per address)")
	callbackSecret := flag.String("callback-secret", "", "optional HMAC secret used to sign callback payloads")
	callbackRetryBase := flag.Duration("callback-retry-base", 10*time.Second, "base retry interval for failed callbacks")
	maxCallbackRetries := flag.Int("callback-max-retries", 5, "maximum retry attempts for failed callbacks")
	adminToken := flag.String("admin-token", "", "optional bearer token to protect admin APIs (Authorization: Bearer ... or X-Admin-Token)")
	rpcURL := flag.String("rpc-url", "", "blockchain RPC endpoint")
	tronAPIKey := flag.String("tron-api-key", "", "optional TronGrid API key for higher rate limits")
	tronQPS := flag.Float64("tron-qps", 8, "global Tron API QPS limit (0 disables)")
	tronRetry429 := flag.Int("tron-retry-429", 3, "number of retries for HTTP 429 from Tron API")
	scanWorkers := flag.Int("scan-workers", 4, "number of concurrent address scans per tick")
	callbackBatch := flag.Int("callback-batch", 100, "max callback tasks to process per scan loop")
	callbackWorkers := flag.Int("callback-workers", 4, "number of concurrent callback deliveries")
	callbackQPS := flag.Float64("callback-qps", 0, "global callback rate limit (qps); 0 disables")
	callbackRetryOn4xx := flag.Bool("callback-retry-4xx", false, "retry callbacks on 4xx responses")
	callbackRetryStatuses := flag.String("callback-retry-statuses", "", "comma-separated HTTP status codes to always retry (e.g. 409,425)")
	scanInterval := flag.Duration("scan-interval", 15*time.Second, "scan interval")
	listenAddr := flag.String("listen", ":8080", "HTTP listen address for admin API")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	db, err := gorm.Open(sqlite.Open(*dbPath), &gorm.Config{})
	if err != nil {
		slog.Error("open db failed", "err", err)
		os.Exit(1)
	}

	if err := db.AutoMigrate(&WatchedAddress{}, &ProcessedTx{}, &MockIncomingTx{}, &ReceivedCallback{}, &CallbackTask{}); err != nil {
		slog.Error("auto migrate failed", "err", err)
		os.Exit(1)
	}
	if err := migrateLegacyIndexes(db); err != nil {
		slog.Error("migrate legacy indexes failed", "err", err)
		os.Exit(1)
	}

	callbackLimiter := NewRateLimiter(*callbackQPS)

	retryStatusCodes := parseRetryStatusCodes(*callbackRetryStatuses)

	app := &App{
		db:                 db,
		scanner:            &MultiClient{db: db, rpcURL: *rpcURL, tronAPIKey: *tronAPIKey, tronQPS: *tronQPS, tronRetry: *tronRetry429},
		defaultCallbackURL: *callbackURL,
		httpClient:         &http.Client{Timeout: 10 * time.Second},
		callbackSecret:     *callbackSecret,
		callbackRetryBase:  *callbackRetryBase,
		maxCallbackRetries: *maxCallbackRetries,
		adminToken:         strings.TrimSpace(*adminToken),
		rpcURL:             strings.TrimSpace(*rpcURL),
		tronAPIKey:         strings.TrimSpace(*tronAPIKey),
		scanWorkers:        *scanWorkers,
		callbackBatch:      *callbackBatch,
		callbackWorkers:    *callbackWorkers,
		callbackLimiter:    callbackLimiter,
		retryOn4xx:         *callbackRetryOn4xx,
		retryStatusCodes:   retryStatusCodes,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *callbackURL == "" {
		slog.Warn("no global callback-url configured; expect per-address callback_url")
	}

	go func() {
		if err := runScanner(ctx, app, *scanInterval); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("scanner stopped", "err", err)
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/addresses", app.requireAdmin(app.handleAddresses))
	mux.HandleFunc("/addresses/", app.requireAdmin(app.handleAddressByID))
	mux.HandleFunc("/scan/once", app.requireAdmin(app.handleScanOnce))
	mux.HandleFunc("/callback-tasks", app.requireAdmin(app.handleCallbackTasks))
	mux.HandleFunc("/callback-tasks/", app.requireAdmin(app.handleCallbackTaskByID))
	mux.HandleFunc("/callback-tasks/retry", app.requireAdmin(app.handleRetryCallbackTasks))
	mux.HandleFunc("/callback-tasks/dead/export", app.requireAdmin(app.handleExportDeadCallbackTasks))
	mux.HandleFunc("/stats", app.requireAdmin(app.handleStats))
	mux.HandleFunc("/mock/transactions", app.requireAdmin(app.handleMockTransactions))
	mux.HandleFunc("/debug/callbacks", app.requireAdmin(app.handleDebugCallbacks))
	mux.HandleFunc("/metrics", app.requireAdmin(app.handleMetrics))

	server := &http.Server{
		Addr:    *listenAddr,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("http server shutdown error", "err", err)
		}
	}()

	slog.Info("wallet monitor started",
		"db", *dbPath,
		"interval", scanInterval.String(),
		"default_callback", *callbackURL,
		"rpc", *rpcURL,
		"tron_qps", *tronQPS,
		"tron_retry_429", *tronRetry429,
		"listen", *listenAddr,
		"scan_workers", app.scanWorkers,
		"callback_batch", app.callbackBatch,
		"callback_workers", app.callbackWorkers,
		"callback_qps", *callbackQPS,
		"callback_retry_4xx", *callbackRetryOn4xx,
		"callback_retry_statuses", *callbackRetryStatuses,
	)

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("http server error", "err", err)
		os.Exit(1)
	}

	slog.Info("wallet monitor exited")
}

func runScanner(ctx context.Context, app *App, interval time.Duration) error {
	if interval <= 0 {
		interval = 15 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Run one scan immediately on startup so new deployments don't wait for the first tick.
	{
		result, err := app.scanOnce(ctx)
		if err != nil {
			return err
		}
		slog.Info("scan complete",
			"addresses", result.AddressesScanned,
			"txs", result.DetectedTxs,
			"queued", result.QueuedCallbacks,
			"callbacks", result.CallbacksSent,
			"duplicates", result.DuplicateTxs,
			"failed", result.FailedCallbacks,
			"dead", result.DeadCallbacks,
		)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			result, err := app.scanOnce(ctx)
			if err != nil {
				slog.Error("scan error", "err", err)
				continue
			}
			slog.Info("scan complete",
				"addresses", result.AddressesScanned,
				"txs", result.DetectedTxs,
				"queued", result.QueuedCallbacks,
				"callbacks", result.CallbacksSent,
				"duplicates", result.DuplicateTxs,
				"failed", result.FailedCallbacks,
				"dead", result.DeadCallbacks,
			)
		}
	}
}

func (app *App) scanOnce(ctx context.Context) (ScanResult, error) {
	startedAt := time.Now()
	result := ScanResult{ScannedAt: time.Now().UTC()}

	var addresses []WatchedAddress
	if err := app.db.WithContext(ctx).Where("enabled = ?", true).Find(&addresses).Error; err != nil {
		return result, err
	}

	workers := app.scanWorkers
	if workers <= 0 {
		workers = 1
	}
	if workers > len(addresses) {
		workers = len(addresses)
	}
	if workers == 0 {
		workers = 1
	}

	addrCh := make(chan WatchedAddress)
	resCh := make(chan addressResult, len(addresses))

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for addr := range addrCh {
				resCh <- app.scanOneAddress(ctx, addr)
			}
		}()
	}

	go func() {
		for _, addr := range addresses {
			addrCh <- addr
		}
		close(addrCh)
		wg.Wait()
		close(resCh)
	}()

	for res := range resCh {
		result.AddressesScanned += res.addressesScanned
		result.DetectedTxs += res.detectedTxs
		result.QueuedCallbacks += res.queuedCallbacks
		result.DuplicateTxs += res.duplicateTxs
		result.FailedCallbacks += res.failedCallbacks
		result.UpdatedAddresses += res.updatedAddresses
	}

	batch := app.callbackBatch
	if batch <= 0 {
		batch = 100
	}
	taskResult, err := app.processDueCallbackTasks(ctx, batch)
	if err != nil {
		return result, err
	}
	result.CallbacksSent += taskResult.CallbacksSent
	result.FailedCallbacks += taskResult.FailedCallbacks
	result.DeadCallbacks += taskResult.DeadCallbacks

	recordScanMetrics(result, startedAt)
	app.updateMetrics(ctx)

	return result, nil
}

func (app *App) scanOneAddress(ctx context.Context, addr WatchedAddress) addressResult {
	res := addressResult{addressesScanned: 1}

	changed, newHeight, txs, err := app.scanner.ScanAddress(ctx, addr)
	if err != nil {
		slog.Error("scan address failed",
			"chain", addr.Chain,
			"network", addr.Network,
			"address", addr.Address,
			"err", err,
		)
		return res
	}
	if changed && len(txs) > 0 {
		sort.Slice(txs, func(i, j int) bool {
			if txs[i].BlockHeight == txs[j].BlockHeight {
				return txs[i].Hash < txs[j].Hash
			}
			return txs[i].BlockHeight < txs[j].BlockHeight
		})

		res.detectedTxs += len(txs)
		allHandled := true

		for _, tx := range txs {
			processed, err := app.isProcessed(ctx, addr, tx.Hash)
			if err != nil {
				slog.Error("check processed tx failed", "tx_hash", tx.Hash, "err", err)
				allHandled = false
				break
			}

			if processed {
				res.duplicateTxs++
				if err := app.markMockDelivered(ctx, addr.Chain, tx.SourceID); err != nil {
					slog.Error("mark mock tx delivered failed", "err", err)
				}
				continue
			}

			callbackURL := addr.CallbackURL
			if callbackURL == "" {
				callbackURL = app.defaultCallbackURL
			}
			if callbackURL == "" {
				slog.Warn("skip tx: no callback URL configured", "tx_hash", tx.Hash, "address", addr.Address)
				res.failedCallbacks++
				allHandled = false
				break
			}

			created, err := app.enqueueCallbackTask(ctx, addr, callbackURL, tx)
			if err != nil {
				slog.Error("enqueue callback tx failed", "tx_hash", tx.Hash, "err", err)
				res.failedCallbacks++
				allHandled = false
				break
			}
			if created {
				res.queuedCallbacks++
			} else {
				res.duplicateTxs++
			}

			if err := app.markMockDelivered(ctx, addr.Chain, tx.SourceID); err != nil {
				slog.Error("mark mock tx delivered failed", "err", err)
				allHandled = false
				break
			}
		}

		if !allHandled {
			return res
		}
	}

	if newHeight > addr.LastSeenHeight {
		if err := app.db.WithContext(ctx).
			Model(&WatchedAddress{}).
			Where("id = ?", addr.ID).
			Update("last_seen_height", newHeight).Error; err != nil {
			slog.Error("update last_seen_height failed", "address", addr.Address, "err", err)
			return res
		}
		res.updatedAddresses++
	}

	return res
}

func (app *App) isProcessed(ctx context.Context, addr WatchedAddress, txHash string) (bool, error) {
	var count int64
	if err := app.db.WithContext(ctx).
		Model(&ProcessedTx{}).
		Where("chain = ? AND network = ? AND address = ? AND asset_type = ? AND token_contract = ? AND tx_hash = ?",
			addr.Chain, addr.Network, addr.Address, addr.AssetType, addr.TokenContract, txHash).
		Count(&count).Error; err != nil {
		return false, err
	}

	return count > 0, nil
}

func (app *App) markMockDelivered(ctx context.Context, chain string, sourceID uint64) error {
	if chain != "mock" || sourceID == 0 {
		return nil
	}

	return app.db.WithContext(ctx).
		Model(&MockIncomingTx{}).
		Where("id = ?", sourceID).
		Update("delivered", true).Error
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
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		next(w, r)
	}
}

func (app *App) handleAddresses(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		app.listAddresses(w, r)
	case http.MethodPost:
		app.createAddress(w, r)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (app *App) listAddresses(w http.ResponseWriter, r *http.Request) {
	query := app.db.WithContext(r.Context()).Order("id asc")
	if value := strings.TrimSpace(r.URL.Query().Get("chain")); value != "" {
		query = query.Where("chain = ?", strings.ToLower(value))
	}
	if value := strings.TrimSpace(r.URL.Query().Get("network")); value != "" {
		query = query.Where("network = ?", strings.ToLower(value))
	}
	if value := strings.TrimSpace(r.URL.Query().Get("asset_type")); value != "" {
		query = query.Where("asset_type = ?", strings.ToLower(value))
	}
	if value := strings.TrimSpace(r.URL.Query().Get("address")); value != "" {
		query = query.Where("address = ?", value)
	}
	if value := strings.TrimSpace(r.URL.Query().Get("enabled")); value != "" {
		switch strings.ToLower(value) {
		case "true", "1":
			query = query.Where("enabled = ?", true)
		case "false", "0":
			query = query.Where("enabled = ?", false)
		}
	}

	var addrs []WatchedAddress
	if err := query.Find(&addrs).Error; err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, addrs)
}

func (app *App) createAddress(w http.ResponseWriter, r *http.Request) {
	var req CreateAddressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	if req.Address == "" {
		http.Error(w, "address is required", http.StatusBadRequest)
		return
	}
	if req.Chain == "" {
		req.Chain = "tron"
	}
	if req.AssetType == "" {
		req.AssetType = "native"
	}
	if req.MinConfirmations <= 0 {
		req.MinConfirmations = 1
	}
	req.Chain = strings.ToLower(req.Chain)
	if req.Network == "" {
		if req.Chain == "mock" {
			req.Network = "local"
		} else {
			req.Network = "mainnet"
		}
	}
	req.Network = strings.ToLower(req.Network)
	req.AssetType = strings.ToLower(req.AssetType)
	req.TokenContract = strings.TrimSpace(req.TokenContract)

	switch req.AssetType {
	case "native", "trc20":
	default:
		http.Error(w, "unsupported asset_type", http.StatusBadRequest)
		return
	}

	if req.Chain == "tron" {
		normalizedAddress, err := normalizeTronAddress(req.Address)
		if err != nil {
			http.Error(w, "invalid tron address", http.StatusBadRequest)
			return
		}
		req.Address = normalizedAddress
		if req.TokenContract != "" {
			normalizedContract, err := normalizeTronAddress(req.TokenContract)
			if err != nil {
				http.Error(w, "invalid tron token_contract", http.StatusBadRequest)
				return
			}
			req.TokenContract = normalizedContract
		}
	}

	if req.Chain == "tron" && req.AssetType == "trc20" && req.TokenContract == "" {
		http.Error(w, "token_contract is required for tron trc20 watcher", http.StatusBadRequest)
		return
	}

	startHeight := uint64(0)
	if req.StartHeight != nil {
		startHeight = *req.StartHeight
	} else if req.Chain == "tron" {
		// Default behavior for production payment monitoring: start from current confirmed height,
		// so a newly registered address won't backfill the entire historical tx list.
		apiURL := resolveTronAPIURL(app.rpcURL, req.Network)
		client := tronclient.NewClient(apiURL).WithAPIKey(app.tronAPIKey)
		currentBlock, err := client.GetNowBlockNumber(r.Context())
		if err != nil {
			http.Error(w, "unable to resolve start_height from tron rpc (provide start_height explicitly or check rpc-url)", http.StatusBadGateway)
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
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, addr)
}

func (app *App) handleScanOnce(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	result, err := app.scanOnce(r.Context())
	if err != nil {
		http.Error(w, "scan failed", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, result)
}

func (app *App) handleMockTransactions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		app.listMockTransactions(w, r)
	case http.MethodPost:
		app.createMockTransaction(w, r)
	case http.MethodDelete:
		app.clearMockTransactions(w, r)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (app *App) listMockTransactions(w http.ResponseWriter, r *http.Request) {
	var rows []MockIncomingTx
	if err := app.db.WithContext(r.Context()).Order("block_height asc, id asc").Find(&rows).Error; err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, rows)
}

func (app *App) createMockTransaction(w http.ResponseWriter, r *http.Request) {
	var req CreateMockTxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	if req.Address == "" {
		http.Error(w, "address is required", http.StatusBadRequest)
		return
	}
	if req.Amount == "" {
		http.Error(w, "amount is required", http.StatusBadRequest)
		return
	}
	if req.Chain == "" {
		req.Chain = "mock"
	}
	if req.Network == "" {
		req.Network = "local"
	}
	req.Chain = strings.ToLower(req.Chain)
	req.Network = strings.ToLower(req.Network)
	if req.TxHash == "" {
		req.TxHash = time.Now().UTC().Format("20060102150405.000000000")
	}
	if req.From == "" {
		req.From = "mock_sender"
	}
	if req.To == "" {
		req.To = req.Address
	}
	if req.BlockHeight == 0 {
		var maxHeight uint64
		if err := app.db.WithContext(r.Context()).
			Model(&MockIncomingTx{}).
			Select("COALESCE(MAX(block_height), 0)").
			Scan(&maxHeight).Error; err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		req.BlockHeight = maxHeight + 1
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
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, row)
}

func (app *App) clearMockTransactions(w http.ResponseWriter, r *http.Request) {
	if err := app.db.WithContext(r.Context()).Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&MockIncomingTx{}).Error; err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (app *App) handleDebugCallbacks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		app.listReceivedCallbacks(w, r)
	case http.MethodPost:
		app.receiveDebugCallback(w, r)
	case http.MethodDelete:
		app.clearReceivedCallbacks(w, r)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (app *App) listReceivedCallbacks(w http.ResponseWriter, r *http.Request) {
	var rows []ReceivedCallback
	if err := app.db.WithContext(r.Context()).Order("id asc").Find(&rows).Error; err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, rows)
}

func (app *App) receiveDebugCallback(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body failed", http.StatusBadRequest)
		return
	}

	var payload CallbackPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	row := ReceivedCallback{
		Address:     payload.Address,
		TxHash:      payload.TxHash,
		BlockHeight: payload.BlockHeight,
		Payload:     string(body),
	}

	if err := app.db.WithContext(r.Context()).Create(&row).Error; err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (app *App) clearReceivedCallbacks(w http.ResponseWriter, r *http.Request) {
	if err := app.db.WithContext(r.Context()).Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&ReceivedCallback{}).Error; err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		http.Error(w, "encode error", http.StatusInternalServerError)
	}
}

func migrateLegacyIndexes(db *gorm.DB) error {
	statements := []string{
		"DROP INDEX IF EXISTS uniq_watched_address",
		"DROP INDEX IF EXISTS uniq_processed_tx",
	}

	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			return err
		}
	}

	return nil
}

func resolveTronAPIURL(rpcURL, network string) string {
	if strings.TrimSpace(rpcURL) != "" {
		return strings.TrimSpace(rpcURL)
	}

	switch strings.ToLower(strings.TrimSpace(network)) {
	case "shasta":
		return "https://api.shasta.trongrid.io"
	case "nile":
		return "https://nile.trongrid.io"
	default:
		return "https://api.trongrid.io"
	}
}

func normalizeTronAddress(address string) (string, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "", errors.New("empty tron address")
	}

	hexAddress, err := tronclient.AddressToHex(address)
	if err != nil {
		return "", err
	}

	return tronclient.HexToAddress(hexAddress)
}

func parseRetryStatusCodes(input string) map[int]bool {
	result := map[int]bool{}
	for _, raw := range strings.Split(input, ",") {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		code, err := strconv.Atoi(value)
		if err != nil || code <= 0 {
			continue
		}
		result[code] = true
	}
	return result
}
