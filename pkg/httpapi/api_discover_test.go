package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/moul/mygnoscan/pkg/discover"
	"github.com/moul/mygnoscan/pkg/glossary"
	"github.com/moul/mygnoscan/pkg/store"
)

func withGlossaryAPI(t *testing.T) {
	t.Helper()
	raw, err := os.ReadFile("../../docs/glossary.md")
	if err != nil {
		t.Fatalf("read docs/glossary.md: %v", err)
	}
	prev := glossary.Default
	glossary.MustLoad(raw)
	t.Cleanup(func() { glossary.Default = prev })
}

func seedEvent(t *testing.T, db *store.DB, kind, subject, actor, namespace string, hoursAgo float64, score float64) {
	t.Helper()
	at := time.Now().UTC().Add(-time.Duration(hoursAgo * float64(time.Hour))).Format(time.RFC3339)
	e := store.DiscoverEvent{
		Network: "alpha", ID: store.EventID("alpha", kind, subject, 0), Kind: kind,
		At: at, Height: 100, Actor: actor, Target: "gno.land/r/" + namespace + "/" + subject,
		Namespace: namespace,
		Facts:     json.RawMessage(`{"package_name":"` + subject + `"}`),
		Layers: json.RawMessage(`{"what":{"text":"MsgAddPackage published it."},` +
			`"means":{"text":"Somebody put a new app on the chain."},` +
			`"matters":{"text":"Anyone can use it now."}}`),
		ScoreBase: score, BuiltAt: time.Now().UTC().Format(time.RFC3339),
	}
	if _, err := db.UpsertDiscoverEvents([]store.DiscoverEvent{e}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func getDiscover(t *testing.T, api *API, url string) discoverResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	api.HandleDiscover(rec, httptest.NewRequest(http.MethodGet, url, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var resp discoverResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

// Merging two chains' events into one ranked list produces a page true of
// neither, so the endpoint refuses rather than guessing.
func TestDiscoverIsPerChain(t *testing.T) {
	withGlossaryAPI(t)
	api, _ := newTestAPI(t)
	rec := httptest.NewRecorder()
	api.HandleDiscover(rec, httptest.NewRequest(http.MethodGet, "/api/discover", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}

// The flat fields are projections. A headline that said something its layers do
// not would be invisible to anyone auditing the structured half.
func TestFlatFieldsProjectTheLayers(t *testing.T) {
	withGlossaryAPI(t)
	api, db := newTestAPI(t)
	seedEvent(t, db, "package.deployed", "hello", "g1a", "moul", 1, 25)

	resp := getDiscover(t, api, "/api/discover?network=alpha")
	if len(resp.Events) != 1 {
		t.Fatalf("%d events, want 1", len(resp.Events))
	}
	e := resp.Events[0]
	var layers discover.Layers
	if err := json.Unmarshal(e.Layers, &layers); err != nil {
		t.Fatal(err)
	}
	if e.Headline != layers.Means.Text {
		t.Errorf("headline %q is not layers.means.text %q", e.Headline, layers.Means.Text)
	}
	if e.Explanation != layers.Means.Text+"\n"+layers.Matters.Text {
		t.Errorf("explanation is not means + newline + matters: %q", e.Explanation)
	}
	if e.VerdictReason == "" {
		t.Error("no verdict_reason: it may be the only thing anybody reads")
	}
}

// With no clearance configured, nothing attributable can be recommended. A
// fresh deployment recommends nothing it has not been told it may.
func TestAnUnconfiguredDeploymentRecommendsNothingAttributable(t *testing.T) {
	withGlossaryAPI(t)
	api, db := newTestAPI(t)
	seedEvent(t, db, "package.deployed", "hello", "g1a", "moul", 1, 500)

	resp := getDiscover(t, api, "/api/discover?network=alpha")
	if resp.ClearanceConfigured {
		t.Error("reports a clearance config where none was set")
	}
	for _, e := range resp.Events {
		if e.Verdict == discover.VerdictShare {
			t.Errorf("%s reached share with no clearance configured", e.ID)
		}
	}

	// And a chain-wide fact is still shareable, because nobody privately owns
	// how many wallets joined this week. Without this an unconfigured
	// deployment could recommend nothing at all, ever.
	seedEvent(t, db, "chain.spike", "2026-09-18", "", "", 1, 500)
	resp = getDiscover(t, api, "/api/discover?network=alpha")
	var sawShare bool
	for _, e := range resp.Events {
		if e.Kind == "chain.spike" && e.Verdict == discover.VerdictShare {
			sawShare = true
		}
	}
	if !sawShare {
		t.Error("a chain-wide event was not shareable on an unconfigured deployment")
	}
}

// One deployer publishing many packages must not own the page. On a real chain
// a single actor published 179 in one day.
func TestOneActorCannotOwnThePage(t *testing.T) {
	withGlossaryAPI(t)
	api, db := newTestAPI(t)
	for i := 0; i < 12; i++ {
		seedEvent(t, db, "package.deployed", fmt.Sprintf("spam%02d", i), "g1spammer", "spammer", 1, 25)
	}
	// One newcomer, scored lower before damping.
	seedEvent(t, db, "package.deployed", "newcomer", "g1newcomer", "newbie", 1, 20)

	resp := getDiscover(t, api, "/api/discover?network=alpha&limit=10")

	var spamInTop10, newcomerRank int
	newcomerRank = -1
	for i, e := range resp.Events {
		if e.Actor == "g1spammer" && !e.Demoted {
			spamInTop10++
		}
		if e.Actor == "g1newcomer" {
			newcomerRank = i
		}
	}
	if spamInTop10 > 3 {
		t.Errorf("one actor holds %d undemoted rows of the top 10, cap is 3", spamInTop10)
	}
	if newcomerRank < 0 || newcomerRank >= 4 {
		t.Errorf("the newcomer's single deploy ranked %d; damping should lift it near the top", newcomerRank)
	}
}

// The cap demotes and never deletes: an over-quota row keeps its score, moves
// below the boundary and says so, which is what keeps the ranking checkable.
func TestTheDiversityCapDemotesRatherThanDeletes(t *testing.T) {
	withGlossaryAPI(t)
	api, db := newTestAPI(t)
	for i := 0; i < 8; i++ {
		seedEvent(t, db, "package.deployed", fmt.Sprintf("p%02d", i), "g1same", "same", 1, 25)
	}

	resp := getDiscover(t, api, "/api/discover?network=alpha&limit=200")
	if len(resp.Events) != 8 {
		t.Fatalf("%d events, want all 8: the cap must not delete", len(resp.Events))
	}
	var demoted int
	for _, e := range resp.Events {
		if e.Demoted {
			demoted++
			if e.Score <= 0 {
				t.Errorf("%s was demoted and lost its score", e.ID)
			}
		}
	}
	if demoted == 0 {
		t.Error("eight events from one actor and none demoted")
	}
	// Demoted rows sit below the kept ones.
	seenDemoted := false
	for _, e := range resp.Events {
		if e.Demoted {
			seenDemoted = true
		} else if seenDemoted {
			t.Error("an undemoted row appears below a demoted one")
			break
		}
	}
}

// Recency orders equal events, so a quiet chain still puts the newest first.
func TestNewerWinsAtEqualBase(t *testing.T) {
	withGlossaryAPI(t)
	api, db := newTestAPI(t)
	seedEvent(t, db, "package.deployed", "older", "g1a", "one", 100, 25)
	seedEvent(t, db, "package.deployed", "newer", "g1b", "two", 1, 25)

	resp := getDiscover(t, api, "/api/discover?network=alpha")
	if len(resp.Events) < 2 {
		t.Fatalf("%d events", len(resp.Events))
	}
	if resp.Events[0].Target != "gno.land/r/two/newer" {
		t.Errorf("first is %q, want the newer event", resp.Events[0].Target)
	}
	if resp.Events[0].Score <= resp.Events[1].Score {
		t.Error("the newer event did not score higher at an equal base")
	}
}

// A typo in a shared link should show the feed, not an error page.
func TestAMangledFilterDoesNotBreakThePage(t *testing.T) {
	withGlossaryAPI(t)
	api, db := newTestAPI(t)
	seedEvent(t, db, "package.deployed", "hello", "g1a", "moul", 1, 25)

	for _, url := range []string{
		"/api/discover?network=alpha&verdict=nonsense",
		"/api/discover?network=alpha&window=nonsense",
		"/api/discover?network=alpha&limit=-5",
		"/api/discover?network=alpha&limit=99999",
		"/api/discover?network=alpha&cursor=garbage",
		"/api/discover?network=alpha&from=not-a-date",
	} {
		resp := getDiscover(t, api, url)
		if len(resp.Events) == 0 {
			t.Errorf("%s returned nothing", url)
		}
	}
}

// The window defaults to what the builder actually built. Asking for more would
// report an unbuilt stretch as a quiet chain.
func TestTheWindowAndItsFilters(t *testing.T) {
	withGlossaryAPI(t)
	api, db := newTestAPI(t)
	seedEvent(t, db, "package.deployed", "recent", "g1a", "one", 2, 25)
	seedEvent(t, db, "package.deployed", "old", "g1b", "two", 24*20, 25)

	if got := getDiscover(t, api, "/api/discover?network=alpha").Window["label"]; got != DiscoverDefaultWindow {
		t.Errorf("default window = %q, want %q", got, DiscoverDefaultWindow)
	}
	if n := len(getDiscover(t, api, "/api/discover?network=alpha&window=24h").Events); n != 1 {
		t.Errorf("24h window returned %d events, want just the recent one", n)
	}
	if n := len(getDiscover(t, api, "/api/discover?network=alpha&window=all").Events); n != 2 {
		t.Errorf("window=all returned %d events, want both", n)
	}
	if n := len(getDiscover(t, api, "/api/discover?network=alpha&kind=chain.spike").Events); n != 0 {
		t.Errorf("filtering to an absent kind returned %d events", n)
	}
	if n := len(getDiscover(t, api, "/api/discover?network=alpha&namespace=one").Events); n != 1 {
		t.Errorf("namespace filter returned %d events, want 1", n)
	}
}

// The staleness of the page is the gap between the build and the response, and
// a reader who refreshes and sees nothing new deserves to know which it is.
func TestTheEnvelopeSaysHowStaleItIs(t *testing.T) {
	withGlossaryAPI(t)
	api, db := newTestAPI(t)
	seedEvent(t, db, "package.deployed", "hello", "g1a", "moul", 1, 25)

	resp := getDiscover(t, api, "/api/discover?network=alpha")
	if resp.BuiltAt == "" || resp.GeneratedAt == "" {
		t.Errorf("built_at=%q generated_at=%q", resp.BuiltAt, resp.GeneratedAt)
	}
	if resp.GlossaryVersion == "" {
		t.Error("no glossary_version: a consumer cannot tell when a definition changed under it")
	}
	if resp.Vocabulary != DiscoverVocabulary {
		t.Errorf("vocabulary = %d", resp.Vocabulary)
	}
	if resp.Counts["package.deployed"] != 1 {
		t.Errorf("counts = %v", resp.Counts)
	}
}

// A number in an envelope that a consumer cannot check is worse than no number,
// because it will be believed. This reported 200 for a window holding 474.
func TestTotalCountsTheWindowNotThePage(t *testing.T) {
	withGlossaryAPI(t)
	api, db := newTestAPI(t)
	for i := 0; i < 25; i++ {
		seedEvent(t, db, "package.deployed", fmt.Sprintf("p%02d", i),
			fmt.Sprintf("g1a%02d", i), fmt.Sprintf("ns%02d", i), 1, 25)
	}

	resp := getDiscover(t, api, "/api/discover?network=alpha&limit=5")
	if len(resp.Events) != 5 {
		t.Fatalf("%d events, want the 5 asked for", len(resp.Events))
	}
	if resp.Total != 25 {
		t.Errorf("total = %d, want 25: it counts the window, not the page", resp.Total)
	}
	// And it respects the filter rather than counting everything.
	filtered := getDiscover(t, api, "/api/discover?network=alpha&namespace=ns00&limit=5")
	if filtered.Total != 1 {
		t.Errorf("filtered total = %d, want 1", filtered.Total)
	}
}
