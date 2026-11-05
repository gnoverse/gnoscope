package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gnoverse/gnoscope/pkg/config"
	"github.com/gnoverse/gnoscope/pkg/store"
)

// /api/holders must answer whether or not quotes are ready.
//
// A quote refresh is dozens of sequential-ish RPC reads. Measured against
// production on 2026-10-26, twelve samples ten minutes apart: five came back
// 502, because the handler outlived http.Server's 30s WriteTimeout waiting for
// one, and the slowest good answer took 71.7s. The USD column is documented as
// best-effort and the ranking already falls back to position count, so a page
// nobody gets is the wrong trade.
func TestHoldersAnswersWithoutQuotes(t *testing.T) {
	const network = "holders-no-quotes"
	resetPriceCache(t)

	// No RPC for this network, so pricesFor fails fast rather than slowly.
	// That makes this a test of the contract — 200, and the reader is told why
	// the column is missing — not of the deadline. The deadline itself is
	// TestPricesForHonoursACancelledContext below, since what the handler
	// relies on is pricesFor giving up when its context does.
	a := &API{
		db:       store.NewTestDB(t),
		networks: []config.NetworkConfig{{ID: network}},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/holders", a.HandleHolders)

	rec := httptest.NewRecorder()
	start := time.Now()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/holders?network="+network, nil))
	elapsed := time.Since(start)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 even with no quotes: %s", rec.Code, rec.Body.String())
	}
	if elapsed > holdersPriceBudget*3 {
		t.Errorf("took %v, want it bounded near the %v price budget", elapsed, holdersPriceBudget)
	}

	var body struct {
		PricesUnavailable string `json:"prices_unavailable"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The reader is told, rather than shown an empty column and left to guess.
	if body.PricesUnavailable == "" {
		t.Error("prices_unavailable is empty, so the missing USD column looks like a real zero")
	}
}

// A warm cache answers in tens of milliseconds and must not be cut off by the
// budget meant for a cold one.
func TestHoldersUsesQuotesItAlreadyHas(t *testing.T) {
	const network = "holders-warm-quotes"
	resetPriceCache(t)
	seedPrice(network, time.Second) // fresh, well inside priceQuoteTTL

	a := &API{
		db:       store.NewTestDB(t),
		networks: []config.NetworkConfig{{ID: network}},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/holders", a.HandleHolders)

	rec := httptest.NewRecorder()
	start := time.Now()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/holders?network="+network, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if elapsed := time.Since(start); elapsed >= holdersPriceBudget {
		t.Errorf("took %v with quotes already in hand, want nowhere near the %v budget", elapsed, holdersPriceBudget)
	}
	var body struct {
		PricesUnavailable string `json:"prices_unavailable"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.PricesUnavailable != "" {
		t.Errorf("prices_unavailable = %q with a fresh quote in the cache", body.PricesUnavailable)
	}
}

// The budget only works if pricesFor actually gives up when its context does.
// That is the whole mechanism behind holdersPriceBudget, so it is worth one
// test of its own rather than being assumed.
func TestPricesForHonoursACancelledContext(t *testing.T) {
	const network = "prices-cancelled"
	resetPriceCache(t)
	// Past TTL and past the grace, so there is nothing servable and the
	// blocking path is the only one left.
	seedPrice(network, priceQuoteTTL+priceStaleGrace+time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	a := &API{networks: []config.NetworkConfig{{ID: network}}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.pricesFor(ctx, network)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pricesFor ignored a cancelled context, so the holders budget cannot bound it")
	}
}
