package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const (
	testOperatorA = "g1manfred47kzduec920z88wfr64ylksmdcedlf5"
	testOperatorB = "g1aeddlftlfk27ret5rf750d7w5dume3kcsm8r8m"
)

func resetValset(t *testing.T) {
	t.Helper()
	valsetCache.reset()
	finalProposals = sync.Map{}
	t.Cleanup(func() {
		valsetCache.reset()
		finalProposals = sync.Map{}
	})
}

// fakeChain serves the four reads FetchValset makes, shaped like onyx-1 on
// 2026-10-01: /status, /validators, the valopers qeval, and gov/dao's list
// and proposal renders. g1val_a is a registered valoper ("moul") added by
// proposal 18 and raised by 19; g1val_b is a genesis validator with no
// profile and enough power to be a single point of failure.
func fakeChain(t *testing.T, api *API, chainID string) {
	t.Helper()
	resetValset(t)
	renders := map[string]string{
		"gno.land/r/gov/dao:": "# GovDAO\n## Proposals\n" +
			"### [Prop #19 - Raise 1 validator\\(s\\) to voting power 4](/r/gov/dao:19)\nAuthor: [@aeddi](/u/aeddi)\n\nStatus: ACCEPTED\n\n---\n" +
			"### [Prop #18 - Add validator " + testOperatorA + "](/r/gov/dao:18)\nAuthor: [@aeddi](/u/aeddi)\n\nStatus: ACCEPTED\n\n---\n",
		"gno.land/r/gov/dao:19": "## Prop #19 - Raise 1 validator\\(s\\) to voting power 4\nAuthor: [@aeddi](/u/aeddi)\n\n" +
			"## Validator Updates\n- " + testOperatorA + ": add (power 4)\n\nExecutor created in: `gno.land/r/sys/validators/v0`\n\n---\n\n### Stats\n- **PROPOSAL HAS BEEN ACCEPTED**\n",
		"gno.land/r/gov/dao:18": "## Prop #18 - Add validator " + testOperatorA + "\nAuthor: [@aeddi](/u/aeddi)\n\n" +
			"Mentions " + testOperatorB + " in prose, which is not a change.\n\n" +
			"## Validator Updates\n- " + testOperatorA + ": add (power 1)\n\nExecutor created in: `gno.land/r/sys/validators/v0`\n\n---\n\n### Stats\n- **PROPOSAL HAS BEEN ACCEPTED**\n",
		"gno.land/r/gov/dao:17": "# Proposal not found",
	}
	valopers := fmt.Sprintf("(%s string)", strconv.Quote("g1val_a\t"+testOperatorA+"\tmoul\ng1val_c\t"+testOperatorB+"\twaiting\n"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/status":
			fmt.Fprintf(w, `{"result":{"node_info":{"network":%q},"sync_info":{"latest_block_height":"90000"}}}`, chainID)
		case r.URL.Path == "/validators":
			io.WriteString(w, `{"result":{"block_height":"90000","validators":[
				{"address":"g1val_a","voting_power":"4"},
				{"address":"g1val_b","voting_power":"60"}]}}`)
		case r.Method == http.MethodPost:
			var req struct {
				Params struct{ Path, Data string } `json:"params"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			data, _ := base64.StdEncoding.DecodeString(req.Params.Data)
			var answer string
			switch {
			case req.Params.Path == "vm/qeval" && strings.HasPrefix(string(data), valopersPath+"."):
				answer = valopers
			case req.Params.Path == "vm/qrender":
				answer = renders[string(data)]
			}
			fmt.Fprintf(w, `{"result":{"response":{"ResponseBase":{"Data":%q}}}}`, base64.StdEncoding.EncodeToString([]byte(answer)))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	api.setRPCVerified("alpha", srv.URL)
}

func fetchLive(t *testing.T, api *API) []ValsetMember {
	t.Helper()
	rec := httptest.NewRecorder()
	api.HandleValidatorsLive(rec, httptest.NewRequest(http.MethodGet, "/api/validators/live?network=alpha", nil))
	var got []ValsetMember
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return got
}

// The set is the network's own, with the valopers profile and the proposals
// joined onto it. Before 2026-10-01 this endpoint served gnockpit's mainnet set
// for every network.
func TestValidatorsLiveIsTheNetworksOwnSet(t *testing.T) {
	api, _ := newTestAPI(t)
	gnockpitDown(t)
	fakeChain(t, api, "alpha-1")

	got := fetchLive(t, api)
	if len(got) != 2 {
		t.Fatalf("got %d members, want 2: %+v", len(got), got)
	}
	a, b := got[0], got[1]
	if a.Address != "g1val_a" || a.Moniker != "moul" || a.Operator != testOperatorA || a.Name != "moul" {
		t.Errorf("valopers join missing: %+v", a)
	}
	if len(a.Proposals) != 2 || a.Proposals[0].ID != 19 || a.Proposals[1].ID != 18 ||
		a.Proposals[1].Op != "add" || a.Proposals[1].Power != 1 || a.Proposals[0].Status != "ACCEPTED" {
		t.Errorf("proposals = %+v, want 19 then 18", a.Proposals)
	}
	if a.SPOF || !b.SPOF {
		t.Errorf("spof: a=%v b=%v, want only b (60 of 64)", a.SPOF, b.SPOF)
	}
	if b.Moniker != "" || len(b.Proposals) != 0 {
		t.Errorf("genesis validator picked up a profile: %+v", b)
	}
	// gnockpit is down, so there are no liveness figures, and they must be
	// absent rather than a confident zero.
	if a.Missed100 != nil || a.Missed24h != nil {
		t.Errorf("missed counters without a source: %+v", a)
	}
}

// gnockpit's figures join only onto the chain gnockpit describes. This is the
// exact failure the onyx page had: mainnet's numbers on a testnet's validators.
func TestValidatorsLiveIgnoresGnockpitForAnotherChain(t *testing.T) {
	api, _ := newTestAPI(t)
	gnockpitUp(t) // claims alpha-1
	fakeChain(t, api, "onyx-1")

	for _, m := range fetchLive(t, api) {
		if m.Missed24h != nil || m.Name == "val-a" || m.Name == "val-b" {
			t.Errorf("gnockpit joined onto a chain it does not describe: %+v", m)
		}
	}
}

func TestValidatorsLiveJoinsGnockpitForItsChain(t *testing.T) {
	api, _ := newTestAPI(t)
	gnockpitUp(t)
	fakeChain(t, api, "alpha-1")

	got := fetchLive(t, api)
	if got[1].Name != "val-b" || got[1].Missed100 == nil || *got[1].Missed100 != 1 {
		t.Errorf("gnockpit half missing for its own chain: %+v", got[1])
	}
	// The moniker beats gnockpit's name: it is the operator's current claim.
	if got[0].Name != "moul" {
		t.Errorf("name = %q, want the valopers moniker", got[0].Name)
	}
}

// An operator address resolves to the key it signs with, so a profile link and
// a block-proposer link land on the same page.
func TestValidatorDetailByOperatorAddress(t *testing.T) {
	api, _ := newTestAPI(t)
	gnockpitDown(t)
	fakeChain(t, api, "alpha-1")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/validator/x?network=alpha", nil)
	req.SetPathValue("addr", testOperatorA)
	api.HandleValidator(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp validatorDetailResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Validator.Address != "g1val_a" || resp.Validator.Moniker != "moul" || !resp.InSet ||
		resp.Validator.VotingPower != 4 || resp.TotalPower != 64 || len(resp.Proposals) != 2 {
		t.Errorf("got %+v", resp)
	}
}

// A registered profile not yet in the set is a validator-to-be, not a 404.
func TestValidatorDetailRegisteredNotInSet(t *testing.T) {
	api, _ := newTestAPI(t)
	gnockpitDown(t)
	fakeChain(t, api, "alpha-1")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/validator/x?network=alpha", nil)
	req.SetPathValue("addr", "g1val_c")
	api.HandleValidator(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp validatorDetailResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.InSet || resp.Validator.Moniker != "waiting" || resp.Validator.Operator != testOperatorB {
		t.Errorf("got %+v", resp)
	}
}

func TestValidatorMonikersCoverProfilesOutsideTheSet(t *testing.T) {
	api, _ := newTestAPI(t)
	gnockpitDown(t)
	fakeChain(t, api, "alpha-1")

	rec := httptest.NewRecorder()
	api.HandleValidatorMonikers(rec, httptest.NewRequest(http.MethodGet, "/api/validators/monikers?network=alpha", nil))
	var got map[string]string
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got["g1val_a"] != "moul" || got["g1val_c"] != "waiting" {
		t.Errorf("monikers = %v", got)
	}
}

// Either key of a validator resolves, so a link can mark the operator's
// account as well as the consensus key that proposes blocks. g1val_b has no
// profile and is still a validator; testOperatorB's profile is not seated yet.
func TestValidatorAddressesKeyBothKeys(t *testing.T) {
	api, _ := newTestAPI(t)
	gnockpitDown(t)
	fakeChain(t, api, "alpha-1")

	rec := httptest.NewRecorder()
	api.HandleValidatorAddresses(rec, httptest.NewRequest(http.MethodGet, "/api/validators/addresses?network=alpha", nil))
	var got map[string]ValidatorAddress
	json.Unmarshal(rec.Body.Bytes(), &got)
	want := map[string]ValidatorAddress{
		"g1val_a":     {Role: "signing", Moniker: "moul", Signing: "g1val_a", Operator: testOperatorA, InSet: true},
		testOperatorA: {Role: "operator", Moniker: "moul", Signing: "g1val_a", Operator: testOperatorA, InSet: true},
		"g1val_b":     {Role: "signing", Signing: "g1val_b", InSet: true},
		"g1val_c":     {Role: "signing", Moniker: "waiting", Signing: "g1val_c", Operator: testOperatorB},
		testOperatorB: {Role: "operator", Moniker: "waiting", Signing: "g1val_c", Operator: testOperatorB},
	}
	if len(got) != len(want) {
		t.Errorf("got %d entries, want %d: %+v", len(got), len(want), got)
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %+v, want %+v", k, got[k], w)
		}
	}
}

func TestParseValsetUpdates(t *testing.T) {
	for _, tc := range []struct {
		name string
		md   string
		want string
	}{
		{"add with power", "## Validator Updates\n- " + testOperatorA + ": add (power 4)\n", testOperatorA + " add 4"},
		{"remove", "## Validator Updates\n- " + testOperatorA + ": remove\n", testOperatorA + " remove 0"},
		{"outside the section", "Add " + testOperatorA + "\n- " + testOperatorA + ": add (power 4)\n", ""},
		{"section ends at next heading", "## Validator Updates\n### Stats\n- " + testOperatorA + ": add (power 4)\n", ""},
		{"short address", "## Validator Updates\n- g1short: add (power 4)\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, u := range parseValsetUpdates(tc.md) {
				got = append(got, fmt.Sprintf("%s %s %d", u.Addr, u.Op, u.Power))
			}
			if strings.Join(got, ",") != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsSPOF(t *testing.T) {
	for _, tc := range []struct {
		power, total int64
		want         bool
	}{
		{4, 215, false},
		{60, 215, false}, // 155 of 215 is 72%, above 2/3
		{60, 64, true},
		{1, 3, true}, // 2 of 3 is exactly 2/3, which is not more than 2/3
		{1, 4, false},
		{0, 0, false},
	} {
		if got := isSPOF(tc.power, tc.total); got != tc.want {
			t.Errorf("isSPOF(%d, %d) = %v, want %v", tc.power, tc.total, got, tc.want)
		}
	}
}

func TestParseValopersRejectsAnErrorShape(t *testing.T) {
	if _, err := parseValopers("panic: undefined: SigningAddress"); err == nil {
		t.Error("an older realm's error parsed as an empty registry")
	}
}

// A signing key with no bank account is a validator's consensus key, not "no
// account", and the operator key's page names the validator it runs.
func TestAddressIdentityPlacesBothValidatorKeys(t *testing.T) {
	api, _ := newTestAPI(t)
	gnockpitDown(t)
	fakeChain(t, api, "alpha-1")

	get := func(addr string) addressIdentity {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/address/x/identity?network=alpha", nil)
		req.SetPathValue("addr", addr)
		api.HandleAddressIdentity(rec, req)
		var out addressIdentity
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode %q: %v", rec.Body.String(), err)
		}
		return out
	}

	sig := get("g1val_a")
	if sig.Kind != identityConsensusKey || sig.Valset == nil || sig.Valset.Role != "signing" ||
		sig.Valset.Moniker != "moul" || !sig.Valset.InSet || sig.Valset.VotingPower != "4" || len(sig.Valset.Proposals) != 2 {
		t.Errorf("signing key: kind=%s valset=%+v", sig.Kind, sig.Valset)
	}
	op := get(testOperatorA)
	if op.Valset == nil || op.Valset.Role != "operator" || op.Valset.Signing != "g1val_a" || op.Kind == identityConsensusKey {
		t.Errorf("operator key: kind=%s valset=%+v", op.Kind, op.Valset)
	}
	if get("g1nobody").Valset != nil {
		t.Error("an unrelated address was placed in the set")
	}
}
