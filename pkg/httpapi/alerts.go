package httpapi

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gnoverse/gnoscope/pkg/store"
)

// Alerts: a short list of conditions worth a human's attention, each stated as a
// rule in words with its thresholds, each saying whether it is firing right now
// and showing the evidence when it is.
//
// There is no subscription. This explorer has no accounts, so nothing can be
// pushed to anyone; the page and the JSON are the interface. Thresholds are
// constants in this file and printed in every response, because an alert whose
// rule cannot be read is an assertion.
const (
	alertFailureMinFailed = 10 // failed calls in the last hour
	alertFailureFactor    = 2.0
	alertTransferGNOT     = 1_000_000
	ugnotPerGNOT          = 1_000_000
)

// AlertEvidence is one line backing a firing alert.
type AlertEvidence struct {
	Text string `json:"text"`
	// Href is an in-app path, when the evidence is something to open.
	Href string `json:"href,omitempty"`
}

// Alert is one rule's verdict.
type Alert struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Rule is the condition, thresholds included.
	Rule   string `json:"rule"`
	Firing bool   `json:"firing"`
	// Unread is set instead of Firing when the rule could not be evaluated,
	// which is not the same as it being quiet.
	Unread   string          `json:"unread,omitempty"`
	Evidence []AlertEvidence `json:"evidence,omitempty"`
}

type alertsResponse struct {
	Network string  `json:"network"`
	Checked string  `json:"checked"`
	Alerts  []Alert `json:"alerts"`
}

func failureRate(t store.FailureTotals) float64 {
	if t.Calls == 0 {
		return 0
	}
	return float64(t.Failed) / float64(t.Calls)
}

// evalFailureSpike fires when the last hour reverted at least
// alertFailureMinFailed calls and at more than alertFailureFactor times the rate
// of the 23 hours before it. Both conditions: a quiet hour with 3 of 4 calls
// failing is not a spike, and a busy chain failing at its usual 1% is not
// either. The baseline excludes the hour itself: measured against a full day
// that contains it, a spike lifts its own baseline and can hide behind it.
func evalFailureSpike(hour, before store.FailureTotals) Alert {
	al := Alert{
		ID:    "failure-spike",
		Title: "reverts are spiking",
		Rule: fmt.Sprintf("the last hour has at least %d failed calls and a failure rate more than %.0fx that of the 23 hours before it",
			alertFailureMinFailed, alertFailureFactor),
	}
	hr, dr := failureRate(hour), failureRate(before)
	al.Firing = hour.Failed >= alertFailureMinFailed && hr > alertFailureFactor*dr
	if al.Firing {
		al.Evidence = []AlertEvidence{{
			Text: fmt.Sprintf("%d of %d calls failed in the last hour (%.1f%%), against %.1f%% in the 23 hours before",
				hour.Failed, hour.Calls, 100*hr, 100*dr),
			Href: "/failed?window=1h",
		}}
	}
	return al
}

func (a *API) HandleAlerts(w http.ResponseWriter, r *http.Request) {
	network := a.singleNetwork(r)
	if network == "" {
		jsonError(w, "no network configured", 404)
		return
	}
	now := time.Now().UTC()
	day := now.Add(-24 * time.Hour).Format(time.RFC3339)
	hour := now.Add(-time.Hour).Format(time.RFC3339)
	out := alertsResponse{Network: network, Checked: now.Format(time.RFC3339), Alerts: []Alert{}}

	spike := Alert{ID: "failure-spike", Title: "reverts are spiking"}
	fh, errH := a.db.GetFailures(store.FailureParams{Network: network, Since: hour, Limit: 1})
	fd, errD := a.db.GetFailures(store.FailureParams{Network: network, Since: day, Limit: 1})
	if errH != nil || errD != nil {
		spike.Unread = "the failure counts could not be read"
	} else {
		spike = evalFailureSpike(fh.Current, store.FailureTotals{
			Calls:  fd.Current.Calls - fh.Current.Calls,
			Failed: fd.Current.Failed - fh.Current.Failed,
		})
	}
	out.Alerts = append(out.Alerts, spike)

	big := Alert{
		ID:    "large-transfer",
		Title: "a large native transfer",
		Rule:  fmt.Sprintf("a successful send of at least %s GNOT in the last 24 hours", fmtGNOTInt(alertTransferGNOT)),
	}
	sends, err := a.db.LargeSends(network, day, alertTransferGNOT*ugnotPerGNOT, 5)
	if err != nil {
		big.Unread = "the transfers could not be read"
	} else if len(sends) > 0 {
		big.Firing = true
		for _, s := range sends {
			big.Evidence = append(big.Evidence, AlertEvidence{
				Text: fmt.Sprintf("%s GNOT from %s to %s", fmtGNOTInt(s.Ugnot/ugnotPerGNOT), shortAddr(s.From), shortAddr(s.To)),
				Href: "/tx/" + s.TxHash,
			})
		}
	}
	out.Alerts = append(out.Alerts, big)

	JSONResponse(w, out)
}

// fmtGNOTInt writes a whole number with thousands separators.
func fmtGNOTInt(n int64) string {
	s := fmt.Sprintf("%d", n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
