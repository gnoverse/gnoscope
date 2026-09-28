package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// qeval answers in Gno's debug repr, not JSON, and the empty string is a real
// answer meaning "nobody claims this path". Reading that as a failed query is
// the mistake delegationHolderFromRepr already made once on a different value:
// it decides whether the page says "not linked" or "the node is down", which
// are different things to tell a reader.
func TestForgeStringRepr(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		ok             bool
	}{
		{"a repo id", `("moul/forge" string)`, "moul/forge", true},
		{"nobody claims it", `("" string)`, "", true},
		{"a tag", `("v1.2.0" string)`, "v1.2.0", true},
		{"padded", "  (\"moul/forge\" string)\n", "moul/forge", true},
		{"an escaped quote", `("a\"b" string)`, `a\"b`, true},
		// Not a string result: an int, a struct, or an ABCI error that reached
		// us as text. None of them may be read as an empty repo id.
		{"an int", `(3 int)`, "", false},
		{"a struct", `(&(struct{...}) *...UserData)`, "", false},
		{"empty", "", "", false},
		{"an error body", `unknown function`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := forgeStringRe.FindStringSubmatch(trimForTest(tc.in))
			if tc.ok != (m != nil) {
				t.Fatalf("match = %v, want %v for %q", m != nil, tc.ok, tc.in)
			}
			if tc.ok && m[1] != tc.want {
				t.Errorf("got %q, want %q", m[1], tc.want)
			}
		})
	}
}

func TestForgeIntRepr(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		want     string
		ok       bool
	}{
		{"a count", `(3 int)`, "3", true},
		{"zero", `(0 int)`, "0", true},
		{"negative", `(-1 int)`, "-1", true},
		{"a string", `("3" string)`, "", false},
		{"empty", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := forgeIntRe.FindStringSubmatch(trimForTest(tc.in))
			if tc.ok != (m != nil) {
				t.Fatalf("match = %v, want %v for %q", m != nil, tc.ok, tc.in)
			}
			if tc.ok && m[1] != tc.want {
				t.Errorf("got %q, want %q", m[1], tc.want)
			}
		})
	}
}

// The forge is realm state, so its answer is per chain. A blended read would
// attribute one chain's issues to another chain's package.
func TestForgeLinkRequiresANetwork(t *testing.T) {
	api, _ := newTestAPI(t)
	rec := muxGETStatus(t, api, "/api/gnohub/forge/r/moul/home")
	if rec.Code != 400 {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// No RPC is configured in a unit test, so this is the degraded path: a 200 that
// says why, never a 500. A realm page must not break because a forge nobody
// uses could not be reached.
func TestForgeLinkDegradesWithoutRPC(t *testing.T) {
	api, _ := newTestAPI(t)
	var got ForgeLink
	muxGET(t, api, "/api/gnohub/forge/r/moul/home?network=alpha", &got)
	if got.Linked {
		t.Error("linked with no chain to ask")
	}
	if got.Unavailable == "" {
		t.Error("an unreachable forge has to say so, or the frontend cannot tell it from `not linked`")
	}
	if got.Path != "gno.land/r/moul/home" {
		t.Errorf("path = %q", got.Path)
	}
}

func trimForTest(s string) string { return strings.TrimSpace(s) }

// muxGETStatus is muxGET without the 200 assertion, for the paths whose status
// IS the thing under test.
func muxGETStatus(t *testing.T, api *API, url string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	return rec
}
