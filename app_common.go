package main

import (
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strconv"
	"strings"

	"gorm.io/gorm"
	tronclient "wallet_monitor/internal/tron"
)

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
		"DROP INDEX IF EXISTS uniq_processed_delivery",
		"DROP INDEX IF EXISTS uniq_callback_task",
	}

	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			return err
		}
	}

	if err := db.Migrator().CreateIndex(&ProcessedTx{}, "uniq_processed_delivery"); err != nil {
		return err
	}
	if err := db.Migrator().CreateIndex(&CallbackTask{}, "uniq_callback_task"); err != nil {
		return err
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

func normalizeEVMAddress(address string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(address))
	if value == "" {
		return "", errors.New("empty evm address")
	}
	if !strings.HasPrefix(value, "0x") {
		value = "0x" + value
	}
	if len(value) != 42 {
		return "", errors.New("invalid evm address length")
	}
	if !isHexString(value[2:]) {
		return "", errors.New("invalid evm address hex")
	}
	return value, nil
}

func padTopicAddress(address string) string {
	value := strings.ToLower(strings.TrimSpace(address))
	value = strings.TrimPrefix(value, "0x")
	if len(value) > 40 {
		value = value[len(value)-40:]
	}
	return "0x" + strings.Repeat("0", 64-len(value)) + value
}

func topicToAddress(topic string) string {
	value := strings.ToLower(strings.TrimSpace(topic))
	value = strings.TrimPrefix(value, "0x")
	if len(value) > 40 {
		value = value[len(value)-40:]
	}
	return "0x" + value
}

func parseHexUint64(value string) (uint64, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "0x")
	if value == "" {
		return 0, nil
	}
	return strconv.ParseUint(value, 16, 64)
}

func parseHexBigInt(value string) *big.Int {
	text := strings.TrimSpace(value)
	text = strings.TrimPrefix(text, "0x")
	if text == "" {
		return big.NewInt(0)
	}
	result := new(big.Int)
	if _, ok := result.SetString(text, 16); !ok {
		return big.NewInt(0)
	}
	return result
}

func isHexString(input string) bool {
	if input == "" {
		return false
	}
	for _, r := range input {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
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
