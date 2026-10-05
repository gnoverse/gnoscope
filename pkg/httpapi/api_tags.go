package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gnoverse/gnoscope/pkg/store"
	"github.com/gnoverse/gnoscope/pkg/tags"
)

// Code-derived tags on the API: what each package's source does, several per
// package, each with the line that earned it (see pkg/tags). They ride on the
// payloads that already list packages (the code tree, the timeline, the realm
// page, the realm and package listings), every one of them filterable with
// tag=, and GET /api/tags says which tags a chain has and what each one checks.

// TagsPath is the route.
const TagsPath = "/api/tags"

// tagParam reads tag= and refuses one the rule table does not have, so a typo
// says so rather than answering with an empty list that reads as "nothing on
// this chain does that".
func tagParam(r *http.Request) (string, string) {
	t := strings.TrimSpace(r.URL.Query().Get("tag"))
	if t == "" || tags.Known(t) {
		return t, ""
	}
	names := make([]string, len(tags.Rules))
	for i, rule := range tags.Rules {
		names[i] = rule.Tag
	}
	return "", "unknown tag " + strconv.Quote(t) + ": want one of " + strings.Join(names, ", ")
}

// stampTags puts each row's tags on it, read per network because a path
// names a different package on every chain. Best-effort: a row whose tags
// cannot be read is left without, which reads as untagged, never as wrong.
func (a *API) stampTags(items []store.PackageInfo) {
	byNet := map[string][]string{}
	for _, it := range items {
		byNet[it.Network] = append(byNet[it.Network], it.Path)
	}
	got := map[string]map[string][]tags.Tag{}
	for net, paths := range byNet {
		if m, err := a.db.PackageTags(net, paths); err == nil {
			got[net] = m
		}
	}
	for i := range items {
		items[i].Tags = got[items[i].Network][items[i].Path]
	}
}

type tagInfo struct {
	Tag      string `json:"tag"`
	Means    string `json:"means"`
	Packages int    `json:"packages"`
	Realms   int    `json:"realms"`
	Pure     int    `json:"pure"`
}

// HandleTags lists every tag in the rule table with what it checks and how
// many packages on the network carry it, rule order, zeros included so a
// reader can see a rule exists and matched nothing. With path=, it answers
// one package's tags and their evidence instead.
//
// GET /api/tags?network=<id>[&path=gno.land/r/...]
func (a *API) HandleTags(w http.ResponseWriter, r *http.Request) {
	network := a.networkParam(r)
	if network == "" {
		jsonError(w, "tags are per-chain: add ?network=", http.StatusBadRequest)
		return
	}
	if p := strings.TrimSpace(r.URL.Query().Get("path")); p != "" {
		m, err := a.db.PackageTags(network, []string{p})
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		ts := m[p]
		if ts == nil {
			ts = []tags.Tag{}
		}
		JSONResponse(w, map[string]any{"network": network, "path": p, "rules": tags.Version, "tags": ts})
		return
	}
	counts, err := a.db.TagCounts(network, store.PackageFilter{})
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	by := map[string]store.TagCount{}
	for _, c := range counts {
		by[c.Tag] = c
	}
	out := make([]tagInfo, 0, len(tags.Rules))
	for _, rule := range tags.Rules {
		c := by[rule.Tag]
		out = append(out, tagInfo{Tag: rule.Tag, Means: rule.Means, Packages: c.Packages, Realms: c.Realms, Pure: c.Pure})
	}
	JSONResponse(w, map[string]any{"network": network, "rules": tags.Version, "tags": out})
}
