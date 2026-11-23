# The indexer request meter: the number #525 is waiting on

Issue: https://github.com/gnoverse/gnoscope/issues/525 (the open question, after #526 and #528)
PR: https://github.com/gnoverse/gnoscope/pull/529
Branch: `ws/d-20261123-gnoscopemeter`
Commit: `5133c73`
Builds on: #526 (Retry-After, breaker, singleflight) and #528 (health history), both on `main`
and neither touched here.

## NO NEW FLAG IS NEEDED

**Nothing here has to be added to the unit file.** The meter is unconditional and
in memory: it starts counting on the first outbound request and `GET
/api/indexer/usage` answers from it with no flag, no database and no interval to
configure. A deploy of the binary alone is a deploy of a working meter.

Said this loudly because the last change shipped a feature that recorded nothing
until `-health-db` was added, and that nearly went unnoticed. This one has the
opposite failure mode and it is covered: a meter with no traffic reports
`"no outbound indexer requests recorded yet on this instance"` rather than a
comfortable 0% of the budget, so a meter that is somehow not being fed reads as
broken instead of as good news. `TestMeterSaysWhenItHasNothing` and
`TestIndexerUsageEndpointWithNoTraffic` hold that line.

## What was built

1. **`pkg/reqmeter`**, the meter. Per endpoint URL and per GraphQL operation:
   requests sent on the wire, requests collapsed by singleflight, 429s, other
   errors, bytes received. Five files, four test files.
2. **The rolling window**, two resolutions in fixed memory: a 300-slot
   one-second ring (one budget window, exactly) and a 1,440-slot one-minute ring
   (one day). Rates over 60s, 300s and 1h; the peak minute and the peak
   **five-minute run** in the retained day.
3. **The attribution**, grouped by the GraphQL operation name or by the first
   root field where there is none.
4. **The cache that was not built**: every wire request is run past a
   simulation of a response cache at 5s, 30s and 300s, and `would_hit` per group
   is the hit rate that cache would have had. **This is the deliverable that
   answers #525.** No cache was built.
5. **`GET /api/indexer/usage`**, plus an **indexer budget** section on `/sanity`
   below the availability strip, in the page's existing visual language (stat
   cards, a verdict badge, the server's own sentence, one table per endpoint).

## Why the peak over five minutes is the figure, not the average

The ceiling is 3,100 requests per 300 seconds, which is not a rate. A day
averaging well under it with one window four times over it is exactly the shape
that earns a 429, and the average reports that day as fine. So the verdict is
graded on `peak_300s`, and `peak_minute` sits beside it because a single minute
at 4x is the thing an operator can actually recognise in a log.

`TestWindowPeakRunBeatsPeakMinute` is the case that makes this worth the code:
one minute with 30 requests loses to five consecutive minutes with 20 each, and
only the second of those costs anything.

The run is minute-aligned, so the true worst 300 seconds can straddle two runs
and be **up to one minute's traffic higher** than what is reported. Said in the
response's own prose and in `docs/api.md`.

## Shared bucket versus own origin, and why that split exists

`shared` is the traffic that spends the per-IP budget; `all` is every endpoint.
The verdict comes from `shared` alone.

This is not a nicety. Mainnet's reads can be served by onbloc, which is a
different origin and off that Cloudflare edge entirely, measured on #525, and
the reason mainnet never 429s while onyx always did. An instance configured with
onbloc first spends almost nothing against the shared bucket while making plenty
of requests, and a meter that summed every endpoint into one verdict would report
a budget problem that does not exist. Classification is by DNS suffix under
`gno.land`; an unparseable URL counts as shared, because over-reporting what we
spend against a ceiling is the safe direction to be wrong in.

## What a cache would have saved: a cache, not a memory

The distinction decides the number, so it is worth stating. Requests at t=0, t=4
and t=8 under a 5 second lifetime are **one hit and a refill**, not two hits: the
entry expires five seconds after it was filled, not five seconds after it was
last read. A naive "have I seen this recently" counter would report a rate no
cache could deliver. `TestCacheSimWouldHaveHit` pins that case by hand.

Three things keep the figure honest, all of which make it read **low**:

- evictions from the bounded key set count as misses a larger cache would have
  hit (`TestCacheSimEvictsAndUndercounts` asserts the error only goes that way);
- requests singleflight already collapsed are excluded, so a saving #526 already
  made is never counted twice;
- a refusal, a transport failure, a miss and a GraphQL error are never cache
  fills, so a repeated failure cannot inflate a hit rate.

The identity is url plus the exact request bytes, which is the same key #526's
singleflight uses, so the two figures are about the same thing.

### What the meter already measured

From the e2e harness, which is the only traffic available here (a fake indexer
on localhost, so nothing spends the real budget): serving `/sanity` once made 7
requests, of which `latestBlockHeight` was 4 and a 300s cache would have
answered 3 of those 4, and `getBlocks` was 3 with 2 of those answered. That is a
75% and 67% would-have-hit rate on a single page load, before any crawler or
second reader. Not a production figure, but the mechanism reporting a real
number rather than a zero.

## What bounds the meter's memory

Every structure is a hard cap, and none is keyed by anything an outsider
controls. Reported in the response's own `bounds` object rather than asserted in
a comment, and `TestMeterStaysBounded` drives each cap past its limit.

| structure | bound | cost |
|---|---|---|
| rolling window per endpoint | 300 one-second slots + 1,440 one-minute slots, 24 bytes each | ~41 KB |
| endpoints | 16, the 16th being a folded `(other endpoints)` bucket | ~670 KB of windows |
| aggregate windows | 2 (`all` and `shared`), kept rather than derived | ~82 KB |
| operation groups | 48 per endpoint, the 48th a folded `(other)` bucket, counters only | ~10 KB per endpoint |
| cache simulation | LRU of 8,192 request digests, a 16-byte key plus three fill instants | ~1.5 MB |

Worst case is roughly **2.5 MB**, flat, and only an instance with sixteen
distinct endpoints reaches it. The default two-network config is about 170 KB of
rings plus whatever the simulation's LRU fills to, which is the 1.5 MB above and
is the single largest piece. Nothing is persisted and nothing is swept by a
goroutine: a window slot carries the index of the interval it holds, so a stale
slot reads as zero on its own with no cleanup pass to forget to run.

Two things an outsider could have influenced, and what stops them:

- **Endpoint URLs** come from the operator's network config, not from a request.
  `maxEndpoints` is the backstop in case that ever stops being true.
- **Group labels** are read from query text this repo writes. Several queries
  interpolate a package path or an address straight into a `where` clause and
  those values *are* attacker-controlled, so the label scanner skips string
  literals (honouring backslash escapes) and a label is charset-clamped to 48
  characters. `TestLabelIgnoresTextInsideValues` feeds it paths crafted to break
  out of the quotes. `maxGroups` is the backstop for that too.

## Where the counting happens, and the one it caught

`Client.fetch` in `pkg/indexer`, which after #526 is the one place an outbound
GraphQL request leaves the binary. `fetch` is now a thin recording wrapper over
`fetchOnce`, which is the former body unchanged apart from returning the response
size. A counter at a call site is a counter somebody forgets to add to the
forty-first call site, and `TestMeterSeesTheEndpointProbe` proves even the pool's
own endpoint probe is counted.

Two figures rather than one. `asked` is counted in `doQuery`, before singleflight
decides whether a caller costs a round trip; `sent` is counted in `fetch`, which
runs once per flight. `collapsed` is the difference, computed rather than counted
separately so the two cannot drift apart and claim a saving that did not happen.

**The trap this hit**: `/api/indexer/usage` reports the server's own live
counters, and `cacheable()` in `pkg/httpapi/cache.go` would have served it from
the response cache. It is the fifth endpoint in that class (`/api/views`,
`/api/traffic` and friends each caught this before), so it is in the bypass list
and in `TestLiveCountersAreNotCacheable`, whose comment says why: read during an
incident, thirty seconds old is the difference between "we are the cause" and
"we were the cause and have stopped".

## Tests

- **The rolling window at a boundary**: `TestWindowBoundary`, table-driven over
  eight cases, including a slot written exactly one full ring lap ago (which sits
  at the position the current interval wants and must read as zero), one second
  either side of that lap, and a slot from the future that a clock stepped
  backwards leaves behind.
- **The would-have-hit counter against a known series**:
  `TestCacheSimWouldHaveHit`, nine hand-computed cases, plus eviction,
  endpoint-keying and a 50,000-request bound check.
- **The grouping against real query text from this repo**: `TestLabel`, eleven
  cases copied out of `pkg/indexer/indexer.go` rather than invented: the probe's
  two-root-field query, `getSupply`'s variable form (the only one), the sync
  walk's height filter, the events-by-pkg-path query that interpolates a value.
- Plus: the budget verdict thresholds, peak-of-a-sum against sum-of-peaks, the
  bounds, concurrency under `-race`, a nil meter, JSON round-trip, the wiring
  from `pkg/indexer` through real HTTP, the endpoint handler, and three
  browser tests on `/sanity` (two stubbed states, one unstubbed against the real
  meter).

## Every outbound HTTP path in the binary this meter does NOT cover

Named in full, because the same blind spot would make the meter understate.
Grepped for `http.Client`, `http.DefaultClient`, `http.Get` and `http.Post`
across every non-test file.

**The one that matters:**

1. **RPC and ABCI**, via `sharedClient` in `pkg/httpapi/httpclient.go`. Five
   distinct clients, none metered and none behind #526's breaker either:
   - `rpcStatusClient` and `balanceClient` (`pkg/httpapi/api.go`): `probeNode`,
     chain verification, account balances.
   - `abciClient` (`pkg/httpapi/govdao.go`): `vm/qrender`, `vm/qeval`, dozens
     per govdao overview.
   - `paramsClient` (`pkg/httpapi/params.go`): chain parameters.
   - `gnockpitClient` (`pkg/httpapi/gnockpit.go`).

   This is already the known gap: #525's second comment measured 2 RPC 429s in 77
   minutes after #526 took the indexer to zero, and `[onyx] rpc chain unverified
   (rpc returned 429)` roughly every forty minutes. The rule this meter grades
   against counts `indexer.` hostnames, so the RPC traffic is outside it and
   covering it is not a matter of pointing the same meter at the same ceiling:
   it needs its own. **Speculation, flagged as such:** #525 records the indexer
   rule's numbers and not the RPC rule's, so what that ceiling actually is has
   not been established here. Not done, deliberately, and it is the next change.

**The rest, for completeness:**

2. `shotClient` (`pkg/httpapi/shot.go`): the gnoshot screenshot upstream.
3. `pkg/price/anchor.go`: Kraken and KuCoin, two goroutines per anchor fetch.
4. `pkg/ghlab/client.go`: the GitHub API for the ecosystem lab.
5. `main.go:387`, `genesis.EnsureImported` with a bare `&http.Client{}`, one
   fetch at startup.
6. `cmd/awesome-sync` (`main.go` and `sites.go`) and `cmd/mcp-eval`: separate
   binaries, not the server, run by hand.
7. The browser's own loads of d3, Chart.js and ECharts from two external
   origins. Client-side, so no server-side meter can see them at all.

Also uncovered inside the indexer path itself, and worth naming: the per-network
`healthTracker` gap recorded on #526 is untouched. Roughly twenty single-network
handlers call `clientFor` directly and `ws.go`'s SSE poll loop runs its own
ticker per network. **Those requests ARE metered**, since they go through
`Client.fetch` like everything else; they just do not report to the health
tracker. The meter is strictly wider than the breaker on the indexer path.

## What I could not do

- **Produce a production number.** The meter is the instrument; it has to run on
  the real instance to answer #525, and this branch does not deploy. The figures
  above are from the e2e harness against a fake indexer on localhost.
- **Measure the RPC path**, for the reason in item 1 above: it is a separate
  ceiling and would be a separate meter with a separate budget constant.
- **Survive a restart.** Everything is in memory, so a deploy resets the day's
  peak. `since` and `uptime_seconds` are in every response and in the page's
  footer so that a short uptime cannot read as a full day.

## Gate

Run in the foreground, watched to completion.

```
$ gofmt -l .
$ go vet ./...
$ go test ./...
?   	github.com/gnoverse/gnoscope	[no test files]
?   	github.com/gnoverse/gnoscope/cmd/awesome-sync	[no test files]
?   	github.com/gnoverse/gnoscope/cmd/mcp-eval	[no test files]
ok  	github.com/gnoverse/gnoscope/pkg/achievements	(cached)
ok  	github.com/gnoverse/gnoscope/pkg/analyzer	4.303s
ok  	github.com/gnoverse/gnoscope/pkg/badge	(cached)
ok  	github.com/gnoverse/gnoscope/pkg/config	(cached)
ok  	github.com/gnoverse/gnoscope/pkg/discover	(cached)
ok  	github.com/gnoverse/gnoscope/pkg/genesis	0.916s
ok  	github.com/gnoverse/gnoscope/pkg/ghlab	(cached)
ok  	github.com/gnoverse/gnoscope/pkg/glossary	0.282s
ok  	github.com/gnoverse/gnoscope/pkg/gnoaddr	(cached)
ok  	github.com/gnoverse/gnoscope/pkg/gnostate	(cached)
ok  	github.com/gnoverse/gnoscope/pkg/healthlog	(cached)
ok  	github.com/gnoverse/gnoscope/pkg/httpapi	44.293s
ok  	github.com/gnoverse/gnoscope/pkg/indexer	9.004s
ok  	github.com/gnoverse/gnoscope/pkg/price	(cached)
ok  	github.com/gnoverse/gnoscope/pkg/registry	(cached)
ok  	github.com/gnoverse/gnoscope/pkg/reqmeter	(cached)
ok  	github.com/gnoverse/gnoscope/pkg/srcdiff	(cached)
ok  	github.com/gnoverse/gnoscope/pkg/srctok	(cached)
?   	github.com/gnoverse/gnoscope/pkg/stdlibs	[no test files]
ok  	github.com/gnoverse/gnoscope/pkg/store	41.755s
ok  	github.com/gnoverse/gnoscope/pkg/syncer	5.789s
ok  	github.com/gnoverse/gnoscope/pkg/tags	(cached)
ok  	github.com/gnoverse/gnoscope/pkg/traffic	(cached)
ok  	github.com/gnoverse/gnoscope/pkg/web	4.430s
$ golangci-lint run ./...
0 issues.

$ make e2e
Running 673 tests using 1 worker
...
  ✓  333 [chromium] › tests/indexer-budget.spec.js:68:1 › a quiet instance reads as under the budget (1.8s)
  ✓  334 [chromium] › tests/indexer-budget.spec.js:105:1 › a burst over the ceiling says so at a glance (1.8s)
  ✓  335 [chromium] › tests/indexer-budget.spec.js:144:1 › the real meter counts the requests this instance made (1.7s)
...
  56 skipped
  617 passed (23.1m)
```
