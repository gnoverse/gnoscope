package httpapi

import (
	"net/http"
	"time"

	"github.com/gnoverse/gnoscope/pkg/store"
)

type failuresResponse struct {
	Network string          `json:"network,omitempty"`
	Window  pulseWindowMeta `json:"window"`
	*store.Failures
}

// HandleFailures answers "what is reverting, and is it more than usual": the
// reverted calls of a window against the window before it. It shares the pulse's
// windows on purpose, so a reader who picked "7d" on the home page sees the same
// seven days here.
func (a *API) HandleFailures(w http.ResponseWriter, r *http.Request) {
	network := a.networkParam(r)
	win := resolvePulseWindow(r.URL.Query().Get("window"))

	now := time.Now().UTC()
	since := now.Add(-win.D)
	prevSince := since.Add(-win.D)

	f, err := a.db.GetFailures(store.FailureParams{
		Network:   network,
		Since:     since.Format(time.RFC3339),
		PrevSince: prevSince.Format(time.RFC3339),
		Limit:     pulseLimit(r),
	})
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}

	options := make([]pulseWindowOption, 0, len(pulseWindows))
	for _, o := range pulseWindows {
		options = append(options, pulseWindowOption{Key: o.Key, Label: o.Label})
	}
	JSONResponse(w, failuresResponse{
		Network: network,
		Window: pulseWindowMeta{
			Key: win.Key, Label: win.Label, Seconds: int(win.D / time.Second),
			Since: since.Format(time.RFC3339), Until: now.Format(time.RFC3339),
			PrevSince: prevSince.Format(time.RFC3339), Options: options,
		},
		Failures: f,
	})
}
