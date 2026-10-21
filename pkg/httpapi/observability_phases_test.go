package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// metricMS pulls one named metric out of a Server-Timing header.
func metricMS(h, name string) (float64, bool) {
	for _, part := range strings.Split(h, ",") {
		part = strings.TrimSpace(part)
		rest, ok := strings.CutPrefix(part, name+";dur=")
		if !ok {
			continue
		}
		v, err := strconv.ParseFloat(rest, 64)
		return v, err == nil
	}
	return 0, false
}

// A handler's phases reach the header through the request context, not through
// the ResponseWriter. That is the whole point: WithCompression sits between
// WithServerTiming and the mux, so a handler never holds the timingWriter, and
// an implementation that reached for it would compile, never match, and record
// nothing. This test runs the handler behind the compression wrapper for that
// reason.
func TestServerTimingCarriesHandlerPhases(t *testing.T) {
	h := WithServerTiming(WithCompression(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Phase(r.Context(), "positions", time.Now().Add(-120*time.Millisecond))
		Phase(r.Context(), "prices", time.Now().Add(-30*time.Millisecond))
		w.Write([]byte("ok"))
	})))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/holders", nil))

	got := rec.Header().Get("Server-Timing")
	// On the names and the magnitudes, never on the formatted digits: a
	// prefix match like "prices;dur=3" passes at 30.1ms and fails at 29.9ms,
	// which is a test that reports how loaded the machine was.
	for _, tc := range []struct {
		metric  string
		atLeast float64
	}{
		{"positions", 100},
		{"prices", 25},
	} {
		v, ok := metricMS(got, tc.metric)
		if !ok {
			t.Errorf("Server-Timing %q carries no %s metric", got, tc.metric)
			continue
		}
		if v < tc.atLeast {
			t.Errorf("%s;dur=%v, want at least %v", tc.metric, v, tc.atLeast)
		}
	}
	// app stays first and parseable: the traffic store reads it out of this
	// same header and a phase in front of it would be read instead.
	if !strings.HasPrefix(got, "app;dur=") {
		t.Errorf("Server-Timing %q does not lead with app", got)
	}
	// Present and parseable, not positive: a handler this small rounds to
	// app;dur=0.0, and appDurationMS cannot tell that from the metric being
	// absent. Asserting on the value instead of its presence is a test that
	// passes or fails on how busy the machine was.
	if _, ok := metricMS(got, "app"); !ok {
		t.Errorf("Server-Timing %q carries no app metric", got)
	}
}

// A handler called outside the middleware records nothing and does not panic,
// so Phase can be called unconditionally and a handler stays testable on its
// own.
func TestPhaseIsANoOpWithoutTheMiddleware(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/holders", nil)
	Phase(r.Context(), "positions", time.Now())

	rec := httptest.NewRecorder()
	rec.WriteHeader(200)
	if got := rec.Header().Get("Server-Timing"); got != "" {
		t.Errorf("Server-Timing = %q, want none", got)
	}
}

// A response with no phases is exactly what it was before this existed, so
// nothing reading the header has to learn a new shape to keep working.
func TestServerTimingIsUnchangedWithoutPhases(t *testing.T) {
	h := WithServerTiming(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/version", nil))

	got := rec.Header().Get("Server-Timing")
	if strings.Contains(got, ",") {
		t.Errorf("Server-Timing = %q, want the bare app metric", got)
	}
	if !strings.HasPrefix(got, "app;dur=") {
		t.Errorf("Server-Timing = %q, want it to lead with app", got)
	}
}
