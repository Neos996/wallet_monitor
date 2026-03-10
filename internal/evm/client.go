package evm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	rpcURL     string
	httpClient *http.Client
}

func NewClient(rpcURL string) *Client {
	return &Client{
		rpcURL:     strings.TrimSpace(rpcURL),
		httpClient: &http.Client{Timeout: 20 * time.Second},
	}
}

type rpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data"`
}

func (c *Client) call(ctx context.Context, method string, params interface{}, out interface{}) error {
	if c.rpcURL == "" {
		return fmt.Errorf("evm rpc url is empty")
	}

	reqBody, err := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.rpcURL, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("rpc status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var rpcResp rpcResponse
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return err
	}
	if rpcResp.Error != nil {
		return fmt.Errorf("rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(rpcResp.Result, out)
}

func (c *Client) GetBlockNumber(ctx context.Context) (string, error) {
	var result string
	if err := c.call(ctx, "eth_blockNumber", []any{}, &result); err != nil {
		return "", err
	}
	return result, nil
}

type Log struct {
	Address         string   `json:"address"`
	Topics          []string `json:"topics"`
	Data            string   `json:"data"`
	TransactionHash string   `json:"transactionHash"`
	LogIndex        string   `json:"logIndex"`
	BlockNumber     string   `json:"blockNumber"`
}

func (c *Client) GetLogs(ctx context.Context, filter map[string]any) ([]Log, error) {
	var result []Log
	if err := c.call(ctx, "eth_getLogs", []any{filter}, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) EthCall(ctx context.Context, to string, data string) (string, error) {
	var result string
	params := []any{
		map[string]any{
			"to":   to,
			"data": data,
		},
		"latest",
	}
	if err := c.call(ctx, "eth_call", params, &result); err != nil {
		return "", err
	}
	return result, nil
}
