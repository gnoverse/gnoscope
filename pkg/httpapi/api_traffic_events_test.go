package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gnoverse/gnoscope/pkg/traffic"
)

// post fires a beacon with a body, which fire() cannot: the engagement beacon
// is the one endpoint here that carries one.
func post(t *testing.T, h http.Handler, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", target, strings.NewReader(body))
	r.RemoteAddr = "203.0.113.7:51234"
	r.Header.Set("User-Agent", browserUA)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func engagementAPI(t *testing.T, store *traffic.Store) (*API, http.Handler) {
	t.Helper()
	a := &API{}
	a.SetTraffic(store)
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+traffic.EventPath, a.HandleEvent)
	return a, mux
}

func TestEventBeaconRecordsBehaviour(t *testing.T) {
	store := trafficTestStore(t)
	a, h := engagementAPI(t, store)

	body := `{"e":[
		{"k":"leave","p":"/realm/gno.land/r/moul/home","f":"/realms","d":42000,"s":80,"c":2},
		{"k":"action","p":"/realm/gno.land/r/moul/home","a":"tab:usage"},
		{"k":"search","p":"/realms","v":"moul","r":7}
	]}`
	if got := post(t, h, traffic.EventPath, body).Code; got != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", got)
	}
	store.Flush()

	rep, err := a.traffic.Engagement(traffic.Query{
		Window: traffic.ParseWindow("24h"), Who: traffic.WhoAll, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("engagement: %v", err)
	}
	if rep.Visits != 1 {
		t.Fatalf("visits = %d, want 1", rep.Visits)
	}
	if rep.MedianDwellMS != 42000 {
		t.Errorf("median dwell = %d, want 42000", rep.MedianDwellMS)
	}
	if len(rep.Actions) != 1 || rep.Actions[0].Label != "tab:usage" {
		t.Errorf("actions = %+v, want tab:usage", rep.Actions)
	}
	if len(rep.Searches) != 1 || rep.Searches[0].Label != "moul" {
		t.Errorf("searches = %+v, want moul", rep.Searches)
	}
	// The entity is derived here from the path, not read from the body: the
	// request log derives it the same way, and two implementations of one
	// classification is how two panels come to disagree.
	if len(rep.StickyPages) != 0 && rep.StickyPages[0].Label == "" {
		t.Errorf("sticky pages carry an empty label: %+v", rep.StickyPages)
	}
}

// Nothing the client sends is trusted, including how much of it there is.
func TestEventBeaconRefusesOversizeAndGarbage(t *testing.T) {
	store := trafficTestStore(t)
	a, h := engagementAPI(t, store)

	// Garbage body: answered 204 and recorded as nothing. A 400 would buy
	// nothing and would print a console error on every navigation of anyone
	// whose build is newer than their server.
	if got := post(t, h, traffic.EventPath, "not json at all").Code; got != http.StatusNoContent {
		t.Errorf("status on garbage = %d, want 204", got)
	}
	// Oversize body: the reader is cut off at the limit, so this process never
	// allocates what a client chose to send.
	huge := `{"e":[{"k":"error","p":"/","v":"` + strings.Repeat("x", maxEventBody*2) + `"}]}`
	if got := post(t, h, traffic.EventPath, huge).Code; got != http.StatusNoContent {
		t.Errorf("status on oversize = %d, want 204", got)
	}
	// A batch past the cap is truncated, not refused: the first events in it
	// are real and dropping them would lose data to punish a client.
	var b strings.Builder
	b.WriteString(`{"e":[`)
	for i := 0; i < maxEventBatch*3; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"k":"leave","p":"/","d":1000}`)
	}
	b.WriteString(`]}`)
	post(t, h, traffic.EventPath, b.String())
	store.Flush()

	rep, err := a.traffic.Engagement(traffic.Query{
		Window: traffic.ParseWindow("24h"), Who: traffic.WhoAll, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("engagement: %v", err)
	}
	if rep.Visits != int64(maxEventBatch) {
		t.Errorf("visits = %d, want the batch cap of %d", rep.Visits, maxEventBatch)
	}
	if len(rep.Errors) != 0 {
		t.Errorf("errors = %+v, want none: the oversize body must not have been stored", rep.Errors)
	}
}

// The visitor id is derived from the connection, never from the body. A client
// that sends one must not be able to choose who it is counted as, and two
// clients on one connection must not be able to claim to be a crowd.
func TestEventBeaconIgnoresClientSuppliedIdentity(t *testing.T) {
	store := trafficTestStore(t)
	a, h := engagementAPI(t, store)

	for _, v := range []string{"someone-else", "and-another"} {
		post(t, h, traffic.EventPath, `{"e":[{"k":"leave","p":"/","d":1000,"visitor":"`+v+`"}]}`)
	}
	store.Flush()

	rep, err := a.traffic.Engagement(traffic.Query{
		Window: traffic.ParseWindow("24h"), Who: traffic.WhoAll, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("engagement: %v", err)
	}
	if rep.Visits != 2 {
		t.Fatalf("visits = %d, want 2", rep.Visits)
	}
	if rep.Readers != 1 {
		t.Errorf("readers = %d, want 1: both beacons came from one connection, whatever they claimed",
			rep.Readers)
	}
}
