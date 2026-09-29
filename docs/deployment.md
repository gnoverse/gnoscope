# Deployment

One static binary and one SQLite file. No runtime dependencies.

## Flags

| flag | default | description |
|---|---|---|
| `-listen` | `:8888` | listen address |
| `-db` | `gnoscope.db` | SQLite database path |
| `-config` | — | JSON config file, for multiple networks |
| `-network` | — | single network ID |
| `-indexer` | — | single network tx-indexer GraphQL URL |
| `-rpc` | — | single network RPC URL, needed for account balances |
| `-sync` | `true` | run the background sync |
| `-block-history-days` | `90` | days of block history to backfill. `0` backfills the full chain; a negative value stores no blocks at all |
| `-analytics-script` | — | URL of an analytics script to load in the frontend. Empty serves no third-party script at all |
| `-gnoshot` | — | base URL of a [gnoshot](https://github.com/gnoverse/gnoshot) capture service. Empty draws no realm screenshots at all |
| `-traffic-db` | — | SQLite path for the request log, e.g. `gnoscope-traffic.db`. Empty records nothing |
| `-traffic-retention-days` | `30` | days of request rows to keep. `0` keeps them forever |

## Configuration

Config comes from exactly one of these, in order:

1. `-config <path>`
2. the single-network flags (`-indexer`, optionally `-network` and `-rpc`)
3. `networks.json` in the working directory
4. built-in defaults

Combining `-config` with the single-network flags is an error, as is passing
`-network` or `-rpc` without `-indexer`. Startup logs which source was used and the
resulting network IDs:

```
networks [mainnet onyx] (from config file)
```

Check that line, or `/api/networks`, after any config change. A wrongly configured
instance starts cleanly, syncs real data and looks healthy — it is just the wrong
chain.

### Config file

```json
{
  "networks": [
    {
      "id": "onyx",
      "indexer": "https://indexer.onyx.testnets.gno.land/graphql/query",
      "rpc": "https://rpc.onyx.testnets.gno.land"
    },
    {
      "id": "mainnet",
      "indexer": "https://indexer.gno.land/graphql/query",
      "rpc": "https://rpc.gno.land"
    }
  ]
}
```

`id` and `indexer` are required for every network, and IDs must be unique. `rpc` is
optional but **account balances need it** — an address on a network with no `rpc`
shows no balance.

Give each network an `rpc` if you can. Balance lookups resolve against a single
network, and with no network filter they fall back to the first configured network
that has one.

### Fallback endpoints

`indexers` and `rpcs` list additional interchangeable endpoints for the same
chain:

```json
{
  "id": "mainnet",
  "indexer": "https://indexer.gno.land/graphql/query",
  "indexers": ["https://indexer.example.org/graphql/query"],
  "rpc": "https://rpc.gno.land",
  "rpcs": ["https://rpc.example.org"]
}
```

They buy two different things, and the second is the one that matters:

- An endpoint that is **down** is skipped.
- An endpoint that is merely **behind** is overtaken. This is the failure that
  is worth configuring for, because it does not look like a failure: gno.land's
  mainnet indexer once sat at block 785 while the chain was at 36,000, answering
  every query promptly and correctly for the 785 blocks it knew about. Nothing
  errored; the explorer simply reported a stalled chain as a healthy one.

So endpoints are ranked by how far along they are, re-checked every couple of
minutes, and the singular `indexer`/`rpc` is just the first entry — listing a
stale endpoint first costs nothing.

Members must serve the same chain. The first entry that can identify itself
defines which chain that is (by chain ID *and* the hash of block 1, so a reset
network is not mistaken for the original), and any endpoint disagreeing with it
is never selected — however healthy or far along it is. A fast, healthy, wrong
chain is the worst member a pool can have: `gnoland-1` and `gnoland1` are one
hyphen apart.

## Analytics

Off by default, and deliberately: the frontend is one file compiled into the
binary and shared by every deployment, so a tag written into it would make
everyone running gnoscope report to one account. `-analytics-script <url>`
adds a single `<script async src="…">` to the `<head>` at startup:

```
gnoscope -analytics-script https://scripts.simpleanalyticscdn.com/latest.js
```

The URL must be an absolute `http(s)` URL; anything else is a startup error
rather than a broken tag served to every reader. Startup logs the line
`analytics: frontend loads <url>` when one is configured, and the page's ETag
changes with it, so readers holding the previous build get the new document
instead of a cached one.

[Simple Analytics](https://www.simpleanalytics.com) is what the public instance
uses. It sets no cookie and writes nothing to the device, so it needs no consent
banner. Any provider shipping a single self-contained script drops in the same
way.

Two things to know before reading the numbers, both properties of the frontend
rather than of the provider:

- **A pageview is a `pushState`.** The script patches `pushState` and listens
  for `popstate` and `hashchange` (verified against `latest.js` v11,
  2026-09-21), which is exactly what `navigate()` calls. Tab switches inside a
  realm page use `replaceState` and are correctly *not* counted as separate
  views.
- **The network is in the query string, and query strings are dropped.**
  `/realms?network=mainnet` and `/realms?network=onyx` arrive as one page. Per
  network figures need the provider's own parameter allow-list, not a code
  change here.

## The request log and /traffic

`-analytics-script` above is a browser-shaped instrument. It sees a person
opening a page, and it is blind to `/api/*`, to `/mcp`, to crawlers, to every
4xx and 5xx, and it drops the query string, so every network collapses into one
row. The request log is the server's own answer to the same question.

```
gnoscope -traffic-db /root/gnoscope-traffic.db -traffic-retention-days 30
```

Off by default: an explorer somebody runs locally should not start writing a
record of its own use without being asked.

**Its own database file, never a table in `-db`.** The chain index is ~1.7 GB
that is rebuilt from the chain when it is wrong; traffic is small, not
reconstructible from anywhere, and has a retention policy. Sharing one file
would put reader behaviour inside every backup of the index and tie a retention
delete to its write lock.

### What is recorded, and what is refused

One row per served request: the matched route *pattern* (so the column is
bounded by the routing table rather than by whatever URLs a crawler invents),
the wildcard part of the path, the network, the status, wire bytes, total and
handler duration, cache state, the MCP tool name, the referer **host**, and a
four-way client class.

What never reaches disk: the IP, the user-agent string, the referer path, the
query string, and any request body. The stored identity is
`HMAC(key-of-the-day, ip + ua)`, truncated to 16 hex characters. The key is 32
bytes from `crypto/rand`, lives only in process memory, is replaced at the first
request after midnight UTC, and is never written anywhere, so nobody, including
whoever holds the database file, can reverse an id to an address or link
yesterday's rows to today's.

The deliberate cost: a reader active across midnight is counted twice, and
"returning visitor over a week" is unanswerable. Neither is worth keeping an
address on disk for.

### Reading it

`GET /api/traffic?window=24h|7d|30d|90d&host=&who=&kind=&errors=1&limit=`
returns the whole dashboard in one response: totals, a time series, and top-N
breakdowns by page, realm, API route, MCP tool, network, client class, referer
host, status, cache state and **host**, plus the slowest routes ranked by p95.

`what` selects the kind of request, and defaults to **page views**. One refresh
of this single-page app fires a document load plus roughly ten XHRs, so an
unfiltered request count moves in jumps of ten and answers "how chatty is the
frontend" rather than "how many people looked". `all requests` is one pill away.

A page view is reported by the frontend through `navigator.sendBeacon` to
`/api/traffic/pageview`, not inferred from the document fetch. Navigation here
is `history.pushState`, so moving between pages sends no document request at
all: without the beacon, in-app navigation is invisible and only cold loads
count. `sendBeacon` rather than `fetch` because the report fires at the moment
the page changes, which is the moment a browser cancels in-flight requests.

Page views also carry what they are **about**: `realms_viewed`,
`addresses_viewed`, `txs_viewed` and `assets_viewed` fold every page about one
subject into a single row, so a realm read through its overview and its source
browser is one realm and not two URLs. `page_kinds` answers the opposite
question: not which realm, but what readers come here to do.

Requests for software this server does not run (`/wp-login.php`, `/.env`,
`/.git`, and about thirty more prefixes) are classified `probe` and excluded
from every other view. They are recorded, because being scanned is worth
seeing, and kept apart because otherwise they bury the mistyped realm paths in
the not-found panel, which are the ones that say something. The list is in
`pkg/traffic/classify.go` and is meant to grow.

`who` selects a client class: `noncrawlers` (the default), `all`, `crawlers`,
`people`, `agents`, `unknown`. The default is everything-but-crawlers rather
than `all`, because crawlers outnumbered browsers here within a day of the log
existing; and it is not `people`, because a tool call is never a browser and
that would empty the MCP panel permanently. `bots=1` from the previous
vocabulary still works and means `who=all`.

Every filter is reflected in the page's query string, so a filtered view is
shareable and the back button works through a sequence of filter changes.

Referrers are stored as the **whole link**, minus tracking parameters
(`utm_*`, `fbclid`, `gclid`, X's `s` and `t`) and minus any embedded
credentials, so one tweet and one aggregator thread are two rows rather than
both being "twitter.com". ⚠️ The traffic page is public, so referring URLs are
published: a private path in somebody's referer becomes visible here. Return
`u.Hostname()` from `RefererURL` to go back to host-only.

`host` filters to one name. The page defaults it to the name it was served
from, so "this site" means this site rather than every name the server answers
to. The hosts panel is always computed across every host, so it can act as the
filter's own control. Rows written before the host column existed show as
`(not recorded)` and are excluded by any host filter; the page says how many
rather than letting the totals shrink unexplained.

**Public, and aggregates only.** There is no endpoint that returns a request
row, and adding one would undo the design above: three rows carrying a visitor
id, a timestamp and a referer re-identify a reader that the hashing went to some
trouble not to keep.

Self-declared crawlers are stored but excluded from the default view, because a
crawler is real load and is not a reader. `bots=1` brings them back.

`GET /api/traffic/health` reports the writer itself. **`dropped` climbing is the
one number worth alerting on**: the buffer is capped at 50,000 unflushed rows,
and past that records are discarded rather than allowed to grow without bound,
so a climbing `dropped` means the dashboard is now understating traffic.

### The other services on the host

This covers gnoscope and nothing else. Everything else behind the same reverse
proxy needs the proxy's own access log:

```
log {
  output file /var/log/caddy/<site>.log {
    roll_size 50MiB
    roll_keep 10
  }
  format json
}
```

Set `roll_size`/`roll_keep` when you add it, not after: an access log with no
rotation is the one that reaches 141 MB unnoticed.

## The GitHub lab and /lab/github

The off-chain section: contributors and pull requests on a curated set of
ecosystem repositories, plus discovery of projects that depend on gno and
nobody curated. Off by default and needing two things, a database and a token.

```
gnoscope -github-db /root/gnoscope-github.db -github-token ghp_... -github-interval 3h
```

`-github-token` falls back to `$GITHUB_TOKEN`. **It needs no scopes**:
everything read is public, and a classic token with nothing ticked already
lifts the budget from 60 requests an hour to 5,000. Granting more buys nothing
and risks something.

Its own database file, for the same reasons as the request log above: small,
entirely re-fetchable from GitHub, and with nothing in common with the chain
index but the process. Delete it and the next pass rebuilds it.

A pass costs about 130 core requests and 20 search requests and takes roughly
two minutes, most of it the deliberate 2.5s spacing between search calls
(measured 2026-09-29, 23 seeds and 8 discovery queries). Discovery caps new
repositories at 120 a pass, so a cold start reaches its full picture over two
or three passes rather than spending an hour's budget in ninety seconds.

**A token is not optional.** Without one the section stays off and says so.
Sixty requests an hour is fewer than one pass needs, so an unauthenticated
instance would not fail, it would half-fill its tables and serve a picture of
the ecosystem missing whichever repositories happened to come last.

### A token that can read private repositories

A GitHub search runs as the token, so a token with private access returns
private repositories in its results. This is handled and worth knowing about
anyway when picking which token to use:

- Repository search carries `is:public`.
- Code search has no such qualifier, and appending one silently matches
  nothing, so its hits are filtered on the hit's own `repository.private` and
  `repository.visibility` instead.
- Nothing private is ever written to the database, rather than written and
  filtered at read time.

The cheapest way to hold that line is a token that has nothing private to
offer. A fine-grained token scoped to public repositories only is the right
default here.

## Choosing network IDs

The `id` labels the network and keys every row belonging to it. It is not the chain
ID and nothing ties the two together.

**Renaming an ID orphans the data stored under the old one.** Since sync cursors
are derived from the highest stored height per network, a renamed network looks
brand new and re-syncs from genesis. To relabel while keeping history, update the
`network` column across all nine network-scoped tables (`packages`,
`package_files`, `dependencies`, `calls`, `msg_runs`, `bank_sends`,
`transactions`, `blocks`, `proposers`) — that preserves the cursor too. Back up
first.

## Reset-prone networks

Portal-loop and staging style chains restart from a low height. gnoscope detects
this by fingerprinting block 1 (chain ID plus hash) per network: when the
fingerprint changes, that network's rows are discarded and it re-syncs from the new
genesis. Chain ID alone is not enough — a reset chain keeps its chain ID.

A lagging indexer replica reporting a tip below the stored height is *not* treated
as a reset. It logs a warning and changes nothing.

## Running it

Any process supervisor works. A systemd unit needs nothing special:

```ini
[Unit]
Description=gnoscope
After=network.target

[Service]
ExecStart=/usr/local/bin/gnoscope --listen 127.0.0.1:8888 --db /var/lib/gnoscope/gnoscope.db --config /etc/gnoscope/networks.json
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

Put it behind a reverse proxy for TLS. `/api/live` is Server-Sent Events, so
disable response buffering for it (nginx: `proxy_buffering off`); Caddy's default
`reverse_proxy` needs no change.

## Docker

A multi-arch image is published to GHCR on every push to `main`:

```bash
docker run -p 8888:8888 \
  -v gnoscope-data:/data \
  ghcr.io/gnoverse/gnoscope:main \
  --listen :8888 --db /data/gnoscope.db --config /data/networks.json
```

Prefer the image over hand-copied binaries. A deployment updated by scp drifts, and
a stale binary is hard to notice: check `/api/version`, and if `git_hash` is `dev`
the build carries no version information at all.

Readers see the same four facts without curling anything: the foot of the nav
rail carries the version, the build date with its age, and the uptime, and a
reader on a page that looks wrong has a "report a bug" link there that opens a
GitHub issue with all of it, the network and the page already filled in.

## Operating notes

- **Sync runs every 30s per network**, incrementally, from the highest stored
  height. Restarts do not re-sync from scratch.
- **A full first sync of a busy chain is expensive** in time and indexer requests.
  Adding a network to an existing instance triggers one for that network only.
- **Use a local indexer where you have one.** It is faster and avoids depending on
  a public endpoint.
- **The database grows with source code**, since full `.gno` file bodies are stored.
  Expect tens of MB per busy network.
- **Blocks cost roughly 130 bytes each** including their index — about **430 MB per
  network** at mainnet's ~3.3M blocks, on top of the source-code storage above.
  `-block-history-days` bounds **the initial backfill depth only** — how far back
  it walks from the tip before stopping. It does not bound total storage: head
  sync keeps appending new blocks at the tip for as long as the process runs, and
  nothing ever deletes a stored block, so a server run for a year at
  `-block-history-days=90` ends up holding a year of blocks *plus* the original 90
  days, not 90 days. The default of 90 keeps the initial backfill to what the
  dashboards' default window actually shows, `0` backfills the whole chain, and a
  negative value declines block storage entirely (the block charts then render
  empty). The startup log line says which mode is in effect.
  **Lowering the flag later reclaims nothing** — existing rows are never pruned.
  **Raising it** (including to `0`) past the depth an earlier, capped backfill
  already completed at makes that backfill resume from where it stopped, walking
  further back automatically; the change takes effect on the next sync pass, no
  manual intervention needed.
- **The block backfill runs automatically** on startup, bounded per pass so it
  cannot stall the rest of the sync. It walks backward from the tip and takes
  roughly 16 minutes to cover a mainnet-sized chain at `-block-history-days=0`.
  It logs its position each pass and its termination reason — genesis, a pruned
  indexer floor, or the configured depth. Until it finishes, block charts cover
  only recent history and say so; `/api/blocks/coverage` reports the stored range
  and whether it is complete.
- **WAL mode is on**, so back up the `-wal` and `-shm` files alongside the database,
  or take the backup with the service stopped.

## Health checks

```bash
curl -s localhost:8888/api/version    # which build
curl -s localhost:8888/api/networks   # which chains
curl -s localhost:8888/api/stats      # is data actually landing
```

In the logs, per-pass `synced N packages` lines with small counts mean incremental
sync is working. Large counts on every pass mean it is re-syncing everything, which
is the signature of a build predating incremental sync.


## Realm screenshots

Off unless `-gnoshot` names a capture service. With one, `/api/shot` turns a
package path into a picture of that realm's gnoweb page, and the frontend puts
it on the realm page and in the `/realms` and `/packages` listings.

```bash
# on the same box, two workers, warmed from this explorer's own path list
gnoshot serve -root /var/lib/gnoshot -source http://127.0.0.1:8888
gnoscope -gnoshot http://127.0.0.1:8890
```

Three things worth knowing before turning it on:

- **A network needs a `gnoweb` in its config** to be photographable. The
  built-in defaults set it for `gnoland1` and `onyx`; a network without one
  simply gets no pictures, rather than an error on every row.
- **The proxy is on this origin on purpose.** A listing opens fifty thumbnails,
  and pointing them at another host costs a DNS lookup and a TLS handshake
  before the first byte of the first one. It is also the only place the
  parameter validation can live.
- **The capture service being down is not this being down.** `/api/shot`
  answers 503 with `Cache-Control: no-store`, and the page draws its own tile.
  Nothing else on the page changes.

The frontend learns whether the feature is on from a flag injected into the
document at serve time, not from `/api/version`: the first listing row is drawn
before that request comes back, and a page that grows a column afterwards is
worse than either outcome on its own.

### Link previews

With a capture service configured, a `/realm/<path>` URL is also the **one
route that gets its own document**: the head carries `og:title`,
`og:description`, `og:url` and an `og:image` pointing at that realm's `og` rung,
so a link pasted into Slack, Discord or X previews as a picture of the realm.

Everything else still gets the single precomputed, pre-gzipped document, byte
for byte, and a test holds that line. The exception exists because a crawler
does not run the SPA, and the SPA is where every other answer lives.

It costs a per-path ETag and a BestSpeed gzip **on that branch only**. Without
`-gnoshot` the text half is still served and the card is downgraded to a plain
`summary`, rather than advertising an image that is not there.
