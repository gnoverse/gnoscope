# Health history: the sanity page reports a history, not a state

Issue: https://github.com/gnoverse/gnoscope/issues/525 (section 4 only)
PR: https://github.com/gnoverse/gnoscope/pull/528
Branch: `ws/d-20261123-gnoscopehist`
Commit: `ddbe422`
Builds on: #526, which is already on `main` and is not touched here.

## What was built

Four pieces, in the order the issue asked for them.

1. **`pkg/healthlog`**, a sample store in its own SQLite file behind
   `-health-db` and `-health-retention-days`, modelled on `pkg/traffic`: buffer,
   `Flush`, a `Run` loop that flushes and prunes, nil-safe everywhere.
2. **`Transitions`**, a pure function collapsing a sample series into runs, plus
   `Summarize` and `Buckets` beside it. Nothing derived is stored.
3. **The strip**, a 7-day availability row per network on `/sanity`, under the
   chain diagnosis and above the heartbeat, with the sentence underneath and the
   incident list under that.
4. **`GET /api/health/history`**, which is where the strip's data comes from and
   what the widget and MCP can read later.

Recording happens inside `probeNetworks`, which is the one function every health
probe in the binary goes through. That is the whole reason there is no probe
path left unrecorded, and the reason `cockpitNetworks` and the all-networks
`/api/sanity/overview` branch were collapsed into one new `chainStates` helper:
they were two copies of the same liveness fan-out, and the sampler would have
been a third.

## Cadence: one sample per network per minute

Three reasons, which happen to agree.

- **It is the unit the thing being measured moves in.** `breakerCooldown` is 60
  seconds and the 429 stand-down is longer, so no outage this explorer can
  actually suffer is shorter than a minute. Sampling faster would resolve
  nothing that is not already resolved.
- **It is the finest resolution the strip can show.** A 7-day strip in 168
  hourly cells collapses 60 samples into one colour. A sample a second would buy
  3,600 rows per cell and the same cell.
- **It is cheap.** Four networks is 5,760 rows a day, a few hundred KB a month,
  against a chain index measured in gigabytes.

Samples come from two places, which is what keeps the record even rather than
a record of when somebody had the page open:

- every reader who opens `/sanity` or `/cockpit` writes one as a side effect of
  the probe, and the cache warmer does too since both endpoints are on its list;
- a background sampler (`RunHealthSampler`) guarantees one a minute, which
  covers 04:00 on a Sunday, the hour an outage actually happens in.

Because of the first source, writes are throttled per network to one every 30
seconds (`healthlog.MinGap`), half the interval so an ordinary tick is never
dropped for arriving early. Without it the row count would be a function of how
many wall displays are pointed at `/cockpit`.

**What this costs the upstream budget**, which matters because the rest of this
issue is about 429 amplification. The sampler runs the same probe the page runs:
four calls to the network's own RPC, which is not the rate-limited endpoint, and
exactly one `GetRecentBlocks` against the indexer. That is 1 query per network
per minute, against a syncer that already ticks every 30 seconds with eighteen
passes behind it. It goes through `fanOut`, so while the breaker is open it
sends nothing at all and the network is recorded as unreachable from what we
already know, which is both correct and free. And it is a floor rather than a
multiplier: `/sanity` and `/cockpit` already made this call on every request,
and the warmer already polls both.

## Retention: 30 days

Matching `-traffic-retention-days`, deliberately. The strip draws seven days and
the summary answers "when was it last unreachable", which wants more history
than the strip draws: the extra three weeks are what answers "has this been
happening all along" when somebody finally looks. 30 days of four networks is
~173k rows, single-digit MB. `0` keeps everything.

Both defaults are flags, so an operator who disagrees changes a number rather
than a build.

## Why a gap is its own state

`no_data` is never sampled; the derivation emits it for a stretch where
consecutive samples are more than three intervals apart, and for the tail when
the newest sample is older than that. This is the part most likely to be got
wrong by a simpler implementation, and getting it wrong is invisible: a deploy,
a crash or a `-health-db` added mid-week would otherwise be drawn as more of
whatever state preceded it, which is to say a restart during an outage would
render as a green week.

It follows that `uptime_percent` is healthy samples over *observed* samples, not
healthy time over wall time, with `no_data_seconds` printed beside it so a
reader can see how much of the window the percentage actually covers. A window
with no samples reports no percentage at all rather than 0% or 100%.

## The rendering, in all four states

Produced by `go test ./pkg/httpapi/ -run 'TestHealthHistoryRenders|TestHealthHistoryWithNoSamples' -v`,
which renders the real endpoint's cells one character per cell:
`#` alive, `!` indexer down, `X` wedged, `.` no sample in the bucket.

```
=== RUN   TestHealthHistoryWithNoSamples
    health_history_test.go:160: 
        onyx |........................................................................................................................................................................|
          no availability history recorded yet: this instance has taken no sample for this network
    health_history_test.go:160: 
        pearl |........................................................................................................................................................................|
          no availability history recorded yet: this instance has taken no sample for this network
--- PASS: TestHealthHistoryWithNoSamples (0.01s)
=== RUN   TestHealthHistoryRenders
=== RUN   TestHealthHistoryRenders/healthy:_a_week_of_alive_samples
    health_history_test.go:282: 
        onyx |......................................................................................................................................##################################|
          no outage in the last 7d; 100.00% of 2001 samples available, recording only since 2026-11-22 02:05 UTC
=== RUN   TestHealthHistoryRenders/flapping:_six_short_outages,_recovered
    health_history_test.go:282: 
        onyx |.............................................................................................................................................................#!#!#!!#!#!|
          last unreachable 49m ago: indexer down for 1m; 6 outage(s) and 99.00% of 601 samples available over the last 7d, recording only since 2026-11-23 01:25 UTC
          2026-11-23T10:35  indexer_down     1m
          2026-11-23T08:55  indexer_down     1m
          2026-11-23T07:15  indexer_down     1m
          2026-11-23T05:35  indexer_down     1m
          2026-11-23T03:55  indexer_down     1m
          2026-11-23T02:15  indexer_down     1m
=== RUN   TestHealthHistoryRenders/a_long_outage,_still_open
    health_history_test.go:282: 
        onyx |...............................................................................................########################XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX|
          wedged right now, for 2d so far; 1 outage(s) and 33.33% of 4321 samples available over the last 7d, recording only since 2026-11-20 11:25 UTC
          2026-11-21T11:25  wedged           2d and counting
--- PASS: TestHealthHistoryRenders (0.14s)
    --- PASS: TestHealthHistoryRenders/healthy:_a_week_of_alive_samples (0.04s)
    --- PASS: TestHealthHistoryRenders/flapping:_six_short_outages,_recovered (0.01s)
    --- PASS: TestHealthHistoryRenders/a_long_outage,_still_open (0.08s)
PASS
```

Note what the healthy and flapping rows admit: `recording only since ...`. The
series is shorter than the window, and the sentence says so rather than letting
`100.00%` imply a week it does not have. Note also that the flapping row's
one-minute outages fill a whole hourly cell, because a cell takes the worst
state in its bucket; the incident list beside it carries the true durations,
which is why both exist.

In the browser the same four states are `e2e/tests/availability.spec.js`, which
asserts the cells are drawn, the sentence is the one the server wrote, the
incidents are listed with their durations, and the no-history case shows a blank
strip with no percentage anywhere. The last test in that file is unstubbed: the
e2e harness now runs with `-health-db`, so opening `/sanity` records a real
sample through the real probe and the strip draws it.

## Probe paths that write no sample, and that I did not fix

Every path that produces a `ChainDiagnosis` writes one: `diagnose` is called
only from `probeNetworks`, and `probeNetworks` records. The gaps are elsewhere,
in code that knows something about reachability and does not produce a
diagnosis. Named, not fixed:

1. **`healthTracker.record` in `pkg/httpapi/api.go`.** Breaker trips and 429
   stand-downs, per query. This is the biggest one, and it is the measurement in
   the issue itself: 38 breaker trips in a day on `onyx`. A trip that opens and
   closes between two samples leaves no row, so the strip can show a clean hour
   that contained three trips. The history records the *state the trips
   produced*, which is what a reader of the page asked for, not the trips.
   Sampling them would mean an event table beside the sample table, with its own
   rate limiting, which is a second feature.
2. **`liveFeed.pollStep` in `pkg/httpapi/ws.go`.** Polls the indexer every few
   seconds per network for the SSE feed, learns immediately when it stops
   answering, writes nothing.
3. **`RunBalanceSweeper` / `SweepBalances` in `pkg/httpapi/balances.go`.** Hits
   each network's RPC on its own timer and knows when it fails.
4. **`VerifyRPCChains`.** The periodic cross-check that an RPC serves the same
   chain as its indexer. It is the one thing that notices an endpoint repointed
   under a running process, and it records nothing.
5. **`syncer.Registry.Record`.** Per-pass sync outcomes. Shown on `/sanity`'s
   sync health table, not in this history, and they answer a different question:
   whether our passes land, not whether the chain is reachable.
6. **`/api/health/heartbeat`.** Not a probe at all, which is worth saying because
   it sits next to the new strip: it is block cadence read from the local
   `blocks` table.

All six write nothing by design rather than by oversight: each would need a
rate limit of its own, and three of them run per request.

## What I could not do

- **No production evidence.** The 38 breaker trips are the issue's measurement,
  not mine. Nothing here was run against the gnoscope.com instance, no config
  was touched, nothing was deployed or restarted. Until `-health-db` is added to
  the unit file this feature records nothing in production and the section
  renders its off state.
- **`/cockpit` and `/widget` do not show the history.** The endpoint exists so
  they can; wiring it into the roll-up and into the grading (an "it has been
  flapping all week" finding) is a separate change. `cockpitChainSeverity` and
  `cockpitChainTitle` still grade the current answer only.
- **No MCP tool.** Same reason: the endpoint is the surface, exposing it is its
  own change.
- **The sampler's own failures are not reported anywhere.** `healthlog.Stats()`
  counts written, throttled and dropped samples, and nothing serves it. An
  operator cannot currently tell a quiet week from a sampler that has been
  failing to write.
- **Retention is enforced hourly from process start.** A process restarted more
  often than that prunes only at boot, which is the same behaviour `pkg/traffic`
  has had all along.

## Gate

```
$ gofmt -l .

$ go vet ./...

$ go test ./...
?   	github.com/gnoverse/gnoscope	[no test files]
?   	github.com/gnoverse/gnoscope/cmd/awesome-sync	[no test files]
?   	github.com/gnoverse/gnoscope/cmd/mcp-eval	[no test files]
ok  	github.com/gnoverse/gnoscope/pkg/achievements	0.008s
ok  	github.com/gnoverse/gnoscope/pkg/analyzer	1.474s
ok  	github.com/gnoverse/gnoscope/pkg/badge	0.007s
ok  	github.com/gnoverse/gnoscope/pkg/config	0.009s
ok  	github.com/gnoverse/gnoscope/pkg/discover	0.019s
ok  	github.com/gnoverse/gnoscope/pkg/genesis	0.237s
ok  	github.com/gnoverse/gnoscope/pkg/ghlab	0.348s
ok  	github.com/gnoverse/gnoscope/pkg/glossary	0.039s
ok  	github.com/gnoverse/gnoscope/pkg/gnoaddr	0.005s
ok  	github.com/gnoverse/gnoscope/pkg/gnostate	5.711s
ok  	github.com/gnoverse/gnoscope/pkg/healthlog	0.107s
ok  	github.com/gnoverse/gnoscope/pkg/httpapi	53.838s
ok  	github.com/gnoverse/gnoscope/pkg/indexer	9.572s
ok  	github.com/gnoverse/gnoscope/pkg/price	0.237s
ok  	github.com/gnoverse/gnoscope/pkg/registry	0.020s
ok  	github.com/gnoverse/gnoscope/pkg/srcdiff	0.088s
ok  	github.com/gnoverse/gnoscope/pkg/srctok	0.676s
?   	github.com/gnoverse/gnoscope/pkg/stdlibs	[no test files]
ok  	github.com/gnoverse/gnoscope/pkg/store	51.841s
ok  	github.com/gnoverse/gnoscope/pkg/syncer	7.442s
ok  	github.com/gnoverse/gnoscope/pkg/tags	0.017s
ok  	github.com/gnoverse/gnoscope/pkg/traffic	4.069s
ok  	github.com/gnoverse/gnoscope/pkg/web	4.519s
```

Also green:

```
$ golangci-lint run ./...
0 issues.
```

And the browser suite, which this change touches `pkg/web/frontend/index.html`
and `e2e/harness/global-setup.mjs` for. Node 24, offset port, full run:

```
$ E2E_PORT_OFFSET=311 npx playwright test --reporter=line
Running 670 tests using 1 worker
...
  56 skipped
  614 passed (25.7m)
```

One note on that run, because an earlier attempt is in this session's history:
the first full run was killed by a harness time limit before it reported, and
it had left one failure artefact behind, `contracts.spec.js > a repaint keeps
the positions rather than reseeding them` (max bubble movement 776px against a
120px bar). That spec passed in the complete run above. It is a force-directed
layout settling test, it touches nothing this change goes near, and the machine
was running twenty-odd other Playwright suites from other checkouts at the
time. Recorded here rather than dropped.

