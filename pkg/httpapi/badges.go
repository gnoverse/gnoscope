package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	"github.com/gnoverse/gnoscope/pkg/badge"
)

// Embeddable badges: the explorer's answers, as images other documents carry.
//
// A gno realm's Render() returns markdown and gnoweb turns it into HTML, so an
// `![](…)` is the only hook a realm has into anything the chain does not store.
// Point one at a route here and a realm page can show its own usage graph,
// drawn from the index, without the chain holding a single byte of it.
//
// Two things about the reader make this different from the rest of the API:
//
//   - It is an <img>, so it cannot read a status code or a JSON error. A
//     failure has to arrive as a picture that says what went wrong, or it
//     arrives as a broken-image icon that says nothing. Hence errorBadge and
//     the 200 it is served with (see badgeError).
//   - It is embedded, so it is fetched every time the embedding page is read,
//     by readers who never visit gnoscope. That makes the cache headers part
//     of the feature rather than a nicety, and it is why /_badges/ is in
//     cacheable().
//
// The routes live under /_badges/ rather than /api/: the leading underscore
// keeps them out of the namespace a package path could ever occupy, and the
// SPA's catch-all "GET /" loses to a more specific pattern under Go's mux.

// BadgePrefix is the one place the route prefix is written. cacheable() reads
// it too, so the response cache and the routes cannot disagree about what a
// badge URL looks like.
const BadgePrefix = "/_badges/"

// Badge windows. The floor is 2 because a one-point series is not a line and
// the renderer refuses to draw it as one; the ceiling is a year because the
// card is 452 pixels wide and a longer window puts several buckets on the same
// column, which reads as noise rather than as history.
const (
	badgeDefaultDays = 30
	badgeMinDays     = 2
	badgeMaxDays     = 365
)

// badgeGranularity picks a bucket size that keeps the point count in the range
// a 452-pixel-wide plot can actually distinguish.
//
// Not the analytics pages' resolveTimeseriesParams: that one sizes a window a
// reader can zoom and pan, and honours explicit ?granularity=. A badge has no
// controls, so the caller choosing "hourly over 365 days" would get 8,760
// points drawn 0.05 pixels apart and no way to tell that is what happened.
func badgeGranularity(days int) string {
	switch {
	case days <= 3:
		return "hourly"
	case days <= 120:
		return "daily"
	default:
		return "weekly"
	}
}

// badgeDays reads ?days=, clamped. Unparseable falls back to the default
// rather than erroring, the same way the API's other filters do: these URLs
// are written by hand into a realm's source and a typo should cost a different
// window, not a broken image on the realm's front page.
func badgeDays(r *http.Request) int {
	v, err := strconv.Atoi(r.URL.Query().Get("days"))
	if err != nil || v < badgeMinDays {
		if err == nil && v > 0 && v < badgeMinDays {
			return badgeMinDays
		}
		return badgeDefaultDays
	}
	if v > badgeMaxDays {
		return badgeMaxDays
	}
	return v
}

// HandleBadgeRealm draws one realm's activity over a window.
//
// `?metric=messages` (the default) is every message aimed at the realm, calls
// and MsgRuns alike. `?metric=callers` is the distinct addresses per bucket,
// which is a different shape entirely: one bot calling hourly draws a flat line
// of 1 on callers and a wall on messages, and a realm people actually use
// draws the opposite.
func (a *API) HandleBadgeRealm(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	network, ok := a.badgeNetwork(r)
	if !ok {
		badgeError(w, "unknown network", q.Get("network"))
		return
	}

	path := strings.TrimRight("gno.land/"+strings.TrimSuffix(r.PathValue("path"), ".svg"), "/")
	days := badgeDays(r)
	gran := badgeGranularity(days)

	pts, err := a.db.RealmActivitySeries(network, path, gran, days)
	if err != nil {
		// The only error this returns is the package lookup failing, which for
		// a hand-written URL is overwhelmingly a typo in the path rather than a
		// database fault. Saying which path was not found is the whole value of
		// answering with a picture.
		badgeError(w, "no such package", path)
		return
	}

	// The totals are read back from the same series the line is drawn from, not
	// from RealmUsage: a headline computed over all time beside a graph of the
	// last 30 days is two different questions printed as one sentence.
	var messages, peak int
	series := make([]badge.Point, len(pts))
	for i, p := range pts {
		v := p.Messages
		if q.Get("metric") == "callers" {
			v = p.Callers
		}
		messages += p.Messages
		if v > peak {
			peak = v
		}
		series[i] = badge.Point{Label: p.Time, Value: float64(v)}
	}

	headline, sub := badgeMessagesText(messages, peak, days, gran)
	if q.Get("metric") == "callers" {
		headline, sub = badgeCallersText(peak, days, gran)
	}

	writeBadge(w, r, badge.Render(badge.Card{
		Title:    path,
		Headline: headline,
		Sub:      sub,
		Series:   series,
		Empty:    "no activity in this window",
		Theme:    badge.ParseTheme(q.Get("theme")),
	}))
}

// badgeMessagesText writes the two text lines for the messages metric.
//
// Split out, and given the peak as well as the total, because the graph has no
// y-axis: without the peak printed, a line reaching the top of the card says
// only "this was the busiest bucket" and not how busy that was.
func badgeMessagesText(total, peak, days int, gran string) (headline, sub string) {
	return humanInt(total) + " messages",
		"peak " + humanInt(peak) + " per " + bucketNoun(gran) + " · last " + humanInt(days) + " days · gnoscope"
}

// badgeCallersText does the same for the callers metric.
//
// There is deliberately no total here. Distinct callers per bucket does not sum
// across buckets (an address that comes back on three days is in all three),
// so a "N callers" headline over a 30-day window would be a number nothing on
// the chain corresponds to.
func badgeCallersText(peak, days int, gran string) (headline, sub string) {
	return "peak " + plural(peak, "caller"),
		"distinct callers per " + bucketNoun(gran) + " · last " + humanInt(days) + " days · gnoscope"
}

func bucketNoun(gran string) string {
	switch gran {
	case "hourly":
		return "hour"
	case "weekly":
		return "week"
	default:
		return "day"
	}
}

// HandleBadgeNetwork draws chain-wide activity.
//
// `?metric=txs` (the default) counts every indexed message: calls, deploys,
// MsgRuns and bank sends, the same four GetTransactionTimeSeries reports and
// the same four the analytics page charts.
func (a *API) HandleBadgeNetwork(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	network, ok := a.badgeNetwork(r)
	if !ok {
		badgeError(w, "unknown network", q.Get("network"))
		return
	}
	// Unlike a realm badge there is no package to scope by, so an unset
	// ?network= would count every chain ever synced into one line, retired
	// testnets included. A badge cannot show a network picker, so it asks for
	// one instead of quietly picking.
	if network == "" {
		badgeError(w, "?network= is required", "one chain per badge")
		return
	}

	days := badgeDays(r)
	gran := badgeGranularity(days)

	pts, err := a.db.GetTransactionTimeSeries(network, gran, days)
	if err != nil {
		badgeError(w, "could not read the index", network)
		return
	}

	var total, peak int
	series := make([]badge.Point, len(pts))
	for i, p := range pts {
		v := p.Calls + p.Deploys + p.MsgRuns + p.Sends
		total += v
		if v > peak {
			peak = v
		}
		series[i] = badge.Point{Label: p.Time, Value: float64(v)}
	}

	writeBadge(w, r, badge.Render(badge.Card{
		Title:    network,
		Headline: plural(total, "message"),
		Sub:      "peak " + humanInt(peak) + " per " + bucketNoun(gran) + " · last " + humanInt(days) + " days · gnoscope",
		Series:   series,
		Empty:    "nothing indexed in this window",
		Theme:    badge.ParseTheme(q.Get("theme")),
	}))
}

// badgeNetwork resolves ?network= against the configured set.
//
// RejectUnknownNetwork guards /api/ and deliberately does not guard anything
// else, because a stale bookmark should still load the SPA. Badges need the
// check and cannot use that middleware's JSON 404, so they do it here: an
// unconfigured network reaching the store would be answered out of rows a
// retired testnet left behind, stamped with an unrelated chain's name.
func (a *API) badgeNetwork(r *http.Request) (string, bool) {
	n := r.URL.Query().Get("network")
	if n == "" || n == "all" {
		return "", true
	}
	for _, cfg := range a.networks {
		if cfg.ID == n {
			return n, true
		}
	}
	return "", false
}

// writeBadge serves an SVG with the headers an embedded image needs.
//
// A badge is fetched once per read of whatever embeds it, by readers who will
// never see gnoscope, so the caching is the difference between a realm page
// that paints and one that waits on a SQLite aggregate. max-age is five
// minutes because the syncer's own cadence puts a floor under how fresh this
// can be anyway; stale-while-revalidate lets a shared cache keep serving while
// it refreshes.
//
// The ETag is over the rendered bytes rather than over the query, so a window
// whose numbers did not move costs a 304 instead of a redraw.
//
// Content-Security-Policy is on the response, not only on whoever embeds it:
// at its own URL this is a top-level document on gnoscope's origin carrying
// strings that came off the chain. `default-src 'none'` means that even if the
// escaping in pkg/badge were wrong one day, there is nothing for injected
// markup to load or call.
func writeBadge(w http.ResponseWriter, r *http.Request, svg []byte) {
	sum := sha256.Sum256(svg)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`

	h := w.Header()
	h.Set("Content-Type", "image/svg+xml; charset=utf-8")
	h.Set("Cache-Control", "public, max-age=300, stale-while-revalidate=600")
	h.Set("ETag", etag)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	// Embedded by design: this is the one thing the app serves that is meant to
	// be loaded by a document on somebody else's origin.
	h.Set("Access-Control-Allow-Origin", "*")

	if match := r.Header.Get("If-None-Match"); match != "" && matchesETag(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Write(svg)
}

// matchesETag reports whether an If-None-Match header covers etag.
//
// A proxy may weaken a validator on the way through, and "*" is the wildcard
// the spec defines. Comparing the raw header against the tag would miss both
// and redraw a card the reader already has.
func matchesETag(inm, etag string) bool {
	for _, c := range strings.Split(inm, ",") {
		c = strings.TrimSpace(c)
		if c == "*" || c == etag || strings.TrimPrefix(c, "W/") == etag {
			return true
		}
	}
	return false
}

// badgeError answers with a card that says what went wrong.
//
// Status 200, deliberately. An <img> has no error channel: a browser handed a
// 404 fires onerror and paints the broken-image glyph, throwing away a body
// that could have said "no such package: gno.land/r/moul/hom". These URLs are
// typed by hand into realm source and read by people who cannot open a network
// tab, so the failure is worth a picture.
//
// X-Badge-Error and no-store are how the difference stays visible to everything
// that is not a browser: a monitor can alert on the header, and no cache stores
// a typo's answer for five minutes.
func badgeError(w http.ResponseWriter, msg, detail string) {
	// detail goes in the plot area, not the footer: with no series there is no
	// graph, and the renderer's own "not enough history to draw" is a sentence
	// about a graph nobody asked for. The footer says who is speaking.
	writeBadgeError(w, msg, badge.Render(badge.Card{
		Title:    "gnoscope",
		Headline: msg,
		Empty:    detail,
		Sub:      "badge error",
	}))
}

// writeBadgeError serves an already-drawn failure.
//
// Split from badgeError because the shield routes draw their failure in their
// own shape (a 480x120 card in a README's badge row reads as the page being
// broken, not as one badge being wrong), and the headers are the half that has
// to stay identical whatever the picture is.
func writeBadgeError(w http.ResponseWriter, msg string, svg []byte) {
	h := w.Header()
	h.Set("X-Badge-Error", msg)
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Type", "image/svg+xml; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	h.Set("Access-Control-Allow-Origin", "*")
	w.Write(svg)
}

// plural writes a count and its noun, agreeing.
//
// "1 messages" on the front page of a realm with exactly one call is the kind
// of detail that makes the rest of the number look unchecked.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return humanInt(n) + " " + noun + "s"
}

// humanInt groups digits in threes.
//
// Every number on a badge is a count a reader compares at a glance, and 1234567
// and 12345678 are indistinguishable at 20 pixels without separators.
func humanInt(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	if len(s) <= 3 {
		if neg {
			return "-" + s
		}
		return s
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
