package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The forge is the collaboration layer a gno package does not otherwise have.
//
// The chain knows a package's deploys, files and imports, and gnohub already
// draws those. It does not know issues, pull requests or releases, because
// nothing on chain records them: gno.land has no such concept. r/moul/forge
// does, and this is the read path that connects the two.
//
// The join is deliberately claim-based. A forge repo id is "<namespace>/<name>",
// which no package path can be, so the forge stores the mapping explicitly and
// nothing verifies it: a realm cannot ask the chain who deployed a path. The
// endpoint therefore reports what the forge SAYS, and the frontend says so in
// those words rather than presenting it as a chain fact.
const forgeRealm = "gno.land/r/moul/forge/v0"

// This data moves at human speed: an issue is filed, a release is cut. The
// negative TTL is shorter because "nobody has linked this package" is the answer
// for almost every path and it is the one a reader is most likely to be waiting
// to change, having just linked it.
const (
	forgeCacheTTL    = 2 * time.Minute
	forgeNegativeTTL = 30 * time.Second
)

// Keyed on network and path, never on path alone: the forge is realm state, and
// the same package path exists on more than one chain.
var forgeCache = newMemo[*ForgeLink](forgeCacheTTL, forgeNegativeTTL)

// ForgeLink is what the forge claims about one package path.
//
// Linked is false, with no error, for the overwhelmingly common case: a package
// whose author has never registered it. That is not a failure and the response
// says so, because a frontend that cannot tell "nobody has linked this" from
// "the node is down" will show the wrong empty state for both.
type ForgeLink struct {
	Path    string `json:"path"`
	Network string `json:"network,omitempty"`
	Realm   string `json:"realm"`
	Linked  bool   `json:"linked"`

	RepoID string `json:"repo_id,omitempty"`
	// RepoURL is the forge realm's own rendered page for the repo. gnohub does
	// not re-implement the issue and change lists: the realm already renders
	// them, and a second rendering is a second thing to keep true.
	RepoURL       string `json:"repo_url,omitempty"`
	LatestRelease string `json:"latest_release,omitempty"`
	ReleaseCount  int    `json:"release_count,omitempty"`
	LogHead       string `json:"log_head,omitempty"`

	// Unavailable carries why nothing could be read, when that is the answer.
	// A missing forge realm on a chain that has none is the normal case here,
	// not an error worth a 500.
	Unavailable string `json:"unavailable,omitempty"`
}

// HandleForgeLink answers "does a forge repo claim this package path, and what
// has it released".
//
// Requires a network: the forge is a realm, its state is per chain, and a
// blended answer would attribute one chain's issues to another's package.
func (a *API) HandleForgeLink(w http.ResponseWriter, r *http.Request) {
	network := a.networkParam(r)
	if network == "" {
		jsonError(w, "the forge is realm state, so it is per-chain: add ?network=", 400)
		return
	}
	path := strings.TrimRight("gno.land/"+r.PathValue("path"), "/")

	out := &ForgeLink{Path: path, Network: network, Realm: forgeRealm}

	rpcURL := a.rpcURLFor(network)
	if rpcURL == "" {
		out.Unavailable = "no verified RPC endpoint for " + network
		JSONResponse(w, out)
		return
	}

	got := forgeCache.get(r.Context(), network+"\x00"+path, func(ctx context.Context) (*ForgeLink, bool) {
		return fetchForgeLink(ctx, rpcURL, network, path)
	})
	if got == nil {
		out.Unavailable = "the forge could not be read on " + network
		JSONResponse(w, out)
		return
	}
	JSONResponse(w, got)
}

// fetchForgeLink does the reads. The bool is memo's "worth caching for the full
// TTL": an unreadable forge is cached only briefly, so a node blip does not
// pin "unavailable" onto every realm page for two minutes.
func fetchForgeLink(ctx context.Context, rpcURL, network, path string) (*ForgeLink, bool) {
	out := &ForgeLink{Path: path, Network: network, Realm: forgeRealm}

	repoID, err := forgeString(ctx, rpcURL, fmt.Sprintf("%s.PackageRepo(%q)", forgeRealm, path))
	if err != nil {
		// The realm is not deployed on this chain, or the node refused. Either
		// way there is nothing to show and nothing is broken about the page.
		out.Unavailable = err.Error()
		return out, false
	}
	if repoID == "" {
		return out, true
	}

	out.Linked = true
	out.RepoID = repoID
	out.RepoURL = "https://gno.land/r/moul/forge/v0:" + repoID

	// Three independent reads, so three at once: each is a full round trip to a
	// public RPC and doing them in sequence triples the page's slowest path for
	// no reason.
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		out.LatestRelease, _ = forgeString(ctx, rpcURL,
			fmt.Sprintf("%s.LatestReleaseTag(%q)", forgeRealm, repoID))
	}()
	go func() {
		defer wg.Done()
		out.ReleaseCount, _ = forgeInt(ctx, rpcURL,
			fmt.Sprintf("%s.ReleaseCount(%q)", forgeRealm, repoID))
	}()
	go func() {
		defer wg.Done()
		out.LogHead, _ = forgeString(ctx, rpcURL,
			fmt.Sprintf("%s.LogHead(%q)", forgeRealm, repoID))
	}()
	wg.Wait()

	return out, true
}

// qeval answers in Gno's debug repr, not JSON: a string comes back as
// `("moul/forge" string)` and an int as `(3 int)`. An empty string is
// `("" string)`, which is a real answer (nobody claims this path) and must not
// read as a failed query: see delegationHolderFromRepr, which learned the same
// lesson on a value that had no quoted token at all.
var (
	forgeStringRe = regexp.MustCompile(`^\("((?:[^"\\]|\\.)*)" string\)`)
	forgeIntRe    = regexp.MustCompile(`^\((-?\d+) int\)`)
)

func forgeString(ctx context.Context, rpcURL, expr string) (string, error) {
	out, err := fetchABCIQuery(ctx, rpcURL, "vm/qeval", expr)
	if err != nil {
		return "", err
	}
	m := forgeStringRe.FindStringSubmatch(strings.TrimSpace(out))
	if m == nil {
		return "", fmt.Errorf("unexpected qeval response: %s", truncate(out, 120))
	}
	return m[1], nil
}

func forgeInt(ctx context.Context, rpcURL, expr string) (int, error) {
	out, err := fetchABCIQuery(ctx, rpcURL, "vm/qeval", expr)
	if err != nil {
		return 0, err
	}
	m := forgeIntRe.FindStringSubmatch(strings.TrimSpace(out))
	if m == nil {
		return 0, fmt.Errorf("unexpected qeval response: %s", truncate(out, 120))
	}
	return strconv.Atoi(m[1])
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
