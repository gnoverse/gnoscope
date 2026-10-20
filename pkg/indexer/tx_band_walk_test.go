package indexer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// bandedIndexer answers transaction queries from a fixed set of heights,
// honouring whatever height bounds the query carries, and records every bound
// it was asked for. That is what lets a test say "no block was read twice"
// rather than "the right number of queries went out".
type bandedIndexer struct {
	mu      sync.Mutex
	tip     int
	at      []int // one transaction at each of these heights, ascending
	bands   [][2]int
	queries int
}

var gtRe = regexp.MustCompile(`gt: (-?\d+)`)
var ltRe = regexp.MustCompile(`lt: (-?\d+)`)

func (f *bandedIndexer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Query string `json:"query"`
	}
	body := make([]byte, r.ContentLength)
	r.Body.Read(body)
	req.Query = string(body)

	switch {
	case strings.Contains(req.Query, "__type(name:"):
		fmt.Fprint(w, `{"data":{"__type":null}}`)
		return
	case strings.Contains(req.Query, "latestBlockHeight"):
		fmt.Fprintf(w, `{"data":{"latestBlockHeight":%d}}`, f.tip)
		return
	}

	lo, hi := -1, f.tip+1 // exclusive on both sides, matching the gt/lt filters
	if m := gtRe.FindStringSubmatch(req.Query); m != nil {
		lo, _ = strconv.Atoi(m[1])
	}
	if m := ltRe.FindStringSubmatch(req.Query); m != nil {
		hi, _ = strconv.Atoi(m[1])
	}

	f.mu.Lock()
	f.queries++
	f.bands = append(f.bands, [2]int{lo, hi})
	f.mu.Unlock()

	// Newest first within the band, the way `order: { heightAndIndex: DESC }`
	// answers. The walk relies on that to concatenate its bands into one
	// descending list.
	var rows []string
	for i := len(f.at) - 1; i >= 0; i-- {
		if h := f.at[i]; h > lo && h < hi {
			rows = append(rows, fmt.Sprintf(`{"hash":"tx-%d","block_height":%d}`, h, h))
		}
	}
	fmt.Fprintf(w, `{"data":{"getTransactions":[%s]}}`, strings.Join(rows, ","))
}

// The ladder widens towards genesis whenever a predicate is too sparse to
// satisfy `need`, which is the normal case for a realm or for gov/dao. Each
// round must read only what the rounds before it did not: re-reading from the
// tip every time cost 10.2s against production where reading each block once
// cost 8.1s, for the same answer.
func TestWindowsDoNotReadTheSameBlockTwice(t *testing.T) {
	for _, tc := range []struct {
		name string
		at   []int
		need int
	}{
		// Nothing at all: the ladder goes all the way down, which is where the
		// re-reading used to cost the most.
		{"a predicate with no rows", nil, 500},
		// Rows only near genesis: found on the last band, nowhere above it.
		{"rows only at the bottom", []int{0, 7, 1200}, 500},
		// Rows spread out, still never reaching need.
		{"rows spread thin", []int{0, 500000, 900000, 944390}, 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &bandedIndexer{tip: 944396, at: tc.at}
			srv := httptest.NewServer(fake)
			defer srv.Close()

			txs, err := NewClient(srv.URL).GetGovDAOTransactions(context.Background(), tc.need)
			if err != nil {
				t.Fatal(err)
			}

			fake.mu.Lock()
			defer fake.mu.Unlock()

			// Every row exactly once, newest first.
			if len(txs) != len(tc.at) {
				t.Fatalf("got %d rows, want %d", len(txs), len(tc.at))
			}
			for i := range txs {
				want := tc.at[len(tc.at)-1-i]
				if txs[i].BlockHeight != want {
					t.Errorf("row %d is height %d, want %d — bands are out of order", i, txs[i].BlockHeight, want)
				}
			}

			// No band overlaps another, and together they cover the chain.
			covered := 0
			for i, b := range fake.bands {
				lo, hi := b[0], b[1]
				if hi <= lo+1 {
					t.Errorf("band %d is empty: (%d, %d)", i, lo, hi)
				}
				covered += hi - lo - 1
				for j, other := range fake.bands[:i] {
					if lo+1 < other[1] && other[0]+1 < hi {
						t.Errorf("band %d (%d, %d) overlaps band %d (%d, %d)", i, lo, hi, j, other[0], other[1])
					}
				}
			}
			if want := fake.tip + 1; covered != want {
				t.Errorf("the bands cover %d heights, want %d — there is a gap", covered, want)
			}
		})
	}
}

// A predicate dense enough to satisfy need in the first band must still stop
// there. The banding changes what each round reads, not when the walk ends.
func TestADenseWindowStillStopsAtTheFirstBand(t *testing.T) {
	at := make([]int, 0, 60)
	for h := 944396; h > 944396-60; h-- {
		at = append(at, h)
	}
	fake := &bandedIndexer{tip: 944396, at: at}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	txs, err := NewClient(srv.URL).GetGovDAOTransactions(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.queries != 1 {
		t.Errorf("issued %d queries, want 1 — the walk widened with enough rows in hand", fake.queries)
	}
	if len(txs) != len(at) {
		t.Errorf("got %d rows, want %d", len(txs), len(at))
	}
}
