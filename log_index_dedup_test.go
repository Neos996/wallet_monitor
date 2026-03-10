package main

import (
	"context"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestLogIndex_UniqCallbackTaskAndProcessedTx(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db failed: %v", err)
	}
	if err := db.AutoMigrate(&WatchedAddress{}, &TokenMetadata{}, &ProcessedTx{}, &MockIncomingTx{}, &ReceivedCallback{}, &CallbackTask{}); err != nil {
		t.Fatalf("auto migrate failed: %v", err)
	}
	if err := migrateLegacyIndexes(db); err != nil {
		t.Fatalf("migrate legacy indexes failed: %v", err)
	}

	app := &App{db: db, maxCallbackRetries: 5}
	addr := WatchedAddress{
		Chain:         "evm",
		Network:       "mainnet",
		Address:       "0x00000000000000000000000000000000000000aa",
		AssetType:     "erc20",
		TokenContract: "0x00000000000000000000000000000000000000bb",
	}

	ctx := context.Background()

	// Same tx_hash, different log_index => should be treated as different events.
	created, err := app.enqueueCallbackTask(ctx, addr, "http://example/cb", Tx{
		Hash:          "0xdeadbeef",
		LogIndex:      1,
		From:          "0xfrom",
		To:            addr.Address,
		Amount:        "1",
		BlockHeight:   123,
		TokenContract: addr.TokenContract,
	})
	if err != nil {
		t.Fatalf("enqueue callback task failed: %v", err)
	}
	if !created {
		t.Fatalf("expected created=true for first event")
	}

	created, err = app.enqueueCallbackTask(ctx, addr, "http://example/cb", Tx{
		Hash:          "0xdeadbeef",
		LogIndex:      2,
		From:          "0xfrom",
		To:            addr.Address,
		Amount:        "1",
		BlockHeight:   123,
		TokenContract: addr.TokenContract,
	})
	if err != nil {
		t.Fatalf("enqueue callback task failed: %v", err)
	}
	if !created {
		t.Fatalf("expected created=true for second event with different log_index")
	}

	created, err = app.enqueueCallbackTask(ctx, addr, "http://example/cb", Tx{
		Hash:          "0xdeadbeef",
		LogIndex:      2,
		From:          "0xfrom",
		To:            addr.Address,
		Amount:        "1",
		BlockHeight:   123,
		TokenContract: addr.TokenContract,
	})
	if err != nil {
		t.Fatalf("enqueue callback task failed: %v", err)
	}
	if created {
		t.Fatalf("expected created=false for duplicate event (same tx_hash + log_index)")
	}

	// Same tx_hash, different log_index => processed should be tracked per event.
	if err := db.WithContext(ctx).Create(&ProcessedTx{
		Chain:         addr.Chain,
		Network:       addr.Network,
		Address:       addr.Address,
		AssetType:     addr.AssetType,
		TokenContract: addr.TokenContract,
		TxHash:        "0xdeadbeef",
		LogIndex:      1,
		BlockHeight:   123,
	}).Error; err != nil {
		t.Fatalf("insert processed tx failed: %v", err)
	}

	got, err := app.isProcessed(ctx, addr, "0xdeadbeef", 1)
	if err != nil {
		t.Fatalf("isProcessed failed: %v", err)
	}
	if !got {
		t.Fatalf("expected processed=true for (tx_hash, log_index) that was inserted")
	}

	got, err = app.isProcessed(ctx, addr, "0xdeadbeef", 2)
	if err != nil {
		t.Fatalf("isProcessed failed: %v", err)
	}
	if got {
		t.Fatalf("expected processed=false for different log_index")
	}
}
