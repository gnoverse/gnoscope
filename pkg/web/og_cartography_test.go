package web

import (
	"image/jpeg"
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// The Go table is a copy of VIEWS in cartography.js, so the two are compared
// here rather than trusted: a view added to the page without a row here would
// preview as the default view, which is a wrong picture, not a missing one.
func TestCartographyViewsMatchTheFrontend(t *testing.T) {
	src, err := fs.ReadFile(frontendFS, "frontend/cartography.js")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`\{ id: '([a-z]+)',\s+name: '([^']+)',\s+blurb: '([^']+)'`)
	got := re.FindAllStringSubmatch(string(src), -1)
	if len(got) != len(cartographyViews) {
		t.Fatalf("cartography.js has %d views, the Go table %d", len(got), len(cartographyViews))
	}
	for i, m := range got {
		want := cartographyViews[i]
		if m[1] != want.ID || m[2] != want.Name || m[3] != want.Blurb {
			t.Errorf("view %d: js {%s %q %q}, go {%s %q %q}", i, m[1], m[2], m[3], want.ID, want.Name, want.Blurb)
		}
	}
	if !regexp.MustCompile(`view: '` + cartographyDefaultView + `',`).Match(src) {
		t.Errorf("cartography.js does not open on %s, which is what the previews assume", cartographyDefaultView)
	}
}

func TestEveryCartographyViewHasAPreview(t *testing.T) {
	for _, v := range cartographyViews {
		f, err := frontendFS.Open("frontend/cartography-og/" + v.ID + ".jpg")
		if err != nil {
			t.Errorf("%s has no preview: %v", v.ID, err)
			continue
		}
		cfg, err := jpeg.DecodeConfig(f)
		f.Close()
		if err != nil {
			t.Errorf("%s preview is not a jpeg: %v", v.ID, err)
			continue
		}
		if cfg.Width != 1200 || cfg.Height != 630 {
			t.Errorf("%s preview is %dx%d, cards want 1200x630", v.ID, cfg.Width, cfg.Height)
		}
	}
}

func TestCartographyDocumentCarriesItsViewsPicture(t *testing.T) {
	body := realmDoc(t, Options{}, "/cartography?v=skyline&network=mainnet")
	for _, want := range []string{
		`property="og:image" content="https://gnoscope.example/cartography-og/skyline.jpg"`,
		`property="og:url" content="https://gnoscope.example/cartography?network=mainnet&amp;v=skyline"`,
		`property="og:title" content="skyline · cartography on gnoscope"`,
		`name="twitter:card" content="summary_large_image"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the skyline document is missing %s", want)
		}
	}
}

// ?v= only ever selects a row: anything else is the default view, and nothing
// from the query reaches the document.
func TestCartographyUnknownViewIsTheDefaultAndNeverEchoed(t *testing.T) {
	body := realmDoc(t, Options{}, `/cartography?v=%22%3E%3Cscript%3Ex`)
	if !strings.Contains(body, "/cartography-og/"+cartographyDefaultView+".jpg") {
		t.Errorf("an unknown view should preview as %s", cartographyDefaultView)
	}
	if strings.Contains(body, "<script>x") || strings.Contains(body, `"><script`) {
		t.Errorf("the query string reached the document")
	}
}
