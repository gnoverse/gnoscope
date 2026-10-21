package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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
	for _, want := range []string{"app;dur=", "positions;dur=1", "prices;dur=3"} {
		if !strings.Contains(got, want) {
			t.Errorf("Server-Timing %q carries no %q", got, want)
		}
	}
	// app stays first and parseable: the traffic store reads it out of this
	// same header and a phase in front of it would be read instead.
	if !strings.HasPrefix(got, "app;dur=") {
		t.Errorf("Server-Timing %q does not lead with app", got)
	}
	if ms := appDurationMS(got); ms <= 0 {
		t.Errorf("appDurationMS(%q) = %v, want the app metric back", got, ms)
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
