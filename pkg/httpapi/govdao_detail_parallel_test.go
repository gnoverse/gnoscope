package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// slowRender answers vm/qrender after a delay, with a different body for the
// proposal and for its votes, and can be told to fail either one.
func slowRender(t *testing.T, delay time.Duration, failDetail, failVotes bool) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Params struct{ Path, Data string } `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		data, _ := base64.StdEncoding.DecodeString(req.Params.Data)
		votes := strings.HasSuffix(string(data), "/votes")

		time.Sleep(delay)
		if (votes && failVotes) || (!votes && failDetail) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		md := "# Prop #1 - A title\nAuthor: [@aeddi](/u/aeddi)\n\nStatus: ACCEPTED\n"
		if votes {
			md = "YES from T1 (VPPM 1):\n- [@aeddi](/u/aeddi)\n"
		}
		fmt.Fprintf(w, `{"result":{"response":{"ResponseBase":{"Data":%q}}}}`,
			base64.StdEncoding.EncodeToString([]byte(md)))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// A proposal page is keyed on its id, so no warmer covers it and whoever opens
// it pays in full. Its two renders do not read each other — measured against
// rpc.gno.land on 2026-10-20 at 0.57s and 0.78s — so paying for them end to
// end was 1.35s nobody had to wait for.
func TestProposalRendersRunConcurrently(t *testing.T) {
	const delay = 300 * time.Millisecond
	rpc := slowRender(t, delay, false, false)

	start := time.Now()
	detail := FetchGovDAOProposal(context.Background(), "renders-concurrent", rpc, 1)
	elapsed := time.Since(start)

	if detail.Title == "" {
		t.Fatalf("no title parsed: %+v", detail.Errors)
	}
	if len(detail.Votes) != 1 {
		t.Errorf("got %d votes, want 1 — the votes render was dropped", len(detail.Votes))
	}
	// Half way between one delay and two: generous on a loaded machine, and
	// still impossible for a sequential pair.
	if want := delay * 3 / 2; elapsed >= want {
		t.Errorf("took %v, want under %v — the renders ran in sequence", elapsed, want)
	}
}

// Running them together must not reorder what a reader is told. The errors
// list is part of the response and reads detail-then-votes.
func TestProposalRenderErrorsKeepTheirOrder(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		failDetail, failVotes bool
		wantFirst, wantSecond string
		wantCount             int
	}{
		{"both fail", true, true, "detail: ", "votes: ", 2},
		{"only the votes fail", false, true, "votes: ", "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rpc := slowRender(t, 0, tc.failDetail, tc.failVotes)
			detail := FetchGovDAOProposal(context.Background(), "errors-"+tc.name, rpc, 1)

			if len(detail.Errors) != tc.wantCount {
				t.Fatalf("got %d errors %v, want %d", len(detail.Errors), detail.Errors, tc.wantCount)
			}
			if !strings.HasPrefix(detail.Errors[0], tc.wantFirst) {
				t.Errorf("first error is %q, want it to start with %q", detail.Errors[0], tc.wantFirst)
			}
			if tc.wantSecond != "" && !strings.HasPrefix(detail.Errors[1], tc.wantSecond) {
				t.Errorf("second error is %q, want it to start with %q", detail.Errors[1], tc.wantSecond)
			}
		})
	}
}

// Every name on a proposal is its own vm/qeval round trip, and a proposal with
// twenty voters was twenty of them end to end.
func TestNameResolutionIsBounded(t *testing.T) {
	const delay = 50 * time.Millisecond
	const names = 16

	var fns []func()
	done := make([]bool, names)
	start := time.Now()
	for i := range names {
		fns = append(fns, func() {
			time.Sleep(delay)
			done[i] = true
		})
	}
	resolveConcurrently(fns, usernameResolveConcurrency)
	elapsed := time.Since(start)

	for i, ok := range done {
		if !ok {
			t.Errorf("name %d was never resolved", i)
		}
	}
	// 16 at 4 a time is 4 rounds. Sequential would be 16.
	if min, max := delay*7/2, delay*8; elapsed < min || elapsed > max {
		t.Errorf("took %v, want between %v and %v — the bound is not %d", elapsed, min, max, usernameResolveConcurrency)
	}
}
