package main

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

func (app *App) scanOnce(ctx context.Context) (ScanResult, error) {
	startedAt := time.Now()
	result := ScanResult{ScannedAt: time.Now().UTC()}
	logger := loggerFromContext(ctx)

	var addresses []WatchedAddress
	if err := app.db.WithContext(ctx).Where("enabled = ?", true).Find(&addresses).Error; err != nil {
		return result, err
	}

	var evmAddresses []WatchedAddress
	otherAddresses := addresses
	if strings.ToLower(strings.TrimSpace(app.evmScanMode)) == "block" {
		otherAddresses = make([]WatchedAddress, 0, len(addresses))
		for _, addr := range addresses {
			if strings.ToLower(strings.TrimSpace(addr.Chain)) == "evm" && strings.ToLower(strings.TrimSpace(addr.AssetType)) == "erc20" {
				evmAddresses = append(evmAddresses, addr)
				continue
			}
			otherAddresses = append(otherAddresses, addr)
		}
	}

	if len(otherAddresses) > 0 {
		workers := normalizeWorkerCount(app.scanWorkers, len(otherAddresses))

		addrCh := make(chan WatchedAddress)
		resCh := make(chan addressResult, len(otherAddresses))

		var wg sync.WaitGroup
		for index := 0; index < workers; index++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for addr := range addrCh {
					resCh <- app.scanOneAddress(ctx, addr)
				}
			}()
		}

		go func() {
			for _, addr := range otherAddresses {
				addrCh <- addr
			}
			close(addrCh)
			wg.Wait()
			close(resCh)
		}()

		for res := range resCh {
			result.mergeAddressResult(res)
		}
	}

	if len(evmAddresses) > 0 {
		result.mergeAddressResult(app.scanEVMByBlockLogs(ctx, evmAddresses))
	}

	batch := app.callbackBatch
	if batch <= 0 {
		batch = 100
	}
	taskResult, err := app.processDueCallbackTasks(ctx, batch)
	if err != nil {
		if errors.Is(err, errCallbackDispatchBusy) {
			logger.Warn("callback dispatch skipped: already running")
			recordScanMetrics(result, startedAt)
			app.updateMetrics(ctx)
			return result, nil
		}
		return result, err
	}
	result.mergeCallbackTaskResult(taskResult)

	recordScanMetrics(result, startedAt)
	app.updateMetrics(ctx)

	return result, nil
}

func (app *App) scanOneAddress(ctx context.Context, addr WatchedAddress) addressResult {
	res := addressResult{addressesScanned: 1}
	logger := loggerFromContext(ctx)

	changed, newHeight, txs, err := app.scanner.ScanAddress(ctx, addr)
	if err != nil {
		metrics.scanAddressFailuresTotal.Add(1)
		logger.Error("scan address failed",
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
				if txs[i].Hash == txs[j].Hash {
					return txs[i].LogIndex < txs[j].LogIndex
				}
				return txs[i].Hash < txs[j].Hash
			}
			return txs[i].BlockHeight < txs[j].BlockHeight
		})

		res.detectedTxs += len(txs)
		allHandled := true

		for _, tx := range txs {
			outcome, err := app.enqueueDetectedTx(ctx, addr, tx)
			if err != nil {
				res.failedCallbacks++
				allHandled = false
				break
			}
			res.applyQueueOutcome(outcome)
			if outcome == detectedTxMissingCallback {
				allHandled = false
				break
			}

			if err := app.markMockDelivered(ctx, addr.Chain, tx.SourceID); err != nil {
				logger.Error("mark mock tx delivered failed", "source_id", tx.SourceID, "tx_hash", tx.Hash, "err", err)
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
			logger.Error("update last_seen_height failed", "address", addr.Address, "new_height", newHeight, "err", err)
			return res
		}
		res.updatedAddresses++
	}

	return res
}

func (app *App) isProcessed(ctx context.Context, addr WatchedAddress, txHash string, logIndex uint64) (bool, error) {
	var count int64
	if err := app.db.WithContext(ctx).
		Model(&ProcessedTx{}).
		Where("chain = ? AND network = ? AND address = ? AND asset_type = ? AND token_contract = ? AND tx_hash = ? AND log_index = ?",
			addr.Chain, addr.Network, addr.Address, addr.AssetType, addr.TokenContract, txHash, logIndex).
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
