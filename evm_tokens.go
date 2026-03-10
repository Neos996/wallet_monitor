package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	evmclient "wallet_monitor/internal/evm"
)

type TokenMetadata struct {
	ID            uint64    `gorm:"primaryKey" json:"id"`
	Chain         string    `gorm:"uniqueIndex:uniq_token_metadata;size:32;not null" json:"chain"`
	Network       string    `gorm:"uniqueIndex:uniq_token_metadata;size:32;not null" json:"network"`
	TokenContract string    `gorm:"uniqueIndex:uniq_token_metadata;size:128;not null" json:"token_contract"`
	Decimals      int       `gorm:"not null;default:0" json:"decimals"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

const erc20DecimalsSelector = "0x313ce567" // decimals()

func (c *MultiClient) getERC20Decimals(ctx context.Context, chain, network, contract string) (int, error) {
	chain = strings.ToLower(strings.TrimSpace(chain))
	network = strings.ToLower(strings.TrimSpace(network))
	contract = strings.ToLower(strings.TrimSpace(contract))

	if chain == "" || network == "" || contract == "" {
		return 0, fmt.Errorf("invalid token metadata key (chain=%q network=%q contract=%q)", chain, network, contract)
	}

	{
		var row TokenMetadata
		err := c.db.WithContext(ctx).
			Where("chain = ? AND network = ? AND token_contract = ?", chain, network, contract).
			First(&row).Error
		if err == nil {
			return row.Decimals, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, err
		}
	}

	if c.evmRPCURL == "" {
		return 0, errors.New("evm rpc url not configured")
	}

	client := evmclient.NewClient(c.evmRPCURL)
	resultHex, err := client.EthCall(ctx, contract, erc20DecimalsSelector)
	if err != nil {
		return 0, err
	}

	trimmed := strings.TrimSpace(resultHex)
	trimmed = strings.TrimPrefix(trimmed, "0x")
	if trimmed == "" || !isHexString(trimmed) {
		return 0, fmt.Errorf("invalid erc20 decimals result: %q", resultHex)
	}

	value := parseHexBigInt(resultHex)
	if value.Sign() < 0 || value.BitLen() > 8 {
		return 0, fmt.Errorf("erc20 decimals out of range: %q", resultHex)
	}
	decimals := int(value.Int64())

	row := TokenMetadata{
		Chain:         chain,
		Network:       network,
		TokenContract: contract,
		Decimals:      decimals,
	}
	if err := c.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "chain"},
				{Name: "network"},
				{Name: "token_contract"},
			},
			DoUpdates: clause.AssignmentColumns([]string{"decimals", "updated_at"}),
		}).
		Create(&row).Error; err != nil {
		return decimals, err
	}

	return decimals, nil
}
