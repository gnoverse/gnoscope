package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTallyUptimeCountsSignedAndMissedAndRanksWorstFirst(t *testing.T) {
	members := []ValsetMember{{Address: "g1a", Name: "a"}, {Address: "g1b"}, {Address: "g1c"}}
	blocks := []map[string]bool{
		{"g1a": true, "g1b": true, "g1c": true},
		{"g1a": true, "g1b": true},
		{"g1a": true, "g1c": true},
		{"g1a": true},
	}
	got := tallyUptime(members, blocks)
	if len(got) != 3 {
		t.Fatalf("got %d rows", len(got))
	}
	// g1a signed 4 of 4, g1b 2 of 4, g1c 2 of 4: worst first, ties by address.
	if got[0].Address != "g1b" || got[1].Address != "g1c" || got[2].Address != "g1a" {
		t.Fatalf("order = %s %s %s, want g1b g1c g1a", got[0].Address, got[1].Address, got[2].Address)
	}
	if got[0].Signed != 2 || got[0].Missed != 2 || got[0].Uptime != 0.5 {
		t.Errorf("g1b = %+v, want 2 signed, 2 missed, 0.5", got[0])
	}
	if got[2].Uptime != 1 || got[2].Name != "a" {
		t.Errorf("g1a = %+v, want full uptime and its name", got[2])
	}
}

// With no block read there is no denominator, and a validator must not read as
// a 0% failure for a window nobody looked at.
func TestTallyUptimeWithNothingReadIsNotZeroPercentOfAnything(t *testing.T) {
	got := tallyUptime([]ValsetMember{{Address: "g1a"}}, nil)
	if got[0].Signed != 0 || got[0].Missed != 0 {
		t.Fatalf("got %+v, want nothing counted", got[0])
	}
}

func TestFetchRPCBlockSignersSkipsNullPrecommits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/block" || r.URL.Query().Get("height") != "42" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"result":{"block":{"last_commit":{"precommits":[
			{"validator_address":"g1a"}, null, {"validator_address":"g1c"}]}}}}`))
	}))
	defer srv.Close()

	got, err := fetchRPCBlockSigners(context.Background(), srv.URL, 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got["g1a"] || !got["g1c"] || got["g1b"] {
		t.Fatalf("signers = %v, want g1a and g1c only", got)
	}

	if _, err := fetchRPCBlockSigners(context.Background(), srv.URL, 43); err == nil {
		t.Error("a 404 must be an error, not an empty set: an unread block is not a missed one")
	}
}
