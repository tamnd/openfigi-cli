// Package openfigi is the library behind the openfigi command line:
// the HTTP client, request shaping, and the typed data models for the
// OpenFIGI financial instrument identifier API.
//
// The Client paces requests so a busy session stays inside the unauthenticated
// rate limit (25 req/min), retries transient failures (429, 5xx), and exposes
// two methods that cover the two live endpoints: Map and Search.
package openfigi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// DefaultUserAgent identifies the client to OpenFIGI.
const DefaultUserAgent = "openfigi/dev (+https://github.com/tamnd/openfigi-cli)"

// Host is the site this client's domain driver claims.
const Host = "openfigi.com"

// Config holds tunable Client parameters.
type Config struct {
	BaseURL string
	Rate    time.Duration
	Retries int
	Timeout time.Duration
}

// DefaultConfig returns conservative defaults for unauthenticated access.
// The unauthenticated rate limit is 25 req/min, so we pace at 1s per request.
func DefaultConfig() Config {
	return Config{
		BaseURL: "https://api.openfigi.com",
		Rate:    1 * time.Second,
		Retries: 3,
		Timeout: 15 * time.Second,
	}
}

// Client talks to the OpenFIGI API over HTTP.
type Client struct {
	HTTP      *http.Client
	UserAgent string
	BaseURL   string
	// Rate is the minimum gap between requests. Zero means no pacing.
	Rate    time.Duration
	Retries int

	last time.Time
}

// NewClient returns a Client with the default config applied.
func NewClient() *Client {
	cfg := DefaultConfig()
	return &Client{
		HTTP:      &http.Client{Timeout: cfg.Timeout},
		UserAgent: DefaultUserAgent,
		BaseURL:   cfg.BaseURL,
		Rate:      cfg.Rate,
		Retries:   cfg.Retries,
	}
}

// Instrument is the output record for both Map and Search operations.
type Instrument struct {
	FIGI          string `kit:"id" json:"figi"`
	Name          string `json:"name"`
	Ticker        string `json:"ticker"`
	ExchCode      string `json:"exch_code"`
	SecurityType  string `json:"security_type"`
	MarketSector  string `json:"market_sector"`
	CompositeFIGI string `json:"composite_figi"`
}

// wire types match the OpenFIGI JSON response exactly (camelCase keys).
type wireInstrument struct {
	FIGI          string `json:"figi"`
	Name          string `json:"name"`
	Ticker        string `json:"ticker"`
	ExchCode      string `json:"exchCode"`
	SecurityType  string `json:"securityType"`
	MarketSector  string `json:"marketSector"`
	CompositeFIGI string `json:"compositeFIGI"`
}

func (w wireInstrument) toInstrument() Instrument {
	return Instrument{
		FIGI:          w.FIGI,
		Name:          w.Name,
		Ticker:        w.Ticker,
		ExchCode:      w.ExchCode,
		SecurityType:  w.SecurityType,
		MarketSector:  w.MarketSector,
		CompositeFIGI: w.CompositeFIGI,
	}
}

type mappingRequest struct {
	IDType   string `json:"idType"`
	IDValue  string `json:"idValue"`
	ExchCode string `json:"exchCode,omitempty"`
}

type searchRequest struct {
	Query    string `json:"query"`
	ExchCode string `json:"exchCode,omitempty"`
}

// Map maps an identifier to FIGI records using POST /v3/mapping.
func (c *Client) Map(ctx context.Context, idType, idValue, exchCode string) ([]Instrument, error) {
	req := []mappingRequest{{IDType: idType, IDValue: idValue, ExchCode: exchCode}}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	respBody, err := c.post(ctx, c.BaseURL+"/v3/mapping", body)
	if err != nil {
		return nil, err
	}

	var wireResp []struct {
		Data    []wireInstrument `json:"data"`
		Warning string           `json:"warning"`
	}
	if err := json.Unmarshal(respBody, &wireResp); err != nil {
		return nil, fmt.Errorf("map: decode response: %w", err)
	}

	var out []Instrument
	for _, entry := range wireResp {
		for _, w := range entry.Data {
			out = append(out, w.toInstrument())
		}
	}
	return out, nil
}

// Search searches for instruments using POST /v3/search.
// The limit is applied client-side after the API returns its page of results.
func (c *Client) Search(ctx context.Context, query, exchCode string, limit int) ([]Instrument, error) {
	req := searchRequest{Query: query, ExchCode: exchCode}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	respBody, err := c.post(ctx, c.BaseURL+"/v3/search", body)
	if err != nil {
		return nil, err
	}

	var wireResp struct {
		Data []wireInstrument `json:"data"`
	}
	if err := json.Unmarshal(respBody, &wireResp); err != nil {
		return nil, fmt.Errorf("search: decode response: %w", err)
	}

	data := wireResp.Data
	if limit > 0 && len(data) > limit {
		data = data[:limit]
	}

	out := make([]Instrument, 0, len(data))
	for _, w := range data {
		out = append(out, w.toInstrument())
	}
	return out, nil
}

// post sends a JSON POST request and returns the response body, with pacing and retries.
func (c *Client) post(ctx context.Context, url string, body []byte) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= c.Retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff(attempt)):
			}
		}
		resp, retry, err := c.doPost(ctx, url, body)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !retry {
			return nil, err
		}
	}
	return nil, fmt.Errorf("post %s: %w", url, lastErr)
}

func (c *Client) doPost(ctx context.Context, url string, body []byte) ([]byte, bool, error) {
	c.pace()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, true, fmt.Errorf("http %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, false, fmt.Errorf("http %d: %s", resp.StatusCode, string(b))
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, true, err
	}
	return b, false, nil
}

// pace blocks until at least Rate has passed since the previous request.
func (c *Client) pace() {
	if c.Rate <= 0 {
		return
	}
	if wait := c.Rate - time.Since(c.last); wait > 0 {
		time.Sleep(wait)
	}
	c.last = time.Now()
}

func backoff(attempt int) time.Duration {
	d := time.Duration(attempt) * 500 * time.Millisecond
	if d > 5*time.Second {
		d = 5 * time.Second
	}
	return d
}
