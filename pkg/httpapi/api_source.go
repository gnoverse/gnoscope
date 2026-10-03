package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gnoverse/gnoscope/pkg/discover"
	"github.com/gnoverse/gnoscope/pkg/store"
)

// SourcePrefix is the route prefix of the source endpoint. The response cache
// reads it to give pinned reads their own TTL (see pinnedSourceTTL).
const SourcePrefix = "/api/source/"

// HandleSource serves a package's source on its own, apart from the realm
// detail.
//
// /api/realm/{path...} carries every file body beside calls, dependents and
// MsgRun references, which move every sync pass, so the whole payload is only
// good for the 30s cache TTL even though the source in it changes once per
// deploy. This endpoint splits the two:
//
//   - without `at`, a manifest: the current stamp (the submission whose
//     source is stored), file names with sizes and line counts, the other
//     generations of the same app on this network, and how often the path was
//     deployed. Short-lived, with an ETag over its bytes.
//   - with `at=<height>` equal to the current stamp, the bodies too, marked
//     immutable. What a stamp names cannot change, so a browser keeps it
//     forever and the server keeps it a day.
//   - with `at` naming any other height, 409 with the current stamp. Only the
//     current submission's source is stored, so the bytes of an older one do
//     not exist here, and serving today's bytes under yesterday's stamp would
//     poison every cache that ever saw that URL.
//
// network is required, like /api/storage: a stamp is a block height, and a
// height means nothing without its chain.
func (a *API) HandleSource(w http.ResponseWriter, r *http.Request) {
	network := a.networkParam(r)
	if network == "" {
		jsonError(w, "source is per-chain: add ?network=", http.StatusBadRequest)
		return
	}
	path := strings.TrimRight("gno.land/"+r.PathValue("path"), "/")
	q := r.URL.Query()
	atParam, file := q.Get("at"), q.Get("file")

	if atParam == "" {
		if file != "" {
			jsonError(w, "file= needs at=<height>: take the height from the manifest's stamp", http.StatusBadRequest)
			return
		}
		a.serveSourceManifest(w, r, network, path)
		return
	}
	at, err := strconv.Atoi(atParam)
	if err != nil || at <= 0 {
		jsonError(w, "at must be a block height", http.StatusBadRequest)
		return
	}
	a.servePinnedSource(w, r, network, path, at, file)
}

// sourceKind is the package's kind in the vocabulary /api/packages filters by.
func sourceKind(src *store.PackageSource) string {
	if src.IsRealm {
		return string(store.KindRealm)
	}
	return string(store.KindPure)
}

type sourceManifest struct {
	Path        string                `json:"path"`
	Network     string                `json:"network"`
	Name        string                `json:"name"`
	Kind        string                `json:"kind"`
	Stamp       store.SourceStamp     `json:"stamp"`
	Files       []store.SourceFile    `json:"files"`
	Siblings    []store.SourceSibling `json:"siblings"`
	Submissions int                   `json:"submissions"`
	Redeploys   int                   `json:"redeploys"`
}

func (a *API) serveSourceManifest(w http.ResponseWriter, r *http.Request, network, path string) {
	src, err := a.db.PackageSource(network, path, false)
	if errors.Is(err, store.ErrNoSource) {
		jsonError(w, "package not found: "+path, http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	siblings, err := a.sourceSiblings(network, path)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	total, ok, err := a.db.SubmissionCounts(network, path)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	redeploys := ok - 1
	if redeploys < 0 {
		redeploys = 0
	}

	body, err := json.Marshal(sourceManifest{
		Path: src.Path, Network: src.Network, Name: src.Name, Kind: sourceKind(src),
		Stamp: src.Stamp, Files: src.Files, Siblings: siblings,
		Submissions: total, Redeploys: redeploys,
	})
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Over the bytes, not over the stamp alone: the manifest also carries the
	// siblings and the submission count, which move without this package's
	// stamp moving, and a strong validator has to change whenever the
	// representation does.
	sum := sha256.Sum256(body)
	writeSourceJSON(w, r, `"src-m-`+hex.EncodeToString(sum[:16])+`"`,
		"public, max-age=30", body)
}

type pinnedSource struct {
	Path    string             `json:"path"`
	Network string             `json:"network"`
	Name    string             `json:"name"`
	Kind    string             `json:"kind"`
	Stamp   store.SourceStamp  `json:"stamp"`
	Files   []store.SourceFile `json:"files"`
}

func (a *API) servePinnedSource(w http.ResponseWriter, r *http.Request, network, path string, at int, file string) {
	src, err := a.db.PackageSource(network, path, true)
	if errors.Is(err, store.ErrNoSource) {
		jsonError(w, "package not found: "+path, http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if at != src.Stamp.Height {
		// Never cached by the response cache (it stores 200s only), and told
		// not to be cached anywhere else: the right answer changes the moment
		// a reader follows `current`, and a stale 409 is a loop.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":   "only the current submission's source is stored; re-read at the current stamp",
			"path":    path,
			"network": network,
			"at":      at,
			"current": src.Stamp,
		})
		return
	}

	files := src.Files
	variant := "all"
	if file != "" {
		files = nil
		for _, f := range src.Files {
			if f.Name == file {
				files = []store.SourceFile{f}
				break
			}
		}
		if files == nil {
			jsonError(w, "no file "+file+" in "+path, http.StatusNotFound)
			return
		}
		variant = "file:" + file
	}

	body, err := json.Marshal(pinnedSource{
		Path: src.Path, Network: src.Network, Name: src.Name, Kind: sourceKind(src),
		Stamp: src.Stamp, Files: files,
	})
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Derived from what the stamp names rather than from the bytes: the same
	// (network, path, height, tx) is the same source by construction, so the
	// validator is stable across processes and needs no body to compute.
	// variant separates the whole-package and single-file representations.
	sum := sha256.Sum256([]byte(strings.Join([]string{
		network, path, strconv.Itoa(src.Stamp.Height), src.Stamp.TxHash, variant,
	}, "\x00")))
	writeSourceJSON(w, r, `"src-`+hex.EncodeToString(sum[:16])+`"`,
		"public, max-age=31536000, immutable", body)
}

// writeSourceJSON answers with a validator, and with 304 when the reader
// already holds it. On a cache hit serveEntry gives the same 304; this covers
// the request that computed the entry.
func writeSourceJSON(w http.ResponseWriter, r *http.Request, etag, cacheControl string, body []byte) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", cacheControl)
	h.Set("ETag", etag)
	if match := r.Header.Get("If-None-Match"); match != "" && matchesETag(match, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(append(body, '\n'))
}

// sourceSiblings lists the other generations of path's app on one network:
// every package whose discover.Generation family key matches, oldest
// generation first.
func (a *API) sourceSiblings(network, path string) ([]store.SourceSibling, error) {
	out := []store.SourceSibling{}
	family, _, ok := discover.Generation(path)
	if !ok {
		return out, nil
	}
	// A family never leaves its namespace (Generation keeps the first three
	// segments verbatim), so that is the candidate set.
	segs := strings.SplitN(path, "/", 4)
	if len(segs) < 4 {
		return out, nil
	}
	candidates, err := a.db.PackagesUnderNamespace(network, strings.Join(segs[:3], "/")+"/")
	if err != nil {
		return nil, err
	}
	type gen struct {
		s   store.SourceSibling
		gen []int
	}
	var keep []gen
	for _, c := range candidates {
		if c.Path == path {
			continue
		}
		f, g, ok := discover.Generation(c.Path)
		if ok && f == family {
			keep = append(keep, gen{c, g})
		}
	}
	sort.SliceStable(keep, func(i, j int) bool {
		if discover.GenerationLess(keep[i].gen, keep[j].gen) {
			return true
		}
		if discover.GenerationLess(keep[j].gen, keep[i].gen) {
			return false
		}
		return keep[i].s.Path < keep[j].s.Path
	})
	for _, k := range keep {
		out = append(out, k.s)
	}
	return out, nil
}
