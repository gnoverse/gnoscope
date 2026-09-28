package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gnoverse/gnoscope/pkg/stdlibs"
	"github.com/gnoverse/gnoscope/pkg/store"
)

// The standard library: served from what the stdlib crawler stored, and read
// from a node by the adapter below.
//
// The adapter lives here rather than in pkg/stdlibs because fetchABCIQuery
// already lives here and is used by thirteen call sites across this package.
// Moving it to a shared package to satisfy one new consumer would touch eight
// files other work is live in; injecting the interface instead keeps the
// crawler testable without a chain and adds no second copy of the ABCI
// plumbing.

// RPCFetcher adapts this package's ABCI helper to stdlibs.Fetcher.
type RPCFetcher struct{ RPCURL string }

func (f RPCFetcher) QFile(ctx context.Context, path string) (string, error) {
	return fetchABCIQuery(ctx, f.RPCURL, "vm/qfile", path)
}

func (f RPCFetcher) QPaths(ctx context.Context, prefix string) (string, error) {
	return fetchABCIQuery(ctx, f.RPCURL, "vm/qpaths", prefix)
}

var _ stdlibs.Fetcher = RPCFetcher{}

// StdlibResponse is one stdlib package.
type StdlibResponse struct {
	Path    string             `json:"path"`
	Network string             `json:"network"`
	Files   []store.StdlibFile `json:"files"`
	// Stdlib is always true here, so a client that renders both this and a
	// realm from one code path can tell them apart without inspecting the
	// path.
	Stdlib bool `json:"stdlib"`
	// Provenance is the honest answer to "when was this deployed and by whom",
	// which for stdlib is neither. It ships with the node binary, so it has no
	// block height, no deployer and no transaction, and rendering a zero
	// height or a blank publisher would be a misreport rather than a gap.
	Provenance string `json:"provenance"`
}

// HandleStdlib serves one stdlib package's source.
func (a *API) HandleStdlib(w http.ResponseWriter, r *http.Request) {
	network := a.networkParam(r)
	path := strings.Trim(r.PathValue("path"), "/")
	if path == "" {
		a.handleStdlibList(w, network)
		return
	}

	files, err := a.db.StdlibPackage(network, path)
	if err != nil {
		jsonError(w, "read stdlib", 500)
		return
	}
	if len(files) == 0 {
		jsonError(w, path+" is not a standard library package on "+network, 404)
		return
	}
	writeJSON(w, StdlibResponse{
		Path:       path,
		Network:    network,
		Files:      files,
		Stdlib:     true,
		Provenance: "shipped with the node",
	})
}

func (a *API) handleStdlibList(w http.ResponseWriter, network string) {
	pkgs, err := a.db.StdlibPackages(network)
	if err != nil {
		jsonError(w, "read stdlib", 500)
		return
	}
	files, _ := a.db.StdlibFileCount(network)
	if pkgs == nil {
		pkgs = []string{}
	}
	writeJSON(w, struct {
		Network    string   `json:"network"`
		Packages   []string `json:"packages"`
		Count      int      `json:"count"`
		Files      int      `json:"files"`
		Provenance string   `json:"provenance"`
	}{network, pkgs, len(pkgs), files, "shipped with the node"})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
