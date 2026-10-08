package httpapi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gnoverse/gnoscope/pkg/store"
)

func TestFailureSpikeNeedsBothVolumeAndRate(t *testing.T) {
	day := store.FailureTotals{Calls: 1000, Failed: 10} // the 23 hours before: 1%
	for _, tc := range []struct {
		name string
		hour store.FailureTotals
		want bool
	}{
		{"too few failures to matter, whatever the rate", store.FailureTotals{Calls: 12, Failed: 9}, false},
		{"enough failures at the baseline rate is not a spike", store.FailureTotals{Calls: 1000, Failed: 10}, false},
		{"exactly twice the rate is not more than twice", store.FailureTotals{Calls: 500, Failed: 10}, false},
		{"enough failures at over twice the rate", store.FailureTotals{Calls: 400, Failed: 10}, true},
		{"a baseline with no failures makes any enough-failures hour a spike", store.FailureTotals{Calls: 100, Failed: 10}, true},
	} {
		d := day
		if tc.name == "a baseline with no failures makes any enough-failures hour a spike" {
			d = store.FailureTotals{Calls: 1000}
		}
		if got := evalFailureSpike(tc.hour, d).Firing; got != tc.want {
			t.Errorf("%s: firing = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAlertsEndpointStatesItsRulesAndFiresOnEvidence(t *testing.T) {
	api, db := newTestAPI(t)
	when := time.Now().UTC().Add(-30 * time.Minute).Format(time.RFC3339)

	// 12 reverts out of 12 calls in the last hour, nothing before: a spike.
	for i := 0; i < 12; i++ {
		if err := db.InsertCall("alpha", "bad-"+string(rune('a'+i)), 10+i, 0, when, "g1human", "gno.land/r/alpha/pool", "Swap", "", "", false); err != nil {
			t.Fatal(err)
		}
	}
	// One whale send over the threshold, one under it, one over it that failed.
	if err := db.InsertBankSend("alpha", "whale", 30, when, "g1a", "g1b", "2000000000000ugnot", true); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertBankSend("alpha", "small", 31, when, "g1a", "g1b", "5000000ugnot", true); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertBankSend("alpha", "reverted", 32, when, "g1a", "g1b", "9000000000000ugnot", false); err != nil {
		t.Fatal(err)
	}

	rec, body := get(t, api.HandleAlerts, "/api/alerts?network=alpha")
	if rec.Code != 200 {
		t.Fatalf("status = %d, body %s", rec.Code, body)
	}
	var resp alertsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	byID := map[string]Alert{}
	for _, a := range resp.Alerts {
		if a.Rule == "" && a.Unread == "" {
			t.Errorf("alert %s states no rule: a verdict nobody can check", a.ID)
		}
		byID[a.ID] = a
	}
	if !byID["failure-spike"].Firing {
		t.Errorf("failure-spike = %+v, want firing", byID["failure-spike"])
	}
	big := byID["large-transfer"]
	if !big.Firing || len(big.Evidence) != 1 {
		t.Fatalf("large-transfer = %+v, want firing on exactly the one successful send over the threshold", big)
	}
}

func TestValidatorUptimeAlert(t *testing.T) {
	ok := ValidatorUptime{Address: "g1good", Name: "good", Signed: 100, Uptime: 1}
	slow := ValidatorUptime{Address: "g1slowaddress0000", Signed: 90, Missed: 10, Uptime: 0.9}
	edge := ValidatorUptime{Address: "g1edge", Name: "edge", Signed: 95, Missed: 5, Uptime: 0.95}

	got := evalValidatorUptime(validatorUptimeResponse{Requested: 100, Read: 100, Validators: []ValidatorUptime{ok, edge}})
	if got.Firing || got.Unread != "" {
		t.Errorf("exactly the threshold is not below it: %+v", got)
	}
	got = evalValidatorUptime(validatorUptimeResponse{Requested: 100, Read: 100, Validators: []ValidatorUptime{slow, ok}})
	if !got.Firing || len(got.Evidence) != 1 || got.Evidence[0].Href != "/address/g1slowaddress0000?tab=validator" {
		t.Errorf("a validator at 90%% = %+v, want firing on exactly it", got)
	}
	// Nobody read is not nobody missing: it is "not read".
	got = evalValidatorUptime(validatorUptimeResponse{Requested: 100, Read: 0, Note: "no verified RPC"})
	if got.Firing || got.Unread != "no verified RPC" {
		t.Errorf("no block read = %+v, want unread with the node's reason", got)
	}
	got = evalValidatorUptime(validatorUptimeResponse{Requested: 100, Read: 30, Validators: []ValidatorUptime{slow}})
	if got.Firing || got.Unread == "" {
		t.Errorf("30 of 100 read = %+v, want unread rather than a verdict on a thin window", got)
	}
}
