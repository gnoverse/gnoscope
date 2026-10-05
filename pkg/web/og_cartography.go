package web

import (
	"bytes"
	"html"
	"net/http"
	"net/url"
)

// Link previews for /cartography.
//
// A cartography link is a picture by definition, so a card without one says
// nothing. The pictures are not captured live: gnoshot photographs realms and
// a short allowlist of app sites, and a drawing that paints in JavaScript over
// three API calls is neither. Instead each view ships a 1200x630 snapshot in
// the binary, rendered from mainnet by e2e/tools/cartography-og.mjs and
// committed with the date in its alt text, so a preview never claims to be
// today's chain.

// cartographyView is one entry of VIEWS in frontend/cartography.js.
// TestCartographyViewsMatchTheFrontend fails the build when the two drift, and
// TestEveryCartographyViewHasAPreview when a view has no picture.
type cartographyView struct {
	ID, Name, Blurb string
}

var cartographyViews = []cartographyView{
	{"city", "city", "districts by namespace, one building per package, storeys by activity"},
	{"settlement", "settlement", "villages and the people walking between them, from real caller counts"},
	{"orbits", "orbits", "one solar system per namespace, orbit radius by deploy date"},
	{"metro", "metro", "the import graph as transit lines, interchanges where code is shared"},
	{"relief", "relief", "a contour map of where the chain is dense, owner-blind"},
	{"metropolis", "metropolis", "a downtown by land value, owner-blind, zoned by what each package does"},
	{"boroughs", "boroughs", "one equal block per namespace, the busiest at the centre"},
	{"hexes", "hexes", "a board of equal hexes, neighbours by imports, terrain by yield"},
	{"frontier", "frontier", "deployers as players, settled outward from (0|0) in deploy order"},
	{"oldtown", "old town", "a walled town grown ring by ring, one wall per era of deploys"},
	{"honeycomb", "honeycomb", "one cell per package, spiralling out from the queen in rank order"},
	{"skyline", "skyline", "the chain side on at night, one tower per package, across the water"},
	{"archipelago", "archipelago", "namespaces as islands by size, packed by trade, imports as ferries"},
	{"lights", "night lights", "the chain from orbit at night: owner-blind, glowing by the metric"},
}

// cartographyDefaultView is what /cartography opens on with no ?v=, and must
// agree with the default in cartography.js.
const cartographyDefaultView = "metropolis"

// cartographyPreviewDate is when the committed snapshots were rendered.
const cartographyPreviewDate = "2026-10-05"

// cartographyViewFromRequest names the view a /cartography URL opens on. An
// unknown ?v= is the default view, which is also what the page itself does.
func cartographyViewFromRequest(r *http.Request) (cartographyView, bool) {
	if r.URL.Path != "/cartography" {
		return cartographyView{}, false
	}
	want := r.URL.Query().Get("v")
	var def cartographyView
	for _, v := range cartographyViews {
		if v.ID == want {
			return v, true
		}
		if v.ID == cartographyDefaultView {
			def = v
		}
	}
	return def, true
}

// cartographyTags is ogTags' counterpart for a drawing. Every value is from
// the constant table above or built through url.URL, never from the request
// verbatim: ?v= only ever selects a row.
func cartographyTags(origin string, v cartographyView, network string) []byte {
	title := v.Name + " · cartography on gnoscope"
	desc := "gno.land drawn as a place: " + v.Blurb + "."

	var b bytes.Buffer
	b.WriteString("<!-- Per-view link preview for /cartography. -->\n")
	meta := func(attr, key, value string) {
		b.WriteString(`<meta ` + attr + `="` + key + `" content="` + html.EscapeString(value) + `">` + "\n")
	}
	meta("property", "og:type", "website")
	meta("property", "og:site_name", "gnoscope")
	meta("property", "og:title", title)
	meta("property", "og:description", desc)
	meta("name", "description", desc)
	if u, err := url.Parse(origin); origin != "" && err == nil {
		q := url.Values{}
		q.Set("v", v.ID)
		if network != "" {
			q.Set("network", network)
		}
		u.Path = "/cartography"
		u.RawQuery = q.Encode()
		meta("property", "og:url", u.String())

		img := *u
		img.Path = "/cartography-og/" + v.ID + ".jpg"
		img.RawQuery = ""
		meta("property", "og:image", img.String())
		meta("property", "og:image:width", "1200")
		meta("property", "og:image:height", "630")
		meta("property", "og:image:alt", "The "+v.Name+" drawing of gno.land mainnet, as of "+cartographyPreviewDate)
		meta("name", "twitter:card", "summary_large_image")
		meta("name", "twitter:image", img.String())
	} else {
		meta("name", "twitter:card", "summary")
	}
	meta("name", "twitter:title", title)
	meta("name", "twitter:description", desc)
	return b.Bytes()
}
