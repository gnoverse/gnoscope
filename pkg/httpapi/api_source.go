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
	"github.com/gnoverse/gnoscope/pkg/srctok"
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
//   - with `at=<height>` naming the current stamp or any other submission at
//     the path, that submission's bodies, marked immutable. What a stamp
//     names cannot change, so a browser keeps it forever and the server
//     keeps it a day. Every submission's files are stored (submission_files),
//     failed ones included; `tx=<hash>` picks one when a block holds two.
//   - with `at`, `file` and `tokens=<version>`, that one file as classified
//     segments per line instead of a body (see pkg/srctok), same caching:
//     the segments are a pure function of the stamp's bytes and the
//     tokenizer version, and the version is in the URL.
//   - with `at` naming a height with no stored submission at the path, 409
//     with the current stamp. Serving other bytes under that stamp would
//     poison every cache that ever saw the URL.
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
	tokens := q.Has("tokens")
	if tokens {
		// The version is checked rather than ignored because the answer is
		// immutable: a client that asked for v1 must not be handed v2 under
		// the URL it will keep for a year.
		if v := q.Get("tokens"); v != srctok.Version {
			jsonError(w, "tokens="+v+" is not served: this server speaks tokens="+srctok.Version, http.StatusBadRequest)
			return
		}
		if atParam == "" || file == "" {
			jsonError(w, "tokens= needs at=<height> and file=<name>: one file of one stamp", http.StatusBadRequest)
			return
		}
	}

	if atParam == "" {
		if file != "" {
			jsonError(w, "file= needs at=<height>: take the height from the manifest's stamp", http.StatusBadRequest)
			return
		}
		a.serveSourceManifest(w, r, network, path)
		return
	}
	// Zero is a real stamp: every package in a chain's genesis was deployed
	// at height 0, which on gnoland1 is most of them.
	at, err := strconv.Atoi(atParam)
	if err != nil || at < 0 {
		jsonError(w, "at must be a block height", http.StatusBadRequest)
		return
	}
	a.servePinnedSource(w, r, network, path, at, file, tokens)
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
	// History is every MsgAddPackage at the path, oldest first. A separate
	// name from Submissions, which has always been the count.
	History []store.SubmissionInfo `json:"history"`
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
	history, err := a.db.PathSubmissions(network, path)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	body, err := json.Marshal(sourceManifest{
		Path: src.Path, Network: src.Network, Name: src.Name, Kind: sourceKind(src),
		Stamp: src.Stamp, Files: src.Files, Siblings: siblings,
		Submissions: total, Redeploys: redeploys, History: history,
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
	Path    string            `json:"path"`
	Network string            `json:"network"`
	Name    string            `json:"name"`
	Kind    string            `json:"kind"`
	Stamp   store.SourceStamp `json:"stamp"`
	// Failed marks the source of a submission the chain rejected: what was
	// sent, never what was published.
	Failed bool               `json:"failed,omitempty"`
	Files  []store.SourceFile `json:"files"`
}

// pinnedTokens is one file of one stamp, classified. Lines has one entry per
// "\n"-separated line of the body, each a list of segments whose texts
// concatenate to that line exactly. Decls is package-wide: where each
// top-level name is declared, so a reference in this file can link to a
// declaration in another.
type pinnedTokens struct {
	Path    string                     `json:"path"`
	Network string                     `json:"network"`
	Name    string                     `json:"name"`
	Kind    string                     `json:"kind"`
	Stamp   store.SourceStamp          `json:"stamp"`
	Failed  bool                       `json:"failed,omitempty"`
	File    string                     `json:"file"`
	Version string                     `json:"tokens"`
	Lines   [][]srctok.Segment         `json:"lines"`
	Decls   map[string]srctok.Location `json:"decls"`
}

func (a *API) servePinnedSource(w http.ResponseWriter, r *http.Request, network, path string, at int, file string, tokens bool) {
	src, err := a.pinnedSubmission(network, path, at, r.URL.Query().Get("tx"))
	var conflict *sourceConflict
	if errors.As(err, &conflict) {
		if conflict.current == nil && !conflict.anySubmission {
			jsonError(w, "package not found: "+path, http.StatusNotFound)
			return
		}
		// Never cached by the response cache (it stores 200s only), and told
		// not to be cached anywhere else: the right answer changes the moment
		// a reader follows `current`, and a stale 409 is a loop.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		body := map[string]any{
			"error":   "no stored submission at this height for this path; re-read at the current stamp",
			"path":    path,
			"network": network,
			"at":      at,
		}
		if conflict.current != nil {
			body["current"] = conflict.current
		}
		_ = json.NewEncoder(w).Encode(body)
		return
	}
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
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

	var body []byte
	if tokens {
		// The whole package is read either way (PackageSource has no
		// one-file form), and the tokenizer needs it: whether a name is a
		// package-level declaration depends on the other files.
		in := make([]srctok.File, 0, len(src.Files))
		for _, f := range src.Files {
			in = append(in, srctok.File{Name: f.Name, Body: *f.Body})
		}
		ix := srctok.NewIndex(in)
		lines, _ := ix.Tokenize(file)
		variant = "tokens:" + srctok.Version + ":" + file
		body, err = json.Marshal(pinnedTokens{
			Path: src.Path, Network: src.Network, Name: src.Name, Kind: sourceKind(src),
			Stamp: src.Stamp, Failed: src.Failed, File: file, Version: srctok.Version, Lines: lines, Decls: ix.Decls,
		})
	} else {
		body, err = json.Marshal(pinnedSource{
			Path: src.Path, Network: src.Network, Name: src.Name, Kind: sourceKind(src),
			Stamp: src.Stamp, Failed: src.Failed, Files: files,
		})
	}
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

// sourceConflict is why a pinned read has nothing to serve: no stored
// submission at that height. current is the path's stamp, nil when the path
// has no current state (never published, or every submission failed);
// anySubmission says whether the path was ever submitted at all, which is the
// difference between a 409 and a 404.
type sourceConflict struct {
	current       *store.SourceStamp
	anySubmission bool
}

func (c *sourceConflict) Error() string { return "no stored submission at that height" }

// pinnedSubmission resolves `at` (and `tx`, when given) to the source of one
// submission.
//
// The current stamp is answered from package_files, which is what it always
// was, and works on a database the per-submission backfill has not reached
// yet. Any other height is answered from submission_files. Both name immutable
// bytes, so either is served under the same caching.
func (a *API) pinnedSubmission(network, path string, at int, tx string) (*store.PackageSource, error) {
	cur, err := a.db.PackageSource(network, path, false)
	if err != nil && !errors.Is(err, store.ErrNoSource) {
		return nil, err
	}
	if cur != nil && cur.Stamp.Height == at && (tx == "" || tx == cur.Stamp.TxHash) {
		// Bodies in their own read, which is one snapshot of the row and the
		// files together; a redeploy landing between the two reads moves the
		// stamp, and the comparison below sends the request on to the
		// submission's own files instead.
		full, err := a.db.PackageSource(network, path, true)
		if err != nil && !errors.Is(err, store.ErrNoSource) {
			return nil, err
		}
		if full != nil && full.Stamp == cur.Stamp {
			return full, nil
		}
	}
	sub, err := a.db.SubmissionSource(network, path, at, tx, true)
	if errors.Is(err, store.ErrNoSubmissionSource) {
		c := &sourceConflict{}
		if cur != nil {
			c.current = &cur.Stamp
		}
		total, _, cerr := a.db.SubmissionCounts(network, path)
		if cerr != nil {
			return nil, cerr
		}
		c.anySubmission = total > 0
		return nil, c
	}
	if err != nil {
		return nil, err
	}
	return sub, nil
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
