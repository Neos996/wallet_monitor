package tron

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	apiURL     string
	apiKey     string
	httpClient *http.Client
	limiter    *RateLimiter
	retry429   int
	retryBase  time.Duration
}

type IncomingTRXTransaction struct {
	TxID           string
	From           string
	To             string
	Amount         string
	AmountSun      int64
	BlockNumber    uint64
	BlockTimestamp int64
}

type IncomingTRC20Transaction struct {
	TxID           string
	From           string
	To             string
	Amount         string
	RawValue       string
	BlockNumber    uint64
	BlockTimestamp int64
	TokenAddress   string
	TokenSymbol    string
	TokenName      string
	TokenDecimals  int
}

func NewClient(apiURL string) *Client {
	return &Client{
		apiURL:     strings.TrimRight(apiURL, "/"),
		httpClient: defaultHTTPClient(),
		retry429:   3,
		retryBase:  500 * time.Millisecond,
	}
}

func (c *Client) WithRateLimiter(limiter *RateLimiter) *Client {
	c.limiter = limiter
	return c
}

func (c *Client) WithRetry429(attempts int, base time.Duration) *Client {
	if attempts > 0 {
		c.retry429 = attempts
	}
	if base > 0 {
		c.retryBase = base
	}
	return c
}

// GetIncomingTRXTransactions queries confirmed incoming TRX transfers since minBlockNumber (exclusive).
func (c *Client) GetIncomingTRXTransactions(ctx context.Context, address string, minBlockNumber uint64, pageSize int) ([]IncomingTRXTransaction, uint64, error) {
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 200
	}

	targetHex, err := AddressToHex(address)
	if err != nil {
		return nil, minBlockNumber, fmt.Errorf("invalid address: %v", err)
	}
	targetHex = strings.ToLower(targetHex)

	baseURL := fmt.Sprintf("%s/v1/accounts/%s/transactions", c.apiURL, address)

	type txResponse struct {
		TxID           string `json:"txID"`
		BlockNumber    uint64 `json:"blockNumber"`
		BlockTimestamp int64  `json:"block_timestamp"`
		Ret            []struct {
			ContractRet string `json:"contractRet"`
		} `json:"ret"`
		RawData struct {
			Contract []struct {
				Type      string `json:"type"`
				Parameter struct {
					Value struct {
						Amount       int64  `json:"amount"`
						OwnerAddress string `json:"owner_address"`
						ToAddress    string `json:"to_address"`
					} `json:"value"`
				} `json:"parameter"`
			} `json:"contract"`
		} `json:"raw_data"`
	}

	type apiResponse struct {
		Data []txResponse `json:"data"`
		Meta struct {
			Fingerprint string `json:"fingerprint"`
		} `json:"meta"`
	}

	var (
		result         []IncomingTRXTransaction
		fingerprint    string
		latestObserved = minBlockNumber
	)

	for {
		reqURL, err := url.Parse(baseURL)
		if err != nil {
			return nil, latestObserved, err
		}

		query := reqURL.Query()
		query.Set("limit", strconv.Itoa(pageSize))
		query.Set("only_to", "true")
		query.Set("only_confirmed", "true")
		query.Set("order_by", "block_timestamp,desc")
		if fingerprint != "" {
			query.Set("fingerprint", fingerprint)
		}
		reqURL.RawQuery = query.Encode()

		body, status, err := c.doRequest(ctx, http.MethodGet, reqURL.String(), nil, "")
		if err != nil {
			return nil, latestObserved, err
		}
		if status < 200 || status >= 300 {
			return nil, latestObserved, fmt.Errorf("query incoming trx failed: status=%d body=%s", status, strings.TrimSpace(string(body)))
		}

		var resultPage apiResponse
		if err := json.Unmarshal(body, &resultPage); err != nil {
			return nil, latestObserved, err
		}

		if len(resultPage.Data) == 0 {
			break
		}

		reachedOldBlock := false
		reachedLimit := len(resultPage.Data) >= pageSize

		for _, item := range resultPage.Data {
			if item.BlockNumber > latestObserved {
				latestObserved = item.BlockNumber
			}

			if item.BlockNumber <= minBlockNumber {
				reachedOldBlock = true
				continue
			}

			if len(item.Ret) > 0 && item.Ret[0].ContractRet != "" && item.Ret[0].ContractRet != "SUCCESS" {
				continue
			}

			if len(item.RawData.Contract) == 0 {
				continue
			}

			contract := item.RawData.Contract[0]
			if contract.Type != "TransferContract" {
				continue
			}

			value := contract.Parameter.Value
			if strings.ToLower(value.ToAddress) != targetHex {
				continue
			}

			fromAddress := value.OwnerAddress
			if decoded, err := HexToAddress(value.OwnerAddress); err == nil {
				fromAddress = decoded
			}

			toAddress := address
			if decoded, err := HexToAddress(value.ToAddress); err == nil {
				toAddress = decoded
			}

			result = append(result, IncomingTRXTransaction{
				TxID:           item.TxID,
				From:           fromAddress,
				To:             toAddress,
				Amount:         formatSunAmount(value.Amount, 6),
				AmountSun:      value.Amount,
				BlockNumber:    item.BlockNumber,
				BlockTimestamp: item.BlockTimestamp,
			})
		}

		if reachedOldBlock || !reachedLimit || resultPage.Meta.Fingerprint == "" {
			break
		}

		fingerprint = resultPage.Meta.Fingerprint
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].BlockNumber == result[j].BlockNumber {
			return result[i].BlockTimestamp < result[j].BlockTimestamp
		}
		return result[i].BlockNumber < result[j].BlockNumber
	})

	return result, latestObserved, nil
}

// GetIncomingTRC20Transactions queries confirmed incoming TRC20 transfers since minBlockNumber (exclusive).
func (c *Client) GetIncomingTRC20Transactions(ctx context.Context, address, contractAddress string, minBlockNumber uint64, pageSize int) ([]IncomingTRC20Transaction, uint64, error) {
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 200
	}

	baseURL := fmt.Sprintf("%s/v1/accounts/%s/transactions/trc20", c.apiURL, address)

	type txResponse struct {
		TransactionID  string `json:"transaction_id"`
		BlockTimestamp int64  `json:"block_timestamp"`
		From           string `json:"from"`
		To             string `json:"to"`
		Type           string `json:"type"`
		Value          string `json:"value"`
		TokenInfo      struct {
			Symbol   string `json:"symbol"`
			Address  string `json:"address"`
			Decimals int    `json:"decimals"`
			Name     string `json:"name"`
		} `json:"token_info"`
	}

	type apiResponse struct {
		Data []txResponse `json:"data"`
		Meta struct {
			Fingerprint string `json:"fingerprint"`
		} `json:"meta"`
	}

	requestContract := strings.TrimSpace(contractAddress)
	filterContract := requestContract
	if filterContract != "" {
		filterContract = canonicalTronAddress(filterContract)
	}

	var (
		result         []IncomingTRC20Transaction
		fingerprint    string
		latestObserved = minBlockNumber
	)

	for {
		reqURL, err := url.Parse(baseURL)
		if err != nil {
			return nil, latestObserved, err
		}

		query := reqURL.Query()
		query.Set("limit", strconv.Itoa(pageSize))
		query.Set("only_to", "true")
		query.Set("only_confirmed", "true")
		query.Set("order_by", "block_timestamp,desc")
		if fingerprint != "" {
			query.Set("fingerprint", fingerprint)
		}
		if requestContract != "" {
			query.Set("contract_address", requestContract)
		}
		reqURL.RawQuery = query.Encode()

		body, status, err := c.doRequest(ctx, http.MethodGet, reqURL.String(), nil, "")
		if err != nil {
			return nil, latestObserved, err
		}
		if status < 200 || status >= 300 {
			return nil, latestObserved, fmt.Errorf("query incoming trc20 failed: status=%d body=%s", status, strings.TrimSpace(string(body)))
		}

		var resultPage apiResponse
		if err := json.Unmarshal(body, &resultPage); err != nil {
			return nil, latestObserved, err
		}

		if len(resultPage.Data) == 0 {
			break
		}

		reachedOldBlock := false
		reachedLimit := len(resultPage.Data) >= pageSize

		for _, item := range resultPage.Data {
			if item.Type != "Transfer" {
				continue
			}

			txBlock, txTimestamp, err := c.GetTransactionInfoByID(ctx, item.TransactionID)
			if err != nil {
				return nil, latestObserved, err
			}

			if txBlock > latestObserved {
				latestObserved = txBlock
			}

			if txBlock <= minBlockNumber {
				reachedOldBlock = true
				continue
			}

			tokenAddress := canonicalTronAddress(item.TokenInfo.Address)
			if filterContract != "" && tokenAddress != filterContract {
				continue
			}

			result = append(result, IncomingTRC20Transaction{
				TxID:           item.TransactionID,
				From:           item.From,
				To:             item.To,
				Amount:         formatTokenAmount(item.Value, item.TokenInfo.Decimals),
				RawValue:       item.Value,
				BlockNumber:    txBlock,
				BlockTimestamp: txTimestamp,
				TokenAddress:   item.TokenInfo.Address,
				TokenSymbol:    item.TokenInfo.Symbol,
				TokenName:      item.TokenInfo.Name,
				TokenDecimals:  item.TokenInfo.Decimals,
			})

			time.Sleep(400 * time.Millisecond)
		}

		if reachedOldBlock || !reachedLimit || resultPage.Meta.Fingerprint == "" {
			break
		}

		fingerprint = resultPage.Meta.Fingerprint
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].BlockNumber == result[j].BlockNumber {
			return result[i].BlockTimestamp < result[j].BlockTimestamp
		}
		return result[i].BlockNumber < result[j].BlockNumber
	})

	return result, latestObserved, nil
}

func (c *Client) GetTransactionInfoByID(ctx context.Context, txID string) (uint64, int64, error) {
	reqBody, err := json.Marshal(map[string]string{"value": txID})
	if err != nil {
		return 0, 0, err
	}

	for attempt := 0; attempt < 3; attempt++ {
		body, status, err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("%s/wallet/gettransactioninfobyid", c.apiURL), reqBody, "application/json")
		if err != nil {
			return 0, 0, err
		}

		if status == http.StatusTooManyRequests && attempt < 2 {
			time.Sleep(5 * time.Second)
			continue
		}
		if status < 200 || status >= 300 {
			return 0, 0, fmt.Errorf("query tx detail failed: status=%d body=%s", status, strings.TrimSpace(string(body)))
		}

		var result struct {
			ID             string `json:"id"`
			BlockNumber    uint64 `json:"blockNumber"`
			BlockTimeStamp int64  `json:"blockTimeStamp"`
			Receipt        struct {
				Result string `json:"result"`
			} `json:"receipt"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return 0, 0, err
		}
		if result.ID == "" {
			return 0, 0, fmt.Errorf("tx not found: %s", txID)
		}
		if result.Receipt.Result != "" && result.Receipt.Result != "SUCCESS" {
			return 0, 0, fmt.Errorf("tx execution failed: txid=%s result=%s", txID, result.Receipt.Result)
		}

		return result.BlockNumber, result.BlockTimeStamp, nil
	}

	return 0, 0, fmt.Errorf("query tx detail failed: txid=%s", txID)
}

func canonicalTronAddress(address string) string {
	if hexAddress, err := AddressToHex(address); err == nil {
		return strings.ToLower(hexAddress)
	}
	return strings.TrimSpace(address)
}

func formatSunAmount(amount int64, decimals int) string {
	negative := amount < 0
	if negative {
		amount = -amount
	}

	value := strconv.FormatInt(amount, 10)
	if decimals <= 0 {
		if negative {
			return "-" + value
		}
		return value
	}

	if len(value) <= decimals {
		value = strings.Repeat("0", decimals-len(value)+1) + value
	}

	intPart := value[:len(value)-decimals]
	fracPart := strings.TrimRight(value[len(value)-decimals:], "0")

	result := intPart
	if fracPart != "" {
		result += "." + fracPart
	}

	if negative {
		return "-" + result
	}
	return result
}

func formatTokenAmount(rawValue string, decimals int) string {
	rawValue = strings.TrimSpace(rawValue)
	if rawValue == "" {
		return "0"
	}

	negative := strings.HasPrefix(rawValue, "-")
	if negative {
		rawValue = strings.TrimPrefix(rawValue, "-")
	}

	rawValue = strings.TrimLeft(rawValue, "0")
	if rawValue == "" {
		rawValue = "0"
	}

	if decimals <= 0 {
		if negative {
			return "-" + rawValue
		}
		return rawValue
	}

	if len(rawValue) <= decimals {
		rawValue = strings.Repeat("0", decimals-len(rawValue)+1) + rawValue
	}

	intPart := rawValue[:len(rawValue)-decimals]
	fracPart := strings.TrimRight(rawValue[len(rawValue)-decimals:], "0")
	if fracPart == "" {
		if negative {
			return "-" + intPart
		}
		return intPart
	}

	if negative {
		return "-" + intPart + "." + fracPart
	}
	return intPart + "." + fracPart
}
