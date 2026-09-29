package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/gnoverse/gnoscope/pkg/analyzer"
	"github.com/gnoverse/gnoscope/pkg/config"
	"github.com/gnoverse/gnoscope/pkg/discover"
	"github.com/gnoverse/gnoscope/pkg/ghlab"
	"github.com/gnoverse/gnoscope/pkg/httpapi"
	"github.com/gnoverse/gnoscope/pkg/indexer"
	"github.com/gnoverse/gnoscope/pkg/stdlibs"
	"github.com/gnoverse/gnoscope/pkg/store"
	"github.com/gnoverse/gnoscope/pkg/syncer"
	"github.com/gnoverse/gnoscope/pkg/traffic"
	"github.com/gnoverse/gnoscope/pkg/web"
)

var gitHash = "dev"       // set via -ldflags at build time
var buildTime = "unknown" // set via -ldflags at build time
var version = "dev"       // set via -ldflags at build time: git describe --tags --always

// startedAt is when this process came up, and it is the only one of the four
// build facts that is not stamped at link time. Uptime read from it answers
// the question a hash cannot: whether the instance in front of you has been
// restarted since the thing you are reporting happened.
var startedAt = time.Now().UTC()

// symbolIndexInterval is how often the symbol index re-walks the corpus. Longer
// than the rollups: a package's declarations change only when somebody deploys,
// and a new package being searchable a few minutes later is not a defect.
const symbolIndexInterval = 10 * time.Minute

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		listenAddr    = flag.String("listen", ":8888", "listen address")
		configPath    = flag.String("config", "", "config file path (JSON)")
		networkFlag   = flag.String("network", "", "single network ID (overrides config)")
		indexerFlag   = flag.String("indexer", "", "single network indexer URL (overrides config)")
		rpcFlag       = flag.String("rpc", "", "single network RPC URL")
		dbPath        = flag.String("db", "gnoscope.db", "SQLite database path")
		clearanceFlag = flag.String("clearance", "", "JSON file naming which namespaces are ours, which belong to other teams, and which must not be narrated (Discover verdicts). Empty means recommend nothing that is not chain-wide")
		syncOnStart   = flag.Bool("sync", true, "sync data from indexer on start")
		// Block backfill is the one sync phase that can pull hundreds of
		// megabytes per network (~130 bytes/block, ~430MB at mainnet's 3.3M
		// blocks), so it is the one phase an operator must be able to bound.
		// 90 days is the dashboards' default window, so the default depth is
		// exactly what the default view shows.
		blockHistoryDays = flag.Int("block-history-days", 90,
			"days of block history to backfill (0 = full chain history, negative = do not store blocks at all)")
		// Off unless an operator asks for it: every deployment shares the same
		// embedded frontend, so a hardcoded tag would make every one of them
		// report to somebody else's analytics account.
		analyticsScript = flag.String("analytics-script", "",
			"URL of an analytics script to load in the frontend, e.g. https://scripts.simpleanalyticscdn.com/latest.js (empty = none)")
		// Realm screenshots. Empty means the explorer draws no pictures of
		// realms at all, which is the right default: pointing at a capture
		// service that is not there would put a broken tile on every row.
		gnoshotURL = flag.String("gnoshot", "",
			"base URL of a gnoshot capture service, e.g. http://127.0.0.1:8890 (empty = no realm screenshots)")
		// Exposed because the right value depends on how fast the corpus moves,
		// and because a test fixture seeded after startup needs the next pass
		// sooner than a production chain does.
		symbolIndexEvery = flag.Duration("symbol-index-interval", symbolIndexInterval,
			"how often to re-index package symbols; the staleness check is one query, so this is cheap")
		// Same knob and the same reason as the one above: the badge table is
		// rebuilt from a fixture that is written after the binary starts, and a
		// test suite cannot wait ten minutes for the next pass.
		achievementEvery = flag.Duration("achievement-interval", store.AchievementInterval,
			"how often to rebuild the achievement table from indexed history")
		// The MCP endpoint carries no authentication, because the data is a
		// public chain and an API key would make it useless to the agents it
		// exists for. Per-IP limits are what make that safe, so they are
		// tunable but never absent by default. Zero disables a limit, which is
		// for a single-user local run and not for anything reachable.
		mcpPerMinute = flag.Int("mcp-rate", httpapi.MCPDefaultPerMinute,
			"MCP requests per minute per client address (0 = unlimited)")
		pprofAddr = flag.String("pprof", "",
			"serve net/http/pprof on this address; bind to localhost, the profiles are not public data")

		warmEvery = flag.Duration("warm-interval", httpapi.WarmInterval,
			"how long the cache warmer waits between passes; 0 disables it")
		warmNetworks = flag.String("warm-networks", "",
			"also warm these networks' govdao endpoints: a comma-separated list, or \"all\"")

		mcpConcurrent = flag.Int("mcp-concurrency", httpapi.MCPDefaultConcurrent,
			"MCP requests in flight per client address (0 = unlimited)")
		// Browser origins beyond this server's own host and loopback, which
		// are always accepted. Empty is right for a public deployment; a page
		// somewhere else embedding the endpoint is the case this exists for.
		mcpOrigins = flag.String("mcp-allowed-origins", "",
			`comma-separated browser origins the MCP endpoint accepts, e.g. "https://example.com" ("*" = any)`)
		// The name readers reach this instance by. Unset, the endpoint can
		// only compare a browser's Host and Origin against each other, which
		// deflects a page but not a client that writes its own headers. Set,
		// both are checked against a known answer. Worth setting on anything
		// bound to loopback, which is what the DNS-rebinding advisory is
		// about; guessing it here instead of asking would refuse every real
		// request behind a reverse proxy that rewrites Host.
		mcpPublicOrigin = flag.String("mcp-public-origin", "",
			`the origin this instance is reached by, e.g. "https://gnoscope.example"; enables strict Host and Origin checks on /mcp`)

		// The request log, and its own database file. Off by default: an
		// explorer somebody runs locally should not start writing a record of
		// its own use without being asked, and the deployment that wants one
		// says so in its unit file.
		//
		// A separate file, never a table in -db: the chain index is ~1.7 GB
		// that is rebuilt from the chain when it is wrong, and traffic is small,
		// unrecoverable and has a retention policy. Sharing would put reader
		// behaviour inside every backup of the index and tie a retention delete
		// to its write lock.
		trafficDB = flag.String("traffic-db", "",
			"SQLite path for the request log, e.g. gnoscope-traffic.db (empty = record nothing)")
		trafficRetention = flag.Int("traffic-retention-days", 30,
			"how many days of request rows to keep; 0 keeps them forever")

		// The lab's GitHub section, and its own database file, for the same
		// reasons as traffic above: small, entirely re-fetchable, and with
		// nothing in common with the chain index but the process.
		//
		// Off by default and needing two things to switch on, a path and a
		// token. The token needs no scopes (everything read is public) and is
		// not optional: GitHub gives 60 requests an hour without one, and one
		// pass over the seed list needs more than that, so an unauthenticated
		// instance would half-fill its tables rather than fail.
		githubDB = flag.String("github-db", "",
			"SQLite path for the GitHub lab, e.g. gnoscope-github.db (empty = section off)")
		githubToken = flag.String("github-token", "",
			"GitHub API token; falls back to $GITHUB_TOKEN. No scopes needed, everything read is public")
		githubRepos = flag.String("github-repos", "",
			"extra owner/name repositories to track, comma-separated, on top of the built-in list")
		githubEvery = flag.Duration("github-interval", ghlab.DefaultInterval,
			"how often to refresh the GitHub lab")
	)
	flag.Parse()

	// Initialize database
	db, err := store.NewDB(*dbPath)
	if err != nil {
		return fmt.Errorf("init db: %w", err)
	}

	var traf *traffic.Store
	if *trafficDB != "" {
		traf, err = traffic.Open(*trafficDB, *trafficRetention)
		if err != nil {
			return fmt.Errorf("init traffic db: %w", err)
		}
		defer traf.Close()
		retention := "forever"
		if *trafficRetention > 0 {
			retention = fmt.Sprintf("%d days", *trafficRetention)
		}
		log.Printf("traffic: recording requests to %s (retention %s); /api/traffic is public and aggregate-only",
			*trafficDB, retention)
	}
	defer db.Close()

	// The GitHub lab. Two switches, and a refusal that says which one is
	// missing: a section that is off for a reason nobody can read is a bug
	// report waiting to happen.
	var ghStore *ghlab.Store
	var ghSyncer *ghlab.Syncer
	ghOffReason := "the GitHub lab is off on this instance (-github-db unset)"
	if *githubDB != "" {
		token := *githubToken
		if token == "" {
			token = os.Getenv("GITHUB_TOKEN")
		}
		ghClient, cerr := ghlab.NewClient(token)
		switch {
		case cerr != nil:
			ghOffReason = "the GitHub lab has a database but no token: " + cerr.Error()
			log.Printf("github: %s", ghOffReason)
		default:
			ghStore, err = ghlab.Open(*githubDB)
			if err != nil {
				return fmt.Errorf("init github db: %w", err)
			}
			defer ghStore.Close()
			ghSyncer = ghlab.NewSyncer(ghStore, ghClient, splitList(*githubRepos))
			if *githubEvery > 0 {
				ghSyncer.Interval = *githubEvery
			}
			log.Printf("github: lab enabled, %s, refreshing every %s",
				*githubDB, ghSyncer.Interval)
		}
	}

	// Load config
	cfg, cfgSource, err := config.ResolveConfig(*configPath, *networkFlag, *indexerFlag, *rpcFlag)
	if err != nil {
		return err
	}
	log.Printf("networks %v (from %s)", cfg.IDs(), cfgSource)
	switch {
	case *blockHistoryDays < 0:
		log.Printf("block history: disabled (-block-history-days=%d); block charts will be empty", *blockHistoryDays)
	case *blockHistoryDays == 0:
		log.Printf("block history: full chain (-block-history-days=0)")
	default:
		log.Printf("block history: %d days (-block-history-days)", *blockHistoryDays)
	}

	// Rows outlive a network being retired from the config, so tell the database
	// which ones still count before anything reads an all-networks total.
	db.SetConfiguredNetworks(cfg.Networks)

	// Create per-network clients. The sync loop gets its own, on a budget sized
	// for catching up on history rather than for answering a page.
	clients := make(map[string]*indexer.Client)
	syncClients := make(map[string]*indexer.Client)
	for _, n := range cfg.Networks {
		clients[n.ID] = indexer.NewClient(n.Indexers()...)
		syncClients[n.ID] = indexer.NewSyncClient(n.Indexers()...)
	}

	// Initialize analyzer
	analyzer := analyzer.NewAnalyzer(db)

	// Recompute dependency edges when the extractor has changed. Reads only
	// stored source, so it costs nothing on the network and is a no-op once the
	// current version is recorded. Errors are logged, not fatal: a stale
	// dependency graph is not a reason to refuse to start.
	go func() {
		if err := analyzer.ReextractDependencies(); err != nil {
			log.Printf("re-extract dependencies: %v", err)
		}
	}()

	// Backfill the code search index from stored source.
	//
	// Every existing deployment has a full corpus and an empty index, and
	// without this the search would return nothing on exactly the instances
	// with the most to search, while looking like it worked. Same trap the
	// GRC20 ledger fell into by only ever seeing transactions synced after it
	// shipped.
	//
	// Reads only the local database, so it costs nothing on the network, and
	// it is skipped once the index is populated. Background and non-fatal: a
	// cold search index is not a reason to refuse to start.
	go func() {
		if n, err := db.BackfillCodeIndex(); err != nil {
			log.Printf("code index backfill: %v", err)
		} else if n > 0 {
			log.Printf("code index: backfilled %d files", n)
		}
	}()

	// Crawl the standard library, per network, in the background.
	//
	// Stdlib is never a MsgAddPackage, so the syncer cannot produce it and an
	// index without it is unusable for an editor or an agent: the first symbol
	// either looks up is in `strings` or `avl`. It is ~50 packages read once,
	// so this costs the node almost nothing and is skipped entirely when the
	// source is already held.
	//
	// Non-fatal and off the startup path: a chain with no stdlib crawled is a
	// thinner search index, not a reason to refuse to serve.
	for _, net := range cfg.Networks {
		if net.RPCURL == "" {
			continue
		}
		go func(n config.NetworkConfig) {
			// Reindex first: source already held but missing from the index is
			// the cheap case and needs no network at all.
			if got, err := db.BackfillStdlibIndex(n.ID); err != nil {
				log.Printf("[%s] stdlib reindex: %v", n.ID, err)
			} else if got > 0 {
				log.Printf("[%s] stdlib: reindexed %d files", n.ID, got)
			}
			held, _ := db.StdlibFileCount(n.ID)
			if held > 0 {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			c := &stdlibs.Crawler{
				DB: db, Fetch: httpapi.RPCFetcher{RPCURL: n.RPCURL},
				Network: n.ID, Workers: 4,
			}
			res, err := c.Run(ctx)
			if err != nil {
				log.Printf("[%s] stdlib crawl: %v", n.ID, err)
				return
			}
			log.Printf("[%s] stdlib: %d packages, %d files (%d skipped)",
				n.ID, res.Packages, res.Files, res.Skipped)
		}(net)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Written by the sync goroutines below, read by the sanity endpoint.
	syncHealth := syncer.NewRegistry()

	// Sync data from indexer (one goroutine per network)
	//
	// Every pass records its outcome, success or failure, so the sanity page
	// can say whether we are managing to read each chain. The log line alone
	// was not enough: a pass that fails every time for a day looks, from every
	// surface the explorer offers, exactly like one that never fails.
	if *syncOnStart {
		for _, n := range cfg.Networks {
			go func(net config.NetworkConfig) {
				sy := syncer.NewSyncer(syncClients[net.ID], db, analyzer, net.ID)
				sy.SetBlockHistoryDays(*blockHistoryDays)
				log.Printf("[%s] starting initial sync...", net.ID)
				err := sy.SyncAll(ctx)
				syncHealth.Record(net.ID, err)
				if err != nil {
					log.Printf("[%s] sync error: %v", net.ID, err)
				}
				log.Printf("[%s] initial sync complete", net.ID)

				ticker := time.NewTicker(30 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						err := sy.SyncAll(ctx)
						syncHealth.Record(net.ID, err)
						if err != nil {
							log.Printf("[%s] sync error: %v", net.ID, err)
						}
					}
				}
			}(n)
		}
	}

	// Keep the rollups warm.
	//
	// The aggregates behind the gas page, the bank page and the active-address
	// series scale with the chain and had reached 14, 5 and 16 seconds on
	// sapphire, against a 30-second write timeout. None can be indexed away —
	// attributing gas per realm means touching every call, and counting distinct
	// active addresses means touching every call, deploy and send — so they are
	// precomputed here instead of per request.
	//
	// Every five minutes, not every sync pass: the recompute takes the write
	// lock for a few seconds, and nothing here is a number a reader watches tick.
	// The gas and bank responses carry computed_at so the page can say how fresh
	// they are; the active-address series instead reads everything newer than
	// the build live, because a lagging newest bucket would disagree with the
	// live feed beside it.
	networkIDs := make([]string, 0, len(cfg.Networks))
	for _, n := range cfg.Networks {
		networkIDs = append(networkIDs, n.ID)
	}
	go func() {
		refresh := func() {
			start := time.Now()
			if err := db.RefreshRollups(); err != nil {
				log.Printf("rollups: %v", err)
				return
			}
			// Separate call rather than folded into RefreshRollups: this one is
			// an incremental upsert that can only move a row earlier, so a
			// failure here costs freshness and never correctness, and it should
			// not take the gas aggregates down with it.
			if err := db.RefreshFirstSeen(); err != nil {
				log.Printf("first_seen: %v", err)
			}
			// The address -> package index. UpsertPackage keeps it in step on a
			// live syncer, so this is the backfill: it is what fills the table
			// on a database built before it existed. Two hashes per path over a
			// few hundred paths, so it rides along rather than earning a ticker.
			paStart := time.Now()
			if n, err := db.RefreshPackageAccounts(); err != nil {
				log.Printf("package_accounts: %v", err)
			} else {
				// Its own duration, like the other passes that take the write
				// slot wholesale: a number to read rather than to guess at.
				log.Printf("package_accounts: %d paths indexed in %s", n,
					time.Since(paStart).Round(time.Millisecond))
			}
			// Third separate call, same reasoning, plus one of its own: this
			// one runs generated prose through the grounding gate, and a build
			// that rejects events is an emitter bug rather than a data problem.
			// Logging the count is how anyone finds out, since a rejected event
			// is simply absent from the feed and absence is invisible.
			if results, err := db.RefreshDiscoverEvents(networkIDs); err != nil {
				log.Printf("discover: %v", err)
			} else {
				for _, r := range results {
					if r.Inserted > 0 || r.Rejected > 0 {
						log.Printf("[%s] discover: %d new, %d built, %d rejected %v",
							r.Network, r.Inserted, r.Built, r.Rejected, r.Violations)
					}
				}
			}
			log.Printf("rollups refreshed in %s", time.Since(start).Round(time.Millisecond))
		}
		// Wait out the startup ANALYZE before the first build. Both take the
		// write lock, and racing it loses the whole refresh to SQLITE_BUSY —
		// which is silent, because the read path just falls back to computing
		// live and the page stays slow with nothing obviously broken.
		db.WaitBackground()
		refresh() // once at startup, so the first visitor is not the one who pays

		ticker := time.NewTicker(store.RollupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refresh()
			}
		}
	}()

	// Achievements: who has done what on chain, precomputed the same way and
	// for the same reason as the rollups above.
	//
	// On its own, slower timer. Every definition in pkg/achievements is a
	// GROUP BY over a whole history table, so the pass is in the same cost
	// class as the rollups, and a badge is a fact about the past that nobody
	// is watching arrive. Starts after the rollups have had the write lock
	// rather than racing them for it.
	go func() {
		db.WaitBackground()
		pass := func() {
			start := time.Now()
			if err := db.RefreshAchievements(); err != nil {
				log.Printf("achievements: %v", err)
				return
			}
			log.Printf("achievements refreshed in %s", time.Since(start).Round(time.Millisecond))
		}
		pass()
		ticker := time.NewTicker(*achievementEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pass()
			}
		}
	}()

	// The symbol index: what every package declares, extracted from the source
	// already in the database so search can answer "which package has
	// IterateByOffset" rather than only "which path contains that string".
	//
	// On its own timer rather than inside the sync pass. It is derived data
	// with no deadline, the skip for an unchanged package is one indexed lookup
	// and one hash, and a corpus walk has no business inside the loop that
	// keeps the chain current.
	go func() {
		db.WaitBackground()
		pass := func() {
			res, err := analyzer.RefreshSymbolIndex(ctx)
			if err != nil && ctx.Err() == nil {
				log.Printf("symbol index: %v", err)
			}
			if res.Indexed > 0 || res.Errors > 0 {
				log.Printf("symbol index: %s", res)
			}
		}
		pass()
		ticker := time.NewTicker(*symbolIndexEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pass()
			}
		}
	}()

	// Set up API routes
	api := httpapi.NewAPI(db, clients, cfg.Networks, analyzer)
	if *clearanceFlag != "" {
		raw, err := os.ReadFile(*clearanceFlag)
		if err != nil {
			// Fatal: an operator who passed -clearance and got a typo would
			// otherwise run with the empty default, which recommends nothing,
			// and conclude the feature is broken rather than the path is wrong.
			log.Fatalf("clearance %s: %v", *clearanceFlag, err)
		}
		var clearance discover.ClearanceConfig
		if err := json.Unmarshal(raw, &clearance); err != nil {
			log.Fatalf("clearance %s: %v", *clearanceFlag, err)
		}
		api.SetClearance(clearance)
		log.Printf("clearance: %d ours, %d other teams, %d treasury, %d denied",
			len(clearance.Accounts), len(clearance.OtherTeams), len(clearance.Treasury), len(clearance.Deny))
	}
	api.SetSyncHealth(syncHealth)
	api.SetShotUpstream(*gnoshotURL)
	if api.ShotsEnabled() {
		log.Printf("screenshots: /api/shot proxies %s", *gnoshotURL)
	}

	// A network pairs an indexer with an RPC, and nothing checked they serve the
	// same chain. Verify before serving rather than after someone reads a
	// balance from one chain beside history from another.
	//
	// Re-checked periodically because an endpoint can be repointed under a
	// running process — which is exactly what a mainnet launch on an existing
	// hostname does.
	// Account balances, on their own slower timer, and only for networks whose
	// RPC has been verified above.
	//
	// Same reason as the rollups: too slow to do per request. Different reason
	// for the interval, since this one is traffic against somebody else's node
	// rather than work on our own database. See pkg/httpapi/balances.go.
	go func() {
		db.WaitBackground()
		api.RunBalanceSweeper(ctx)
	}()

	go func() {
		check := func() {
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			api.VerifyRPCChains(ctx)
		}
		check()

		ticker := time.NewTicker(httpapi.RPCChainRecheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				check()
			}
		}
	}()
	mux := http.NewServeMux()

	// API routes
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// shots tells the frontend whether to put any realm <img> on the page.
		// It asks once and decides for the whole session, so a deployment
		// without a capture service never requests an image it cannot get.
		// Uptime is computed here rather than sent as a start timestamp alone
		// so a reader with a skewed clock still gets the right number; the
		// timestamp goes out beside it for anything that wants to tick it.
		httpapi.JSONResponse(w, map[string]any{
			"git_hash":       gitHash,
			"build_time":     buildTime,
			"version":        version,
			"go_version":     runtime.Version(),
			"started_at":     startedAt.Format(time.RFC3339),
			"uptime_seconds": int64(time.Since(startedAt).Seconds()),
			"shots":          api.ShotsEnabled(),
		})
	})
	mux.HandleFunc("GET /api/networks", func(w http.ResponseWriter, r *http.Request) {
		type netInfo struct {
			ID      string `json:"id"`
			Indexer string `json:"indexer,omitempty"`
			RPC     string `json:"rpc,omitempty"`
		}
		var nets []netInfo
		for _, n := range cfg.Networks {
			nets = append(nets, netInfo{ID: n.ID, Indexer: n.IndexerURL, RPC: n.RPCURL})
		}
		httpapi.JSONResponse(w, nets)
	})
	api.RegisterRoutes(mux)

	// SSE live feed
	httpapi.InitLiveFeeds(cfg.Networks, clients)
	mux.HandleFunc("GET /api/live", httpapi.LiveFeedHandler())

	// The MCP endpoint, for agents. Registered on the same mux it dispatches
	// into, which is what keeps a tool's answer identical to the REST
	// endpoint's; the dispatcher is set below, once the cache exists.
	mcp := api.NewMCPServer(httpapi.NewIPLimiter(*mcpPerMinute, *mcpConcurrent), gitHash)
	if *mcpPublicOrigin != "" {
		mcp.SetPublicOrigin(*mcpPublicOrigin)
		log.Printf("mcp: strict host and origin checks against %s", *mcpPublicOrigin)
	}
	if *mcpOrigins != "" {
		origins := strings.Split(*mcpOrigins, ",")
		for i := range origins {
			origins[i] = strings.TrimSpace(origins[i])
		}
		mcp.SetAllowedOrigins(origins)
		log.Printf("mcp: also accepting browser origins %v", origins)
	}
	// Both methods named rather than one method-less pattern. A pattern with
	// no method conflicts with the SPA's "GET /" and Go's mux panics at
	// registration: "matches fewer methods than /mcp, but has a more general
	// path pattern". GET is registered only so the handler can answer it with
	// a 405 and an Allow header, which is what a client opening the URL
	// looking for an SSE stream needs to be told.
	mux.HandleFunc("POST "+httpapi.MCPPath, mcp.Handle)
	mux.HandleFunc("GET "+httpapi.MCPPath, mcp.Handle)
	log.Printf("mcp: %s serves %d read-only tools, %d req/min and %d concurrent per address",
		httpapi.MCPPath, httpapi.MCPToolCount(), *mcpPerMinute, *mcpConcurrent)

	// Frontend: SPA handler serves index.html for all non-API routes
	frontend, err := web.Handler(web.Options{AnalyticsScript: *analyticsScript, Shots: api.ShotsEnabled()})
	if err != nil {
		return err
	}
	if *analyticsScript != "" {
		log.Printf("analytics: frontend loads %s", *analyticsScript)
	}
	mux.HandleFunc("GET /", frontend)

	// Cache outermost, so a hit costs nothing beyond the network-name check —
	// and compression *inside* it, so what the cache stores is already
	// compressed and a hit does not re-gzip 3.5 MB of JSON per reader. The
	// cache key carries the negotiated encoding to keep those two facts
	// consistent (see cacheKey).
	// WithServerTiming sits inside the cache on purpose: the number it reports
	// is the cost of computing an answer, so its presence on a response means
	// somebody paid for that answer and its absence means they did not.
	cache := httpapi.NewResponseCache(httpapi.CacheTTL)
	// The read counter is outside the cache, and that is not a preference: a
	// cached answer never reaches the handler, so counting deeper would count
	// the first reader of a realm and miss everyone who followed. The more a
	// realm was read, the less it would appear to be read.
	views := httpapi.NewViewCounter(db)
	// WithAccessLog outermost, and for the same reason WithRealmViews is: a
	// cached answer never reaches a handler, so a counter placed deeper would
	// count the first reader of a page and miss everyone who followed. It is
	// also the only layer that sees the wire bytes, the final status, and the
	// total a reader actually waited.
	handler := httpapi.WithAccessLog(traf, mux, selfHost(*mcpPublicOrigin),
		httpapi.WithRealmViews(views,
			httpapi.WithResponseCache(cache,
				httpapi.RejectUnknownNetwork(cfg.Networks,
					httpapi.WithServerTiming(
						httpapi.WithCompression(mux))))))
	go views.Run(ctx)
	go traf.Run(ctx) // nil-safe

	// A tool call goes through the cache, not straight at the mux: the reads
	// behind get_realm_state and the analytics endpoints are the expensive
	// ones, and an agent asking the same question twice should pay for it
	// once. Compression is skipped on the way in, since an internal request
	// sends no Accept-Encoding, so nothing is gzipped only to be gunzipped.
	mcp.SetDispatcher(httpapi.WithResponseCache(cache, mux))

	// The warmer drives `handler`, the same stack a browser hits, so the
	// entries it fills are keyed exactly as a reader's request would key them.
	// Pointing it at `mux` instead would compute everything and cache nothing.
	//
	// Started after the background DB work settles, because a warmer racing a
	// cold sync would cache the answers of a half-populated database for a
	// whole TTL. See pkg/httpapi/warmer.go for why this exists at all.
	var warmer *httpapi.Warmer
	if *warmEvery > 0 {
		warmer = httpapi.NewWarmer(handler, httpapi.ParseWarmNetworks(*warmNetworks, cfg.IDs()), *warmEvery)
		api.SetWarmer(warmer)
		go func() {
			db.WaitBackground()
			warmer.WaitReady(ctx, syncHealth, httpapi.WarmReadyGrace)
			warmer.Run(ctx)
		}()
	} else {
		log.Printf("warmer: disabled (-warm-interval=0); readers pay the cold cost")
	}
	api.SetResponseCache(cache)
	api.SetViewCounter(views)
	api.SetTraffic(traf)
	api.SetGitHub(ghStore, ghOffReason)
	if ghSyncer != nil {
		go ghSyncer.Run(ctx)
	}

	// pprof on its own listener rather than on the public mux: a profile says
	// more about this process than any page does, and the difference between
	// "diagnosable" and "exposed" should be which interface it binds to, not a
	// path nobody guesses.
	if *pprofAddr != "" {
		go func() {
			log.Printf("pprof: listening on %s", *pprofAddr)
			pprofSrv := &http.Server{Addr: *pprofAddr, Handler: http.DefaultServeMux}
			if err := pprofSrv.ListenAndServe(); err != http.ErrServerClosed {
				log.Printf("pprof: %v", err)
			}
		}()
	}

	srv := &http.Server{
		Addr:         *listenAddr,
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("shutting down...")
		cancel()
		srv.Shutdown(context.Background())
	}()

	log.Printf("gnoscope listening on %s (networks: %v)", *listenAddr, cfg.IDs())
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

// selfHost extracts the hostname from -mcp-public-origin, which is the one flag
// that already names how this instance is reached.
//
// Used to drop own-origin referers from the traffic log: a link from one page of
// this app to the next says nothing about where readers come from, and left in
// it would outnumber every real inbound link.
func selfHost(origin string) string {
	if origin == "" {
		return ""
	}
	u, err := url.Parse(origin)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// splitList turns a comma-separated flag into a trimmed, non-empty slice.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
