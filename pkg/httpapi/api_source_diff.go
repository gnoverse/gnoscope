package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gnoverse/gnoscope/pkg/srcdiff"
	"github.com/gnoverse/gnoscope/pkg/store"
)

// SourceDiffSuffix ends a source URL that asks what changed rather than for
// the source: /api/source/{path...}/diff.
const SourceDiffSuffix = "/diff"

// diffVersion names one side of a diff.
type diffVersion struct {
	Path   string `json:"path"`
	Height int    `json:"height"`
	TxHash string `json:"tx_hash"`
	Time   string `json:"time,omitempty"`
	Failed bool   `json:"failed,omitempty"`
}

// diffTotals sums a diff's files.
type diffTotals struct {
	FilesAdded     int `json:"files_added"`
	FilesRemoved   int `json:"files_removed"`
	FilesModified  int `json:"files_modified"`
	FilesUnchanged int `json:"files_unchanged"`
	LinesAdded     int `json:"lines_added"`
	LinesRemoved   int `json:"lines_removed"`
}

type sourceDiff struct {
	Path    string `json:"path"`
	Network string `json:"network"`
	// From is null when there is nothing earlier to compare against: the
	// first publication at a path, which is then every file added.
	From   *diffVersion       `json:"from"`
	To     diffVersion        `json:"to"`
	API    srcdiff.API        `json:"api"`
	Files  []srcdiff.FileDiff `json:"files"`
	Totals diffTotals         `json:"totals"`
}

// isDiffRequest reports whether a source path is a diff request: it ends in
// /diff, and the path with that suffix is not itself a package on the
// network. A package whose last segment is diff keeps its own URL, and its
// diff is .../diff/diff.
func (a *API) isDiffRequest(network, path string) (string, bool) {
	base, ok := strings.CutSuffix(path, SourceDiffSuffix)
	if !ok || base == "gno.land" {
		return "", false
	}
	if total, _, err := a.db.SubmissionCounts(network, path); err == nil && total > 0 {
		return "", false
	}
	if _, err := a.db.PackageSource(network, path, false); err == nil {
		return "", false
	}
	return base, true
}

// serveSourceDiff answers /api/source/{path...}/diff: what changed between
// two submissions, as declarations and as lines.
//
//	to=<height>         defaults to the current stamp
//	from=<height>       defaults to the newest successful submission below to
//	from_path=<path>    compare against another path (a previous generation);
//	                    from then defaults to that path's current stamp
//
// Either height may name any stored submission, failed ones included, the
// same as /api/source?at=. One that names nothing stored is a 409, a path
// with no source at all a 404. With both heights given the answer is
// immutable, like a pinned read; without, it moves with the next publish.
func (a *API) serveSourceDiff(w http.ResponseWriter, r *http.Request, network, path string) {
	q := r.URL.Query()
	parseH := func(name string) (int, bool, error) {
		v := q.Get(name)
		if v == "" {
			return 0, false, nil
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return 0, false, errors.New(name + " must be a block height")
		}
		return n, true, nil
	}
	toH, toSet, err := parseH("to")
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	fromH, fromSet, err := parseH("from")
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	fromPath := path
	if fp := q.Get("from_path"); fp != "" {
		fromPath = "gno.land/" + strings.TrimPrefix(strings.Trim(fp, "/"), "gno.land/")
	}

	// to: the current stamp unless asked.
	if !toSet {
		cur, err := a.db.PackageSource(network, path, false)
		if errors.Is(err, store.ErrNoSource) {
			a.diffMissing(w, network, path, "no current source at "+path+": pass to=<height>")
			return
		}
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		toH = cur.Stamp.Height
	}
	to, err := a.pinnedSubmission(network, path, toH, "")
	if a.diffResolveError(w, err, network, path, "to", toH) {
		return
	}

	// from: below to at the same path, or the other path's current stamp.
	var from *store.PackageSource
	switch {
	case fromSet:
		from, err = a.pinnedSubmission(network, fromPath, fromH, "")
		if a.diffResolveError(w, err, network, fromPath, "from", fromH) {
			return
		}
	case fromPath != path:
		from, err = a.db.PackageSource(network, fromPath, true)
		if errors.Is(err, store.ErrNoSource) {
			a.diffMissing(w, network, fromPath, "no current source at "+fromPath)
			return
		}
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
	default:
		subs, err := a.db.PathSubmissions(network, path)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		prev := -1
		for _, s := range subs {
			if s.Success && s.Height < to.Stamp.Height {
				prev = s.Height
			}
		}
		if prev >= 0 {
			from, err = a.pinnedSubmission(network, path, prev, "")
			if a.diffResolveError(w, err, network, path, "from", prev) {
				return
			}
		}
	}

	out := sourceDiff{Path: path, Network: network, To: versionOf(path, to)}
	var fromFiles []srcdiff.File
	if from != nil {
		v := versionOf(fromPath, from)
		out.From = &v
		fromFiles = diffFiles(from)
	}
	toFiles := diffFiles(to)
	out.API = srcdiff.DiffAPI(fromFiles, toFiles)
	out.Files = srcdiff.DiffFiles(fromFiles, toFiles)
	for _, f := range out.Files {
		switch f.Status {
		case "added":
			out.Totals.FilesAdded++
		case "removed":
			out.Totals.FilesRemoved++
		case "modified":
			out.Totals.FilesModified++
		default:
			out.Totals.FilesUnchanged++
		}
		out.Totals.LinesAdded += f.Added
		out.Totals.LinesRemoved += f.Removed
	}

	body, err := json.Marshal(out)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if toSet && fromSet {
		// Both ends named by height: the same bytes forever, keyed by what
		// names them, like a pinned read.
		fromStamp := ""
		if out.From != nil {
			fromStamp = out.From.Path + "@" + strconv.Itoa(out.From.Height) + "/" + out.From.TxHash
		}
		sum := sha256.Sum256([]byte(strings.Join([]string{
			network, path, fromStamp, strconv.Itoa(out.To.Height), out.To.TxHash, srcdiff.Version,
		}, "\x00")))
		writeSourceJSON(w, r, `"src-d-`+hex.EncodeToString(sum[:16])+`"`, "public, max-age=31536000, immutable", body)
		return
	}
	sum := sha256.Sum256(body)
	writeSourceJSON(w, r, `"src-dm-`+hex.EncodeToString(sum[:16])+`"`, "public, max-age=30", body)
}

func versionOf(path string, src *store.PackageSource) diffVersion {
	return diffVersion{Path: path, Height: src.Stamp.Height, TxHash: src.Stamp.TxHash, Time: src.Stamp.Time, Failed: src.Failed}
}

func diffFiles(src *store.PackageSource) []srcdiff.File {
	out := make([]srcdiff.File, 0, len(src.Files))
	for _, f := range src.Files {
		b := ""
		if f.Body != nil {
			b = *f.Body
		}
		out = append(out, srcdiff.File{Name: f.Name, Body: b})
	}
	return out
}

// diffResolveError answers for a side that did not resolve, and reports
// whether it did.
func (a *API) diffResolveError(w http.ResponseWriter, err error, network, path, side string, at int) bool {
	if err == nil {
		return false
	}
	var conflict *sourceConflict
	if errors.As(err, &conflict) {
		if conflict.current == nil && !conflict.anySubmission {
			jsonError(w, "package not found: "+path, http.StatusNotFound)
			return true
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		body := map[string]any{
			"error":   side + ": no stored submission at this height for this path",
			"path":    path,
			"network": network,
			side:      at,
		}
		if conflict.current != nil {
			body["current"] = conflict.current
		}
		_ = json.NewEncoder(w).Encode(body)
		return true
	}
	jsonError(w, err.Error(), http.StatusInternalServerError)
	return true
}

// diffMissing is a 404 for a path with no source at all, and a 409 for one
// whose submissions all failed (it has no current stamp to default to).
func (a *API) diffMissing(w http.ResponseWriter, network, path, msg string) {
	total, _, err := a.db.SubmissionCounts(network, path)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if total == 0 {
		jsonError(w, "package not found: "+path, http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonError(w, msg, http.StatusConflict)
}
