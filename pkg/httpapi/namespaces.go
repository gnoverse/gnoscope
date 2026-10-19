package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sync"
	"time"
)

// Namespace ownership, read from the chain rather than inferred from who
// deployed what.
//
// The inference is wrong often enough to matter. Deploy history says
// gno.land/r/onbloc belongs to the genesis deployer, because onbloc's packages
// were included in genesis rather than deployed by onbloc — and the same
// address then "owns" gnoland, aeddi, mason, sunspirit and jeronimoalbi by the
// same reasoning. Checked against the registry on mainnet, four of twelve
// namespaces resolved to a different account than their deploy history
// suggested, including one where the derived label itself disagrees.
//
// r/sys/users is the registry the chain itself uses, so it is the answer rather
// than an approximation of it.
const namespaceRealm = "gno.land/r/sys/users"

// nsOwnerTTL is how long a resolved owner is trusted. Registrations change
// rarely; this is short enough that a transfer shows up the same session.
const nsOwnerTTL = 10 * time.Minute

// nsAddress pulls the account out of qeval's rendered struct. The realm returns
// a Go-ish literal rather than JSON, and the address is the only bech32-shaped
// token in it.
var nsAddress = regexp.MustCompile(`g1[a-z0-9]{38}`)

type nsCacheEntry struct {
	owners  map[string]string
	fetched time.Time
}

var nsCache = struct {
	mu sync.Mutex
	by map[string]nsCacheEntry
}{by: map[string]nsCacheEntry{}}

// nsResolveConcurrency bounds the fan-out below. Each resolve is one
// lightweight abci_query the node answers from local state, not a write, so the
// node is not the reason for a bound; the bound is there so a chain that grows
// to hundreds of namespaces opens tens of connections rather than hundreds.
const nsResolveConcurrency = 24

// nsResolveTimeout bounds one resolve. Without it the only deadline is the
// reader's own context, which means one unresponsive query holds a page open
// for as long as the reader is willing to wait — and this runs on the boot path
// of every page, so that is every page.
//
// A name that times out is skipped exactly like one that errors: absent from
// the map, not cached as unregistered.
const nsResolveTimeout = 5 * time.Second

// NamespaceOwners resolves each name to the account registered for it.
//
// Unregistered names are omitted rather than guessed at: a namespace can hold
// packages without a registration (leon does on mainnet), and inventing an
// owner for it is exactly the error this exists to avoid.
//
// One query per name, fanned out. It used to be a sequential loop, written when
// the comment here could say the set was "bounded by what has actually been
// deployed — twelve on mainnet". It is 19 on mainnet and 23 on onyx now, and
// the loop's cost is the sum of them: measured against rpc.gno.land, an
// abci_query answers in 130-460 ms when the node is healthy, so 19 of them in a
// row is 2.5-8.7 s, and far worse when it is not.
//
// This endpoint is on the boot path of every page, behind a 10-minute cache, so
// the first reader of each window pays that whole sum while everyone else pays
// nothing. On the deployment's own /api/traffic it was the worst offender on
// the site by a wide margin: 35 of the 39 cache misses over 10s in a week, up
// to 59.3 s.
//
// A name that fails to resolve is skipped, not cached as absent, so a transient
// RPC failure does not pin it as unregistered for the whole TTL.
func (a *API) NamespaceOwners(ctx context.Context, network string, names []string) map[string]string {
	rpcURL := a.rpcURLFor(network)
	if rpcURL == "" {
		return nil
	}

	nsCache.mu.Lock()
	if e, ok := nsCache.by[network]; ok && time.Since(e.fetched) < nsOwnerTTL {
		nsCache.mu.Unlock()
		return e.owners
	}
	nsCache.mu.Unlock()

	resolved := make([]string, len(names))
	sem := make(chan struct{}, nsResolveConcurrency)
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, name string) {
			defer wg.Done()
			defer func() { <-sem }()

			callCtx, cancel := context.WithTimeout(ctx, nsResolveTimeout)
			defer cancel()

			expr := fmt.Sprintf(`%s.ResolveName(%q)`, namespaceRealm, name)
			out, err := fetchABCIQuery(callCtx, rpcURL, "vm/qeval", expr)
			if err != nil {
				return
			}
			resolved[i] = nsAddress.FindString(out)
		}(i, name)
	}
	wg.Wait()

	owners := map[string]string{}
	for i, name := range names {
		if resolved[i] != "" {
			owners[name] = resolved[i]
		}
	}

	nsCache.mu.Lock()
	nsCache.by[network] = nsCacheEntry{owners: owners, fetched: time.Now()}
	nsCache.mu.Unlock()
	return owners
}

// HandleNamespaces answers name -> owning account for a network.
func (a *API) HandleNamespaces(w http.ResponseWriter, r *http.Request) {
	network := a.networkParam(r)
	if network == "" {
		// Ownership is per chain: the same name can be registered to different
		// accounts on two networks, and merging them would assert something
		// neither chain says.
		jsonError(w, "namespace ownership is per-chain: add ?network=", 400)
		return
	}

	names, err := a.db.Namespaces(network)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	owners := a.NamespaceOwners(r.Context(), network, names)
	if owners == nil {
		owners = map[string]string{}
	}
	JSONResponse(w, owners)
}
