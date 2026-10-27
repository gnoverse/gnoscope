package httpapi

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gnoverse/gnoscope/pkg/config"
	"github.com/gnoverse/gnoscope/pkg/gnostate"
)

// A stored struct holding one string and one reference to another object. The
// reference is the point: expanding this must not follow it.
const stateOneObject = `{"objectid":"abc:1","value":{
  "@type":"/gno.StructValue",
  "Fields":[
    {"T":{"@type":"/gno.PrimitiveType","value":"16"},
     "V":{"@type":"/gno.StringValue","value":"hello"}},
    {"V":{"@type":"/gno.RefValue","ObjectID":"abc:2"}}
  ]}}`

func objectReq(t *testing.T, api *API, oid, query string) (*httptest.ResponseRecorder, ObjectResponse) {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/state/object/"+oid+query, nil)
	r.SetPathValue("oid", oid)
	w := httptest.NewRecorder()
	api.HandleStateObject(w, r)
	var got ObjectResponse
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	return w, got
}

// Expanding one object is one round trip and follows nothing.
//
// This is the half of the state explorer that was missing. The view resolves a
// realm's graph eagerly against a deadline and draws whatever did not fit as a
// `ref`, which was dead text: the only way onward was ?full=1, an all-or-
// nothing re-walk that on r/gnoland/blog is ~1,250 objects and ~39s and so
// does not fit the server's 30s WriteTimeout at all.
func TestHandleStateObject(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers map[string]string
		oid     string
		query   string
		status  int
		check   func(*testing.T, ObjectResponse, int32)
	}{
		{
			name:    "one object, one round trip, children left as refs",
			answers: map[string]string{"abc:1": stateOneObject},
			oid:     "abc:1",
			status:  200,
			check: func(t *testing.T, got ObjectResponse, calls int32) {
				if calls != 1 {
					t.Errorf("made %d node calls, want exactly 1: expanding one object must not walk", calls)
				}
				var refs, leaves int
				for _, c := range got.Node.Children {
					switch c.Kind {
					case gnostate.KindRef:
						refs++
					default:
						leaves++
					}
				}
				if refs != 1 {
					t.Errorf("got %d refs among %+v, want the nested object left unfollowed", refs, got.Node.Children)
				}
				if leaves == 0 {
					t.Error("the object's own values did not decode")
				}
			},
		},
		{
			// The reader opened a row that named this object. Being told the
			// node could not answer is different from being shown an empty
			// branch, which reads as "this reference points at nothing".
			name:    "an object the node does not know is a 404",
			answers: map[string]string{},
			oid:     "abc:404",
			status:  404,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, _ := newTestAPI(t)
			var calls atomic.Int32
			srv := stateNode(t, tc.answers, &calls)
			api.networks = []config.NetworkConfig{{ID: "alpha", RPCURL: srv.URL}}
			api.setRPCVerified("alpha", srv.URL)

			w, got := objectReq(t, api, tc.oid, "?network=alpha"+tc.query)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tc.status, w.Body.String())
			}
			if tc.status != 200 {
				if strings.Contains(w.Body.String(), "InvalidPkgPath") {
					t.Errorf("a raw abci error reached the reader: %s", w.Body.String())
				}
				return
			}
			if tc.check != nil {
				tc.check(t, got, calls.Load())
			}
		})
	}
}

// Same rule as the realm state view: another chain's objects rendered under
// this chain's realm is wrong in a way nobody can see.
func TestHandleStateObjectRefusesUnverifiedRPC(t *testing.T) {
	api, _ := newTestAPI(t)
	srv := stateNode(t, map[string]string{"abc:1": stateOneObject}, nil)
	api.networks = []config.NetworkConfig{{ID: "alpha", RPCURL: srv.URL}}
	// deliberately not setRPCVerified

	w, _ := objectReq(t, api, "abc:1", "?network=alpha")
	if w.Code != 503 {
		t.Errorf("status = %d, want 503 for an unverified RPC", w.Code)
	}
}
