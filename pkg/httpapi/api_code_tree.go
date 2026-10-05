package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gnoverse/gnoscope/pkg/discover"
	"github.com/gnoverse/gnoscope/pkg/store"
)

// CodeTreePath is the route of the code tree. The response cache reads it to
// give the tree its own TTL (see endpointTTL).
const CodeTreePath = "/api/code/tree"

// codeTreeWindowDays is how far back the activity numbers look.
const codeTreeWindowDays = 30

// codeTreeNow is the clock the activity window is anchored to. A variable so
// a test can pin it.
var codeTreeNow = time.Now

// HandleCodeTree serves every deployed package on one network as one compact
// payload: enough to draw a file tree with fuzzy jump and a treemap of all
// the code on the chain, without a request per package.
//
// Field names are short because the list is the whole chain. Measured on
// gnoland1 on 2026-10-03 (598 packages, 3,173 files): 171 KB with these keys
// against 306 KB spelled out, which is what a browser parses and holds. On the
// wire gzip takes most of that back (37 KB against 40 KB at -6), so the case
// is the parse, not the transfer. docs/api.md is the key.
//
// network is required, like /api/source: a stamp is a block height, and a
// tree mixing two chains would draw one realm twice under two stamps.
func (a *API) HandleCodeTree(w http.ResponseWriter, r *http.Request) {
	network := a.networkParam(r)
	if network == "" {
		jsonError(w, "the code tree is per-chain: add ?network=", http.StatusBadRequest)
		return
	}

	// The window starts on an hour boundary, so two computations inside one
	// hour count over the same window and, with no new calls, serve the same
	// bytes under the same ETag. A window sliding by the second would change
	// the body on every recompute and make the validator useless.
	since := codeTreeNow().UTC().Truncate(time.Hour).AddDate(0, 0, -codeTreeWindowDays)
	tag, bad := tagParam(r)
	if bad != "" {
		jsonError(w, bad, http.StatusBadRequest)
		return
	}
	pkgs, err := a.db.CodeTree(network, since)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	pkgTags, err := a.db.PackageTags(network, nil)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	out := codeTree{
		Network:    network,
		Since:      since.Format(time.RFC3339),
		WindowDays: codeTreeWindowDays,
		Packages:   make([]codeTreePackage, 0, len(pkgs)),
	}
	for _, p := range pkgs {
		var names []string
		hasTag := tag == ""
		for _, t := range pkgTags[p.Path] {
			names = append(names, t.Tag)
			hasTag = hasTag || t.Tag == tag
		}
		if !hasTag {
			continue
		}
		cp := codeTreePackage{
			Tags:       names,
			Path:       p.Path,
			Namespace:  store.NamespaceOf(p.Path),
			Kind:       "p",
			Height:     p.Height,
			Files:      make([]codeTreeFile, len(p.Files)),
			Calls:      p.Calls,
			Callers:    p.Callers,
			Dependents: p.Dependents,
		}
		if p.IsRealm {
			cp.Kind = "r"
		}
		for i, f := range p.Files {
			cp.Files[i] = codeTreeFile(f)
			cp.Lines += f.Lines
			cp.Bytes += f.Bytes
		}
		if family, gen, ok := discover.Generation(p.Path); ok {
			if family != p.Path {
				cp.Family = family
			}
			for _, g := range gen {
				if g != 0 {
					cp.Gen = gen
					break
				}
			}
		}
		if p.Height > out.Height {
			out.Height = p.Height
		}
		out.Files += len(p.Files)
		out.Lines += cp.Lines
		out.Bytes += cp.Bytes
		out.Packages = append(out.Packages, cp)
	}
	out.Count = len(out.Packages)

	body, err := json.Marshal(out)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// The (height, count) prefix names the code that is deployed: a deploy,
	// a redeploy and a new package all move one of the two. It is not enough
	// on its own, because the activity numbers move with every call, so the
	// tail is over the bytes, and a strong validator changes whenever the
	// representation does.
	sum := sha256.Sum256(body)
	etag := `"ct-` + strconv.Itoa(out.Height) + "-" + strconv.Itoa(out.Count) + "-" +
		hex.EncodeToString(sum[:8]) + `"`
	writeSourceJSON(w, r, etag, "public, max-age=60", body)
}

type codeTree struct {
	Network string `json:"network"`
	// Height is the newest stamp in the tree, Count the number of packages.
	Height     int               `json:"height"`
	Count      int               `json:"count"`
	Files      int               `json:"files"`
	Lines      int               `json:"lines"`
	Bytes      int               `json:"bytes"`
	Since      string            `json:"since"`
	WindowDays int               `json:"window_days"`
	Packages   []codeTreePackage `json:"packages"`
}

// codeTreePackage is one package. Keys are short on purpose, see
// HandleCodeTree; the zero-valued activity fields are left out because most
// packages on a chain have none.
type codeTreePackage struct {
	Path       string         `json:"p"`
	Namespace  string         `json:"ns"`
	Kind       string         `json:"k"`
	Height     int            `json:"h"`
	Files      []codeTreeFile `json:"f"`
	Lines      int            `json:"l"`
	Bytes      int            `json:"b"`
	Family     string         `json:"fam,omitempty"`
	Gen        []int          `json:"g,omitempty"`
	Calls      int            `json:"c,omitempty"`
	Callers    int            `json:"u,omitempty"`
	Dependents int            `json:"d,omitempty"`
	// Tags are the package's code-derived tag names, in rule order. Names
	// only: the evidence is per tag and per package, and /api/tags?path=
	// serves it for the one package on screen.
	Tags []string `json:"t,omitempty"`
}

// codeTreeFile serializes as a [name, lines, bytes] triple: an object per
// file would repeat three keys three thousand times.
type codeTreeFile store.CodeTreeFile

func (f codeTreeFile) MarshalJSON() ([]byte, error) {
	return json.Marshal([3]any{f.Name, f.Lines, f.Bytes})
}
