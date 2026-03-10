package tron

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

func (c *Client) WithAPIKey(apiKey string) *Client {
	c.apiKey = strings.TrimSpace(apiKey)
	return c
}

func (c *Client) doRequest(ctx context.Context, method, requestURL string, body []byte, contentType string) ([]byte, int, error) {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, requestURL, reader)
	if err != nil {
		return nil, 0, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.apiKey != "" {
		req.Header.Set("TRON-PRO-API-KEY", c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}
	return responseBody, resp.StatusCode, nil
}

func (c *Client) GetNowBlockNumber(ctx context.Context) (uint64, error) {
	body, status, err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("%s/wallet/getnowblock", c.apiURL), []byte(`{}`), "application/json")
	if err != nil {
		return 0, err
	}
	if status < 200 || status >= 300 {
		return 0, fmt.Errorf("get now block failed: status=%d body=%s", status, strings.TrimSpace(string(body)))
	}

	var result struct {
		BlockHeader struct {
			RawData struct {
				Number uint64 `json:"number"`
			} `json:"raw_data"`
		} `json:"block_header"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, err
	}
	return result.BlockHeader.RawData.Number, nil
}

func defaultHTTPClient() *http.Client {
	return &http.Client{Timeout: 20 * time.Second}
}
