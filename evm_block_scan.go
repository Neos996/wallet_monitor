package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	evmclient "wallet_monitor/internal/evm"
)

type evmLogCrawlKey struct {
	network          string
	contract         string
	minConfirmations int
}

type evmMatchedEvent struct {
	addr WatchedAddress
	tx   Tx
}

func (app *App) scanEVMByBlockLogs(ctx context.Context, addrs []WatchedAddress) addressResult {
	logger := loggerFromContext(ctx)
	// Best-effort optimization; if anything looks off, fall back to the existing per-address scan path.
	mc, ok := app.scanner.(*MultiClient)
	if !ok {
		logger.Warn("evm block scan requires MultiClient scanner; falling back to per-address scan")
		return app.scanAddressesIndividually(ctx, addrs)
	}
	if strings.TrimSpace(mc.evmRPCURL) == "" {
		logger.Warn("evm rpc url is empty; falling back to per-address scan")
		return app.scanAddressesIndividually(ctx, addrs)
	}

	client := evmclient.NewClient(mc.evmRPCURL)
	blockHex, err := client.GetBlockNumber(ctx)
	if err != nil {
		logger.Warn("get evm block number failed; falling back to per-address scan", "err", err)
		return app.scanAddressesIndividually(ctx, addrs)
	}
	currentBlock, err := parseHexUint64(blockHex)
	if err != nil {
		logger.Warn("parse evm block number failed; falling back to per-address scan", "value", blockHex, "err", err)
		return app.scanAddressesIndividually(ctx, addrs)
	}

	buckets := map[evmLogCrawlKey][]WatchedAddress{}
	for _, addr := range addrs {
		if strings.ToLower(strings.TrimSpace(addr.Chain)) != "evm" {
			continue
		}
		if strings.ToLower(strings.TrimSpace(addr.AssetType)) != "erc20" {
			continue
		}
		contract := strings.ToLower(strings.TrimSpace(addr.TokenContract))
		if contract == "" {
			logger.Warn("skip evm watcher: empty token_contract", "address_id", addr.ID, "address", addr.Address)
			continue
		}
		network := strings.ToLower(strings.TrimSpace(addr.Network))
		if network == "" {
			network = "mainnet"
		}

		key := evmLogCrawlKey{
			network:          network,
			contract:         contract,
			minConfirmations: addr.MinConfirmations,
		}
		buckets[key] = append(buckets[key], addr)
	}

	tasks := make([]evmLogCrawlKey, 0, len(buckets))
	for key := range buckets {
		tasks = append(tasks, key)
	}
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].network == tasks[j].network {
			if tasks[i].contract == tasks[j].contract {
				return tasks[i].minConfirmations < tasks[j].minConfirmations
			}
			return tasks[i].contract < tasks[j].contract
		}
		return tasks[i].network < tasks[j].network
	})

	workers := normalizeWorkerCount(app.scanWorkers, len(tasks))

	taskCh := make(chan evmLogCrawlKey)
	resCh := make(chan addressResult, len(tasks))

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for key := range taskCh {
				resCh <- app.scanEVMLogBucket(ctx, mc, client, key, buckets[key], currentBlock)
			}
		}()
	}

	go func() {
		for _, key := range tasks {
			taskCh <- key
		}
		close(taskCh)
		wg.Wait()
		close(resCh)
	}()

	res := addressResult{}
	for bucketRes := range resCh {
		res.merge(bucketRes)
	}

	return res
}

func (app *App) scanAddressesIndividually(ctx context.Context, addrs []WatchedAddress) addressResult {
	res := addressResult{}
	for _, addr := range addrs {
		res.merge(app.scanOneAddress(ctx, addr))
	}
	return res
}

func (app *App) scanEVMLogBucket(
	ctx context.Context,
	mc *MultiClient,
	client *evmclient.Client,
	key evmLogCrawlKey,
	addrs []WatchedAddress,
	currentBlock uint64,
) addressResult {
	res := addressResult{addressesScanned: len(addrs)}
	logger := loggerFromContext(ctx)

	if len(addrs) == 0 {
		return res
	}

	cutoff := calculateConfirmedCutoff(currentBlock, key.minConfirmations)

	active := make([]WatchedAddress, 0, len(addrs))
	for _, addr := range addrs {
		if cutoff > addr.LastSeenHeight {
			active = append(active, addr)
		}
	}
	if len(active) == 0 {
		return res
	}

	from := active[0].LastSeenHeight + 1
	for _, addr := range active[1:] {
		if addr.LastSeenHeight+1 < from {
			from = addr.LastSeenHeight + 1
		}
	}
	to := cutoff

	step := mc.evmLogRange
	if step == 0 {
		step = 2000
	}

	toBatchSize := app.evmTopicBatch
	if toBatchSize <= 0 {
		toBatchSize = 100
	}

	watchByTo := make(map[string]WatchedAddress, len(active))
	toTopics := make([]string, 0, len(active))
	for _, addr := range active {
		normalized := strings.ToLower(strings.TrimSpace(addr.Address))
		watchByTo[normalized] = addr
		toTopics = append(toTopics, padTopicAddress(normalized))
	}
	toBatches := chunkStrings(toTopics, toBatchSize)

	events := make([]evmMatchedEvent, 0)
	for start := from; start <= to; start += step {
		end := start + step - 1
		if end > to {
			end = to
		}

		for _, batchTopics := range toBatches {
			filter := map[string]any{
				"fromBlock": fmt.Sprintf("0x%x", start),
				"toBlock":   fmt.Sprintf("0x%x", end),
				"address":   key.contract,
				"topics":    []any{evmTransferTopic, nil, batchTopics},
			}

			logs, err := client.GetLogs(ctx, filter)
			if err != nil {
				metrics.scanAddressFailuresTotal.Add(uint64(len(addrs)))
				logger.Error("evm getLogs failed", "network", key.network, "contract", key.contract, "err", err)
				return res
			}

			for _, logRow := range logs {
				if len(logRow.Topics) < 3 {
					continue
				}

				blockNumber, err := parseHexUint64(logRow.BlockNumber)
				if err != nil {
					continue
				}
				logIndex, err := parseHexUint64(logRow.LogIndex)
				if err != nil {
					continue
				}

				toAddr := topicToAddress(logRow.Topics[2])
				watched, ok := watchByTo[toAddr]
				if !ok {
					continue
				}
				if blockNumber <= watched.LastSeenHeight {
					continue
				}

				amount := parseHexBigInt(logRow.Data)
				events = append(events, evmMatchedEvent{
					addr: watched,
					tx: Tx{
						Hash:          logRow.TransactionHash,
						LogIndex:      logIndex,
						From:          topicToAddress(logRow.Topics[1]),
						To:            toAddr,
						Amount:        amount.String(),
						BlockHeight:   blockNumber,
						AssetType:     "erc20",
						TokenContract: key.contract,
					},
				})
			}
		}
	}

	decimals := 0
	if len(events) > 0 {
		value, err := mc.getERC20Decimals(ctx, "evm", key.network, key.contract)
		if err != nil {
			logger.Warn("resolve erc20 decimals failed", "network", key.network, "token_contract", key.contract, "err", err)
		} else {
			decimals = value
		}
	}

	sort.Slice(events, func(i, j int) bool {
		if events[i].tx.BlockHeight == events[j].tx.BlockHeight {
			if events[i].tx.Hash == events[j].tx.Hash {
				if events[i].tx.LogIndex == events[j].tx.LogIndex {
					return events[i].addr.Address < events[j].addr.Address
				}
				return events[i].tx.LogIndex < events[j].tx.LogIndex
			}
			return events[i].tx.Hash < events[j].tx.Hash
		}
		return events[i].tx.BlockHeight < events[j].tx.BlockHeight
	})

	missingCallback := map[uint64]bool{}
	for i := range events {
		tx := events[i].tx
		tx.TokenDecimals = decimals

		watched := events[i].addr
		res.detectedTxs++

		outcome, err := app.enqueueDetectedTx(ctx, watched, tx)
		if err != nil {
			res.failedCallbacks++
			return res
		}
		res.applyQueueOutcome(outcome)
		if outcome == detectedTxMissingCallback {
			missingCallback[watched.ID] = true
		}
	}

	// Advance cursors only when the entire bucket scan and enqueue succeeded.
	updateIDs := make([]uint64, 0, len(active))
	for _, addr := range active {
		if missingCallback[addr.ID] {
			continue
		}
		updateIDs = append(updateIDs, addr.ID)
	}
	if len(updateIDs) > 0 {
		if err := app.db.WithContext(ctx).
			Model(&WatchedAddress{}).
			Where("id IN ?", updateIDs).
			Update("last_seen_height", cutoff).Error; err != nil {
			logger.Error("update last_seen_height failed", "network", key.network, "contract", key.contract, "cutoff", cutoff, "err", err)
			return res
		}
		res.updatedAddresses += len(updateIDs)
	}

	return res
}

func chunkStrings(input []string, size int) [][]string {
	if size <= 0 {
		size = 1
	}
	if len(input) == 0 {
		return nil
	}
	out := make([][]string, 0, (len(input)+size-1)/size)
	for i := 0; i < len(input); i += size {
		end := i + size
		if end > len(input) {
			end = len(input)
		}
		out = append(out, input[i:end])
	}
	return out
}
