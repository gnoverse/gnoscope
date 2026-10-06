# AGENTS.md

Conventions and invariants for working in this repo. Read this first; it is meant
to stay stable. For what the system *is* and how it behaves, see [`docs/`](docs/)
— that changes far more often and is deliberately kept separate.

## What this is

A single Go binary that serves a gno.land block explorer with an embedded
frontend. No build step for the frontend, no Node.js in the shipped artifact.

**This was `mygnoscan` until 2026-09-28.** Prose that talks about what the tool
did in the past may still say so, and should: a sentence about April is about
mygnoscan. Anything describing it *now* says gnoscope. The rename, and the
redirects that make every old link keep working, are in
[`docs/rename.md`](docs/rename.md).

## Layout

```
main.go       entrypoint: flags, wiring, HTTP routes
config.go     network configuration and flag resolution
indexer.go    GraphQL client for the tx-indexer API
db.go         SQLite storage: schema, migrations, all queries
analyzer.go   import extraction from .gno source, dependency graph building
pkg/ghlab/    the GitHub lab: ecosystem repos, PRs, contributors, discovery
syncer.go     background sync from tx-indexer into SQLite
api.go        REST API handlers
ws.go         SSE live feed (polls the indexer, fans out to browsers)
frontend/     static HTML/JS/CSS, embedded with go:embed
e2e/          browser tests (Node, development only — never in the binary)
docs/         project documentation
```

See [`docs/architecture.md`](docs/architecture.md) for how these fit together.

## Invariants

Break these and things go wrong in ways that are hard to see:

- **Everything is network-scoped.** Rows in `packages`, `package_files`,
  `dependencies`, `calls`, `msg_runs`, `bank_sends` and `transactions` all carry a
  `network` column, and it is part of the primary key or unique constraint. Any
  new query, join or aggregate must filter or group by `network`, otherwise data
  from two chains gets silently mixed. Joins on `pkg_path` alone are the usual way
  this goes wrong. There are two exceptions. One is `pkg/ghlab`, whose subject is GitHub and
  not a chain: a repository is the same repository whichever network its realms
  end up on, and a `network` column there would be two rows that must always
  agree. It lives in its own database file and its endpoints take no `network`.
  The other is `blobs`, file bodies keyed by their sha256: a hash names bytes,
  not a chain, so it has no `network` to filter by. Reach it only through
  `submission_files`, which is network-scoped and says where a body was
  published.
- **Nothing private reaches `gh_repos`.** A GitHub search runs as the token, so
  an operator's token returns private repositories it can read: measured
  2026-09-29, the first discovery run here surfaced eight of them, with the
  matching file paths ready to print in a public page's evidence column.
  Repository search is fixed with `is:public`; **code search has no such
  qualifier** and appending one matches nothing instead of erroring, so its hits
  are filtered on `repository.private`. `UpsertRepo` refuses a private row rather
  than skipping it. Same shape as the two invariants above: quiet, load-bearing,
  and broken invisibly, because the leaked row looks exactly like a good one.
- **Sync is incremental and cursor-driven.** Cursors are derived from the highest
  stored `block_height` for that network, not stored separately. Anything that
  deletes or rewrites rows moves the cursor as a side effect.
- **`network` IDs are labels, not chain IDs.** They name a configured network and
  key its data. Renaming one orphans its existing rows.
- **The frontend builds DOM, never HTML strings.** Use the `el()` helper. There is
  no `innerHTML` with interpolated data anywhere, and it should stay that way —
  the explorer renders on-chain content, all of which is attacker-controlled.
  This is also why the optimistic-UI cache stores payloads and not rendered
  markup: a revived `innerHTML` would be the one place this stopped being true.
- **A generated explanation may not add a fact.** Discover's layers 2 and 3 are
  written from a closed `facts` map and `pkg/discover` fails the build if they
  say anything that map does not contain: an ungrounded number, a name from
  another row, an unlicensed superlative, a forward-looking claim, jargon in the
  headline. Same kind of rule as "the frontend builds DOM, never HTML strings":
  quiet, load-bearing, and broken invisibly, because a fluent sentence asserting
  something false reads exactly like a correct one.
- **`docs/glossary.md` defines the words, and nothing else does.** Nineteen terms,
  embedded and served at `GET /api/glossary`. If a tooltip, a tile, an empty state
  or a feed needs to explain what "parked" or "unique callers" means, it reads the
  glossary; it does not write its own sentence. Two places defining one word is how
  a reader gets told two different things, and `pkg/glossary` fails the build if a
  gloss is restated anywhere in the repo. Adding a term is a docs change, not a
  code change.
- **A block height is drawn by `blockWithAge`, never by `blockLink` alone.**
  A bare height answers "which block" and leaves "when" to a second page load,
  which is the question a reader of a table actually had. The shape is
  `1,234 (3d)`, and the age carries `data-age` so the 10s ticker refreshes it
  on a tab left open. The exception is a column that already has a timestamp
  beside it (`/blocks`, the home tx feed, the tx detail table): there the age
  is a second way of saying the same thing. `e2e/tests/block-age.spec.js`
  holds the line. An endpoint that returns a height and no time is the bug to
  fix, not a reason to drop back to `blockLink`.

- **An `apiSWR` render function runs more than once, and must be synchronous.**
  Loaders paint cached data first and fresh data second, and a list carrying an
  optional path (one with a `fallback`) paints a third time: once the required
  paths land with the optional half at its fallback, once more when the
  stragglers arrive. So a render has to rebuild its container from scratch
  (appending to something a previous pass filled is how you get two of
  everything) and must not `await` (an await reopens the interleaving that
  rebuilding exists to close). Kick long work off in an async IIFE with a
  generation guard, the way `renderTsCharts` does.
- **A `fallback` on an `apiSWR` path means "may fail *and* may be late".**
  Declaring one is the only thing that keeps a slow endpoint off a page's
  critical path, so declare it for anything best-effort. Measured against
  production on 2026-09-29: a cold `/api/inert/package` took 34s and the realm
  page, which had its own payload in 26ms, rendered nothing at all for those
  34s. `e2e/tests/slow-optional-endpoint.spec.js` holds the line by stalling
  that endpoint for 8s and asserting the header, the tab strip and the info
  table are all up inside 3s.
- **Anything attached to a painted row has to survive that row being replaced.**
  The corollary of the above, and the one that is easy to miss: the fresh pass
  throws away the rows the cached pass drew, so a one-off applied to them (a
  filter hiding rows, a sort reordering them, a highlight) is gone a moment
  later, leaving a control that says it is doing something it is not. The cache
  is `sessionStorage`, so the second render only happens on the *second* visit
  to a page, and on localhost the two often collapse into one, which makes this
  a bug that passes in isolation and fails in the suite. `enhanceTables` is the
  pattern to copy: register the work with `registerTableRestore` and let it be
  re-applied whenever a row turns up without the `data-tstate` mark. Delay the
  fresh fetch with `page.route` to test it, or the test proves nothing.
- **The nav is described twice, and a test keeps the two identical.** The rail
  is static HTML in `index.html`; the `NAV` table in the script beside it drives
  the `.pagenav` section strips and `route()`'s active-state bookkeeping. The
  rail is not generated from the table because it must not depend on the
  script below it having run at all. Add an entry to both, in the
  same order, or `TestRailMatchesNavTable` fails. Left to drift it fails
  silently: a rail entry missing from the table navigates fine and simply has no
  section strip. The corollary is that a link in the rail that does *not* route
  (the about block's commit, changelog and bug links) has to live outside
  `<nav>`, where that test cannot see it; `e2e/tests/rail.spec.js` asserts it
  stays there.
- **Nothing third-party is on the critical path, and nothing may go back on
  it.** d3, Chart.js, ECharts and echarts-gl are 676 KB compressed and ~2.2 MB
  parsed from two external origins; they used to be four plain `<script src>`
  tags above the app script, so every page paid for them before a line of this
  app's own code ran, including the many that draw no chart. They are fetched on
  first use now (`loadLib`). A drawing function starts with either
  `if (!libReady('chart')) return libRetry('chart', () => sameCallAgain())` when
  it owns and clears its container, or `libBlock(into, 'echarts', draw)` when
  its caller appends siblings after it and the block has to hold its place. Do
  not add a fifth `<script src>`.
- **Never commit the built binary.** `gnoscope` and `*.db` are gitignored.

## Conventions

- **Go**, latest stable. Toolchain version comes from `go.mod`.
- **Formatting is enforced.** `gofmt -l .` must be empty; CI fails otherwise.
- **Tests are table-driven** where there is more than one case, with a temp
  SQLite file rather than a mock. The driver (`modernc.org/sqlite`) is pure Go, so
  a real database works everywhere including CI.
- **Commits are conventional and single-line**: `feat:`, `fix:`, `docs:`, `ci:`,
  `refactor:`, `test:`, `chore:`. No trailing co-author lines.
- **`make` targets** are the entry points: `test`, `e2e`, `run`, `install`, `dev`.
  `test` is Go only. `e2e` drives a headless browser and is the only thing in the
  repo that needs Node; changing anything in `frontend/` should run it.
- **Errors go up, not into logs.** The exception is the sync loop, which logs and
  continues per-item so one bad package cannot stall a whole pass. Do not copy
  that pattern into query paths — several aggregate readers currently swallow
  errors and return zeroes, and that is a known bug, not a style to follow.

## Before opening a PR

```bash
gofmt -l .        # must print nothing
go vet ./...
go test ./...
```

CI runs the same three plus `golangci-lint` and a multi-arch Docker build. See
[`CONTRIBUTING.md`](CONTRIBUTING.md).

## Gotchas

- `db.go` is large and mixes schema, migrations and every query. Adding to it is
  fine; just know that queries are not grouped by domain yet.
- The startup migration path rebuilds tables (`packages_new` and friends) to add
  columns SQLite cannot add in place. It runs against real user data on every
  deploy, so treat changes there as high-risk.
- `network` is string-concatenated into a few aggregate queries rather than bound
  as a parameter. It is quote-escaped, so not currently exploitable, but do not
  add more of it — bind parameters.
- Some `/api/*` endpoints query the indexer live on every request instead of
  reading local SQLite. Check which before assuming an endpoint is cheap.
