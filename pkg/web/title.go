package web

import (
	"html"
	"regexp"
	"strings"
)

// Page titles.
//
// Every document used to say `<title>gnoscope</title>` and nothing ever set
// document.title, so every tab, bookmark and history entry on the site read
// the same word. A title is what a screen reader announces on arrival and what
// a chat window shows when a link is pasted, so it leads with the thing:
// `gno.land/r/gov/dao on gnoscope`, `by realm · gas on gnoscope`.
//
// Written twice, here for the document and in pageTitle() in index.html for
// every navigation after it, because the SPA routes without asking the server.
// Both read the rail's labels (here from the rail markup, there from NAV, which
// TestRailMatchesNavTable already keeps identical), and
// e2e/tests/title.spec.js loads each shape of URL and fails if the title the
// server wrote is not the one the client sets.

// siteName closes every title but the home page's, which is only this.
const siteName = "gnoscope"

// maxTitleThing bounds the part of a title taken from the URL. A transaction
// hash or a realm path fits comfortably; a pasted URL a kilobyte long would
// otherwise become a kilobyte of tab label.
const maxTitleThing = 120

// titleOf is the one format: the thing, then where it is.
func titleOf(thing string) string {
	if thing == "" {
		return siteName
	}
	if r := []rune(thing); len(r) > maxTitleThing {
		thing = string(r[:maxTitleThing]) + "…"
	}
	return thing + " on " + siteName
}

// railTitles maps each rail path to the thing its title names: the rail label,
// with its section after it when it is a child, since "by realm" or "options"
// alone does not say of what. home's children are not suffixed: "analytics ·
// home" says nothing "analytics" does not.
func railTitles(index []byte) map[string]string {
	rail := string(index)
	if i := strings.Index(rail, "<nav>"); i >= 0 {
		rail = rail[i:]
	}
	if i := strings.Index(rail, "</nav>"); i >= 0 {
		rail = rail[:i]
	}
	out := map[string]string{}
	section, inKids := "", false
	for _, m := range railTokenRe.FindAllStringSubmatch(rail, -1) {
		switch m[0] {
		case `<div class="nav-kids">`:
			inKids = true
		case "</div>":
			inKids = false
		default:
			path, id, label := m[1], m[2], m[3]
			if !inKids {
				section = label
				if id == "home" {
					section = ""
				}
				if path != "/" {
					out[path] = label
				}
				continue
			}
			if section == "" {
				out[path] = label
			} else {
				out[path] = label + " · " + section
			}
		}
	}
	return out
}

var railTokenRe = regexp.MustCompile(`<div class="nav-kids">|</div>|<a href="([^"]+)" id="nav-([A-Za-z0-9-]+)"[^>]*>.*?<span class="rail-label">([^<]+)</span></a>`)

// titleAliases are paths that render a rail entry's view without being its
// path. route() in index.html has the reason for each.
var titleAliases = map[string]string{
	"/grc721": "/nfts",
	"/tokens": "/grc20",
	"/params": "/govdao/params",
}

// pageThing names what a path shows, or "" for the home page and anything the
// app does not route. Detail pages mirror route()'s own prefixes.
func pageThing(path string, rail map[string]string) string {
	if t, ok := rail[path]; ok {
		return t
	}
	if to, ok := titleAliases[path]; ok {
		return rail[to]
	}
	cut := func(prefix string) (string, bool) {
		rest, ok := strings.CutPrefix(path, prefix)
		return rest, ok && rest != ""
	}
	if p := realmPathFromURL(path); p != "" {
		return p
	}
	if rest, ok := cut("/tx/"); ok {
		return "tx " + rest
	}
	if rest, ok := cut("/block/"); ok {
		if n := leadingDigits(rest); n != "" {
			return "block " + n
		}
		return ""
	}
	if rest, ok := cut("/address/"); ok {
		return rest
	}
	if rest, ok := cut("/validator/"); ok {
		return rest
	}
	if rest, ok := cut("/grc20/"); ok {
		return rest
	}
	if rest, ok := cut("/stdlib/"); ok {
		return rest + " · stdlib"
	}
	if rest, ok := cut("/code/"); ok {
		return rest + " · code"
	}
	if strings.HasPrefix(path, "/gnohub/") {
		return rail["/gnohub"]
	}
	if rest, ok := cut("/govdao/"); ok {
		if n := leadingDigits(rest); n != "" {
			return "proposal " + n
		}
		return ""
	}
	if path == "/search" || strings.HasPrefix(path, "/search/") {
		return "search"
	}
	return ""
}

// leadingDigits is parseInt's reading of a path segment, which is what route()
// uses for a block height and a proposal id.
func leadingDigits(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	s = strings.TrimLeft(s[:i], "0")
	if i > 0 && s == "" {
		return "0"
	}
	return s
}

// titleTag is the <title> element for a title, escaped: most of what goes into
// one came off the URL, and some of that came off the chain.
func titleTag(title string) string {
	return "<title>" + html.EscapeString(title) + "</title>\n"
}
