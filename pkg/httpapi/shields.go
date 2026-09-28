package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gnoverse/gnoscope/pkg/badge"
)

// One-line badges: the shields.io shape, answering questions about a realm.
//
// The card routes in badges.go draw a graph, which is a figure in a document.
// This is the other half: the 20-pixel plate a README carries in a row at the
// top, beside the CI badge and the licence badge. Same reader, different
// question — "is this thing alive and how much is it used" answered before the
// first paragraph, rather than in one.
//
// Two ways to get it, deliberately:
//
//   - GET /_badges/shield/{kind}/{path...} draws the SVG here. One hop, no
//     third party, works for anyone who can reach this host.
//   - GET /api/shield/{kind}/{path...} answers shields.io's endpoint schema,
//     so https://img.shields.io/endpoint?url=… renders the same numbers in
//     shields' own pipeline. That is the one to use when you want a style this
//     renderer does not draw (for-the-badge, social), a named logo, or when
//     you would rather one CDN served every badge on the page.
//
// Both go through shieldFor, so the two can never disagree about what a
// number means.

// The kinds. Kept short and written out, rather than derived from a metric
// registry: each one is a URL people paste into a README and then never touch
// again, so the set is a compatibility surface, not an implementation detail.
const (
	// shieldKindStatus is the chain's own answer: is there code at this path,
	// and is it callable.
	shieldKindStatus = "status"
	// shieldKindTxs counts distinct transactions; shieldKindMessages counts
	// the messages inside them.
	shieldKindTxs      = "txs"
	shieldKindMessages = "messages"
	// shieldKindUsers is distinct calling addresses.
	shieldKindUsers = "users"
	// shieldKindVersion is how many accepted submissions the path has had.
	shieldKindVersion = "version"
)

// shieldMaxDays bounds ?days=. A badge with no window says "since the
// beginning", which is the default, so the window is always an explicit ask.
const shieldMaxDays = 365

// HandleBadgeShield draws one answer as an SVG.
func (a *API) HandleBadgeShield(w http.ResponseWriter, r *http.Request) {
	s, errMsg := a.shieldFor(r)
	if errMsg != "" {
		// An error shield, not the error card badgeError draws: a 480x120 card
		// appearing where a 20-pixel plate was expected does not read as "this
		// badge is wrong", it reads as the page being broken.
		writeBadgeError(w, errMsg, badge.RenderShield(badge.Shield{
			Label: "gnoscope", Message: errMsg, Color: "critical",
			Style: badge.ParseShieldStyle(r.URL.Query().Get("style")),
		}))
		return
	}
	writeBadge(w, r, badge.RenderShield(s))
}

// HandleShieldEndpoint answers the same question in shields.io's endpoint
// schema (https://shields.io/badges/endpoint-badge).
//
// Status 200 on failure too, and isError instead: shields treats a non-200 as
// "the endpoint is down" and draws its own generic error, discarding a message
// that said which path was not found.
func (a *API) HandleShieldEndpoint(w http.ResponseWriter, r *http.Request) {
	s, errMsg := a.shieldFor(r)
	if errMsg != "" {
		JSONResponse(w, map[string]any{
			"schemaVersion": 1,
			"label":         "gnoscope",
			"message":       errMsg,
			"color":         "critical",
			"isError":       true,
		})
		return
	}
	JSONResponse(w, map[string]any{
		"schemaVersion": 1,
		"label":         s.Label,
		"message":       s.Message,
		"color":         s.Color,
		// Shields caches an endpoint's answer for what the endpoint asks for,
		// floored at 300s. Matching writeBadge's own max-age keeps one
		// freshness story however the badge is being served.
		"cacheSeconds": 300,
	})
}

// shieldFor resolves a request into the badge it asked for, or a message
// saying why it could not.
//
// The message is returned rather than written because the two routes above
// render a failure differently, and because every failure here is a sentence a
// human typed something wrong to earn: an unknown kind, a path with a typo, a
// network that is not configured.
func (a *API) shieldFor(r *http.Request) (badge.Shield, string) {
	q := r.URL.Query()
	style := badge.ParseShieldStyle(q.Get("style"))

	network, ok := a.badgeNetwork(r)
	if !ok {
		return badge.Shield{}, "unknown network"
	}

	kind := strings.ToLower(strings.TrimSuffix(r.PathValue("kind"), ".svg"))
	// The .svg suffix is optional on the path as well as on the kind, because
	// which segment ends up last depends on whether the badge takes a path at
	// all.
	raw := strings.TrimSuffix(strings.TrimRight(r.PathValue("path"), "/"), ".svg")
	path := "gno.land/" + raw

	label, message, color, errMsg := a.shieldData(r, kind, network, path)
	if errMsg != "" {
		return badge.Shield{}, errMsg
	}

	// Overrides come last and are not validated beyond escaping and the
	// colour table: a badge in a README is somebody else's document, and
	// "calls" instead of "txs" is their call to make.
	if v := q.Get("label"); v != "" {
		label = v
	}
	if v := q.Get("color"); v != "" {
		color = v
	}

	return badge.Shield{
		Label: label, Message: message, Color: color,
		LabelColor: q.Get("labelColor"),
		Style:      style,
	}, ""
}

// shieldData is where each kind's number comes from.
func (a *API) shieldData(r *http.Request, kind, network, path string) (label, message, color, errMsg string) {
	if kind == shieldKindStatus {
		label, message, color = a.shieldStatus(r, network, path)
		return label, message, color, ""
	}

	// The kind is checked before anything is read, so a URL with a typo in the
	// *kind* is told that, rather than being told about its path.
	label, ok := shieldCountLabels[kind]
	if !ok {
		return "", "", "", "unknown badge: " + kind
	}

	days := shieldDays(r)
	var since string
	if days > 0 {
		since = time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)
	}

	// Everything below is a count out of the index, and the index is the only
	// thing that can answer: a chain holds what a realm is, not how often it
	// has been called.
	stats, err := a.db.RealmBadgeStats(network, path, since)
	if err != nil {
		// The only error this returns is the path not resolving in the index,
		// and there are three quite different reasons for that. Ask the chain
		// which one it is: writing a badge into a README before the realm is
		// deployed is a normal thing to do, and "no such package" under it
		// reads as a typo the author would then go hunting for.
		return shieldMissing(label, a.chainStatus(r, network, path))
	}

	// A window is named on the badge rather than assumed. "1,234 txs" and
	// "1,234 txs this month" are answers to different questions and a reader
	// cannot tell them apart from the number.
	if days > 0 && kind != shieldKindVersion {
		label += " (" + strconv.Itoa(days) + "d)"
	}

	switch kind {
	case shieldKindTxs:
		return label, humanInt(stats.Txs), countColor(stats.Txs), ""
	case shieldKindMessages:
		return label, humanInt(stats.Messages), countColor(stats.Messages), ""
	case shieldKindUsers:
		return label, humanInt(stats.UniqueCallers), countColor(stats.UniqueCallers), ""
	}
	// gno has no version field, so the honest answer is which release this is:
	// the Nth accepted submission at the path. A package the index holds with
	// no submission of its own arrived in genesis, and "r0" would be a number
	// rather than that fact.
	if stats.Deploys == 0 {
		return label, "genesis", "lightgrey", ""
	}
	return label, "r" + strconv.Itoa(stats.Deploys), "blue", ""
}

// shieldCountLabels is both the set of kinds that count something and what
// each one is called on the badge. One map rather than a switch with a default
// label, so a kind cannot exist without a name or be named without existing.
var shieldCountLabels = map[string]string{
	shieldKindTxs:      "txs",
	shieldKindMessages: "messages",
	shieldKindUsers:    "users",
	shieldKindVersion:  "version",
}

// shieldMissing explains a path the index does not hold, in the chain's terms.
//
// A count is not printable here whatever the answer: zero would be a number,
// and a number is something a reader believes. What the badge can do is say
// which of the three cases it is, in a word the author can act on.
func shieldMissing(label, status string) (_, message, color, errMsg string) {
	switch status {
	case PackageStatusLive:
		// On the chain but not in the index: deployed within the sync loop's
		// last pass, or the syncer is behind. Either way nothing has been
		// indexed calling it, and zero is the honest count.
		return label, "0", "lightgrey", ""
	case PackageStatusInert:
		return label, "parked", "yellow", ""
	case PackageStatusAbsent:
		return label, "not deployed", "lightgrey", ""
	}
	// The chain could not be asked either, so the index's own answer stands.
	return "", "", "", "no such package"
}

// chainStatus reads one path's current status off the chain, or "" if it could
// not be asked.
func (a *API) chainStatus(r *http.Request, network, path string) string {
	meta, err := fetchPackageMeta(r.Context(), a.rpcURLFor(network), path)
	if err != nil {
		return ""
	}
	return meta.Status
}

// shieldStatus asks the chain what is at the path, right now.
//
// Live RPC rather than the index, because the question is about the present
// and because "absent" has to be answerable for a path the index has never
// heard of — a badge in the README of a realm that is not deployed yet is
// exactly the case this kind exists for.
func (a *API) shieldStatus(r *http.Request, network, path string) (label, message, color string) {
	label = "package"
	if strings.HasPrefix(path, "gno.land/r/") {
		label = "realm"
	}

	switch a.chainStatus(r, network, path) {
	case PackageStatusLive:
		return label, "live", "brightgreen"
	case PackageStatusInert:
		// "parked" rather than the chain's own "inert": docs/glossary.md is
		// what the product says to readers, and a README is the furthest out
		// any of this travels.
		return label, "parked", "yellow"
	case PackageStatusAbsent:
		return label, "absent", "lightgrey"
	}
	// An RPC that did not answer is not an absent package: saying "absent"
	// would tell a reader their realm had been removed because a node was
	// restarting.
	return label, "unknown", "lightgrey"
}

// countColor greys out a zero.
//
// A blue "0" reads as a measurement that has been taken and is fine; grey
// reads as nothing to report, which is what a realm nobody has called yet is.
func countColor(n int) string {
	if n <= 0 {
		return "lightgrey"
	}
	return "blue"
}

// shieldDays reads ?days=, where unset means all of history.
//
// Different default from badgeDays on purpose: a card draws a series and needs
// a window to draw it over, while "1,234 txs" with no window is the number a
// README wants most of the time.
func shieldDays(r *http.Request) int {
	v, err := strconv.Atoi(r.URL.Query().Get("days"))
	if err != nil || v <= 0 {
		return 0
	}
	if v > shieldMaxDays {
		return shieldMaxDays
	}
	return v
}
