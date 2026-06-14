package openfigi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tamnd/openfigi-cli/openfigi"
)

// mapResponse builds a /v3/mapping response with one entry.
func mapResponse(instruments []openfigi.Instrument) []byte {
	type wireInst struct {
		FIGI          string `json:"figi"`
		Name          string `json:"name"`
		Ticker        string `json:"ticker"`
		ExchCode      string `json:"exchCode"`
		SecurityType  string `json:"securityType"`
		MarketSector  string `json:"marketSector"`
		CompositeFIGI string `json:"compositeFIGI"`
	}
	type entry struct {
		Data []wireInst `json:"data"`
	}
	var wires []wireInst
	for _, inst := range instruments {
		wires = append(wires, wireInst{
			FIGI:          inst.FIGI,
			Name:          inst.Name,
			Ticker:        inst.Ticker,
			ExchCode:      inst.ExchCode,
			SecurityType:  inst.SecurityType,
			MarketSector:  inst.MarketSector,
			CompositeFIGI: inst.CompositeFIGI,
		})
	}
	b, _ := json.Marshal([]entry{{Data: wires}})
	return b
}

// searchResponse builds a /v3/search response.
func searchResponse(instruments []openfigi.Instrument) []byte {
	type wireInst struct {
		FIGI          string `json:"figi"`
		Name          string `json:"name"`
		Ticker        string `json:"ticker"`
		ExchCode      string `json:"exchCode"`
		SecurityType  string `json:"securityType"`
		MarketSector  string `json:"marketSector"`
		CompositeFIGI string `json:"compositeFIGI"`
	}
	type resp struct {
		Data  []wireInst `json:"data"`
		Total int        `json:"total"`
	}
	var wires []wireInst
	for _, inst := range instruments {
		wires = append(wires, wireInst{
			FIGI:         inst.FIGI,
			Name:         inst.Name,
			Ticker:       inst.Ticker,
			ExchCode:     inst.ExchCode,
			SecurityType: inst.SecurityType,
			MarketSector: inst.MarketSector,
		})
	}
	b, _ := json.Marshal(resp{Data: wires, Total: len(wires)})
	return b
}

func newTestClient(ts *httptest.Server) *openfigi.Client {
	c := openfigi.NewClient()
	c.BaseURL = ts.URL
	c.Rate = 0 // no pacing in tests
	return c
}

func TestMap(t *testing.T) {
	want := []openfigi.Instrument{
		{
			FIGI:         "BBG000B9XRY4",
			Name:         "APPLE INC",
			Ticker:       "AAPL",
			ExchCode:     "US",
			SecurityType: "Common Stock",
			MarketSector: "Equity",
		},
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/v3/mapping" {
			t.Errorf("path = %q, want /v3/mapping", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(mapResponse(want))
	}))
	defer ts.Close()

	c := newTestClient(ts)
	got, err := c.Map(context.Background(), "TICKER", "AAPL", "US")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].FIGI != want[0].FIGI {
		t.Errorf("FIGI = %q, want %q", got[0].FIGI, want[0].FIGI)
	}
	if got[0].Ticker != want[0].Ticker {
		t.Errorf("Ticker = %q, want %q", got[0].Ticker, want[0].Ticker)
	}
	if got[0].ExchCode != want[0].ExchCode {
		t.Errorf("ExchCode = %q, want %q", got[0].ExchCode, want[0].ExchCode)
	}
}

func TestSearch(t *testing.T) {
	want := []openfigi.Instrument{
		{FIGI: "BBG000B9XRY4", Name: "APPLE INC", Ticker: "AAPL", ExchCode: "US"},
		{FIGI: "BBG000BMX289", Name: "APPLE INC", Ticker: "AAPL", ExchCode: "LN"},
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/v3/search" {
			t.Errorf("path = %q, want /v3/search", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(searchResponse(want))
	}))
	defer ts.Close()

	c := newTestClient(ts)
	got, err := c.Search(context.Background(), "Apple", "US", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].Name != "APPLE INC" {
		t.Errorf("Name = %q, want APPLE INC", got[0].Name)
	}
}

func TestMapRetries(t *testing.T) {
	var hits int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		want := []openfigi.Instrument{{FIGI: "BBG000BLNNH6", Name: "INTL BUSINESS MACHINES CORP", Ticker: "IBM"}}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(mapResponse(want))
	}))
	defer ts.Close()

	c := newTestClient(ts)
	c.Retries = 5

	got, err := c.Map(context.Background(), "ID_ISIN", "US4592001014", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].Ticker != "IBM" {
		t.Errorf("got %v, want IBM", got)
	}
	if hits != 3 {
		t.Errorf("server saw %d hits, want 3", hits)
	}
}
