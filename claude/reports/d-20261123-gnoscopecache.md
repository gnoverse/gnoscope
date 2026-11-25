# The indexer response cache: keeping the 92% that cannot change

Issue: https://github.com/gnoverse/gnoscope/issues/525
PR: https://github.com/gnoverse/gnoscope/pull/530
Follow-up issue opened: https://github.com/gnoverse/gnoscope/issues/531
Branch: `ws/d-20261123-gnoscopecache`
Commits: `ee06a29`, `f5da90f`
Builds on: #526 (Retry-After, first-refusal breaker, singleflight), #528 (health
history) and #529 (the outbound request meter), all on `main` and none of them
changed here. #529 is both the instrument this was measured with and the test
harness it was built against.

## NO NEW FLAG IS NEEDED

**Nothing here has to be added to the unit file.** The response cache is
unconditional, in memory, and bounded at 20,000 entries or 32 MB per endpoint
pool. There is nothing to enable, nothing to size, and no database.

It is not wired in `main.go` either, deliberately: `newIndexerClient` looks its
cache up from a registry keyed by the endpoint list, so a network's serve client
and its sync client share one and there is no constructor anybody can forget.
#528 shipped a feature that recorded nothing until `-health-db` reached the unit
file. The opposite failure is covered here and is loud: with no cache
registered, `/api/indexer/usage` answers `"no response cache is registered on
this instance, so every figure above is uncached traffic. This is a wiring
fault, not a hit rate of zero."` and the `/sanity` section prints it in red.
`TestUsageSaysWhenNoCacheIsRegistered`, `TestIndexerUsageSaysWhenNoCacheIsWired`
and an unstubbed e2e assertion hold that line.

## What I found about why the same ranges are re-asked

The issue offered two possibilities and asked me to say which. It is neither,
and the real answer is sharper than both.

**The passes do not share a walk, but they are also not the duplication.** They
are cursor-driven and mostly fetch nothing: over the production hour #529
measured, `getTransactions` was 633 requests on mainnet against `getBlocks`'s
21,684. A walk that re-fetched pages would show up in the first number.

**The duplication is block-time resolution, one outbound request per block
height, by passes that re-ask the same finalised heights every thirty seconds.**
`syncer.fetchBlockTimes` calls `GetBlocksByHeights`, which issues one
`GetBlock(height)` per distinct height, ten at a time. Eight sync passes call it.

Evidence, in the order I found it:

1. **The meter's own shape.** 0.16% collapsed by singleflight says the
   duplicates are sequential. `would_hit` of 87% at 300s against 32% at 30s says
   the repeat period is the 30-second sync tick. And `cache_sim_tracked` was
   **1,418 distinct request digests behind 35,235 requests**, with zero
   evictions: the instance asked about fourteen hundred distinct questions in an
   hour and asked them thirty-five thousand times.

2. **A live sample, 2026-11-23, twelve readings fifteen seconds apart off
   production's `/api/indexer/usage`.** Mainnet over 165 seconds: **990
   `getBlocks` against 36 `getTransactions`**, in bursts of roughly 330 per
   30-second tick with quiet gaps between them. Twenty-seven block queries per
   transaction query is not a transaction walk.

3. **`syncUsers` has no cursor at all, by design, and re-resolves every
   registration's block time on every pass.** `pkg/syncer/users.go` replays the
   whole `gno.land/r/sys/users` registry each pass, and its comment explains
   correctly why a cursor would be wrong: a third of the registry was written at
   genesis and a cursor-driven walk above the last stored height never revisits
   it. What follows is `times := s.fetchBlockTimes(ctx, ordered)`, which is one
   outbound request per registration. My own run against live mainnet logged
   `[gnoland-1] user registry: 152 events replayed` on every pass. 152 requests
   every 30 seconds, for heights that are finalised and immutable.

4. **`backfillBlockTimes` is head-of-line blocked.**
   `HeightsMissingBlockTime` is `ORDER BY block_height DESC LIMIT 200` with no
   marker for "asked, and there is no answer"; `SetBlockTimes` skips an empty
   time. So any height the indexer returns nothing for is never filled, comes
   back at the head of the same 200 on the next pass, and blocks the repair
   behind it indefinitely. 200 heights, two networks, 120 passes an hour is the
   right order of magnitude for the rest of what the meter shows.

5. **The answer is already in local SQLite.** `syncBlocks` fills a `blocks`
   table with `(network, height, time)`. `store.BlockTimesForHeights` reads
   block times locally and the API's `blockTimesForHeights` uses it properly,
   asking the indexer only for what storage could not answer.
   `syncer.fetchBlockTimes` does neither, and nothing reads `blocks` for this at
   all.

So a cache is the right instrument for this PR, and **removing the questions is
the better fix**. Both are worth having, so the cache is here and the other is
an issue with the evidence: https://github.com/gnoverse/gnoscope/issues/531.

One honest limit on the cache, which is why that issue matters: the heights in
(4) are stuck precisely because the indexer answers them with *nothing*, and an
empty `getBlocks` is never cached (see below). The cache cannot absorb them.
They need the marker the follow-up proposes.

## The TTL classes, and the reasoning per class

### Class 1: indefinite, bounded only by memory

A finalised block is immutable, and Tendermint2 finalises in one slot, so there
is no reorg to invalidate against. The right lifetime is not 300 seconds; it is
"until the chain under it changes identity".

| query | kept when | why that condition |
|---|---|---|
| `GetBlock(height)`, `height > 1` | the answer contains a block | an empty `getBlocks` is also what a height above the tip, a replica still catching up and a pruned floor look like, and the first of those becomes a real block seconds later. `syncBlocks`' floor probe is written to re-test exactly that verdict, so caching it would freeze a backfill on a wrong "done" |
| `GetBlocksInRange(from, to)` | **every** height in the range came back | this is what makes an indefinite lifetime safe without the caller knowing where the tip is. Blocks are contiguous, so a whole range lies below the tip and is final; a short one either runs past the tip and will grow, or sits under a pruned floor another endpoint may yet answer for |

Note that the second condition is a rule about the *answer*, not about the
query. "This query is cacheable" is not a safe unit of decision here.

### Class 2: never cached

Caching is **opt-in per call site** and the zero-value rule caches nothing, so
this class is everything else by default rather than by enumeration. Named
explicitly, as asked:

- **`latestBlockHeight`.** A stale tip makes the explorer silently lag, which is
  a worse failure than a 429 because nothing reports it. It was 2.6% of
  mainnet's requests when measured, so there is nothing worth buying with it.
  `TestTipIsNeverServedFromCache`.
- **`GetBlock(1)`.** ⚠️ Block 1 is the chain's fingerprint.
  `Syncer.chainFingerprint` and `API.VerifyRPCChains` both read it to decide
  whether the indexer is still serving the chain the stored rows came from.
  Cached indefinitely, a chain reset would stop being late and become
  *undetectable*: the reset check would be answered forever by the chain it
  exists to notice the end of. This is the trap #525's comments record one layer
  down, where a stale node defines a pool's identity and locks the real
  endpoints out as impostors. `TestBlockOneIsNeverCached` drives a reset and
  fails if the hash does not change.
- **`GetRecentBlocks(limit)`.** It reads the tip and then asks for a window
  below it. A tip query wearing a range's clothes.
- **Every `getTransactions` form**, including `GetTransactionsByBlock` and
  `GetTransactionsInRange`, which are immutable by exactly the same argument as
  a finalised block. They were 2.8% and 5.3% of their endpoints' requests but
  96% of the bytes, and the budget counts requests, not bytes. Caching them
  would spend the memory bound on the traffic that is not the problem. This is a
  judgement call, not a safety one, and it is the obvious next candidate.
- **`GetSupply`, balances, `vm/qrender`, `vm/qeval`, parameters** and everything
  else on the RPC/ABCI path. Mutable state, and not on the indexer client at
  all.
- **Schema introspection (`__type`).** Already memoised in
  `Client.typeSupport`, and a second cache over it would be two places
  answering one question.
- **The endpoint probe.** `probe()` calls `doQuery` directly, so it is never
  cached: a pool that picked its member from remembered tips would be the stalled
  indexer bug #525's comments describe, rebuilt.

### What a cache hit is, and is not

A cached answer is served **even while the circuit breaker is shut**. An answer
that cannot change is as true during a 300-second blackout as outside one, and a
page whose whole content is finalised block data should not go down with the
budget. `TestCacheAnswersWhileTheBreakerIsShut`.

A cache hit is **not an observation of the indexer**: it reaches neither
`recordResult` nor `selectEndpoint`. A cache that healed the breaker would hide
the condition the breaker exists to survive.

## What bounds the memory

| bound | value | why |
|---|---|---|
| entries | 20,000 per pool | production held a working set of 1,418 distinct digests over an hour. 20,000 single-block answers at the measured 310 bytes each is about 6 MB |
| bytes | 32 MB per pool | the backstop for range answers, which the entry count alone does not bound |
| one answer | 256 KB | a 5,000-block backfill page is ~684 KB and is walked exactly once, so storing it would be pure cost *and* would flush thousands of single-block answers. A reader's 50-block page of `/blocks` is ~7 KB and is walked by every crawler that pages back through history |
| pools | 16 | one per endpoint list; the real bound is the operator's config and this is the backstop. A production config of three networks is ~96 MB worst case |

Eviction is least-recently-used on both bounds, checked after every store.
Nothing is persisted and no goroutine sweeps anything. Every one of these
numbers is reported under `caches` in `/api/indexer/usage` beside the live
figures, so the memory story is readable off a running instance rather than out
of the source.

The cache is keyed by the request body alone, not by `(endpoint, body)` the way
the singleflight key one layer down is. The members of a pool are interchangeable
and proven to serve one chain (`selectEndpoint` excludes any that disagrees), so
leaving the URL out means a failover does not re-pay for history the pool already
has. Queries pinned to an endpoint for its schema carry different field sets, so
they carry different bodies and key apart on their own. Two *networks* never
share a cache: the lookup is per endpoint list, which is what keeps `GetBlock(500)`
on two chains from being one entry.

## Reset and reorg

**Reorg: there is none to handle.** Tendermint2 has single-slot finality, so a
height never changes hands. That is the whole reason an indefinite lifetime is
defensible.

**Reset: handled, and it is the dangerous case.** A reset chain keeps its chain
ID and its heights and comes back with different blocks at them, which is
exactly what portal-loop and staging networks do. Every cached answer is then
about a chain that no longer exists, and none of them looks wrong.
`Syncer.checkChainReset` purges the cache **before** it discards the rows, and
because the cache is shared by the network's serve and sync clients, that one
call covers the pages as well as the sync loop. `purges` and `last_purge` in
`/api/indexer/usage` are how an operator sees it happened.

Two tests, in both directions: `TestChainResetEmptiesTheResponseCache` fails if
a reset does not reach the cache, and `TestUnchangedChainKeepsTheResponseCache`
fails if it purges on an ordinary pass, because purging every 30 seconds would
be a 30-second TTL wearing an indefinite one's clothes.

And the detection itself stays live, because `GetBlock(1)` is never cached. That
is the half that makes the purge mean anything.

**The gap the cache inherits:** the purge rides the syncer, so an instance
started with `-sync=false` has nothing that would empty it. That is not new
breakage. Such an instance has no reset detection at all today, and its stored
rows already outlive a reset by the same amount; the cache now joins them.
Naming it rather than leaving it to be discovered.

## The measured before and after

### 1. Production, before. The problem, from its own meter

`GET https://gnoscope.com/api/indexer/usage`, read 2026-11-23T19:36Z, 65 minutes
of uptime. Read-only: nothing on that instance was touched.

```
shared bucket   asked 35,235   sent 35,179   collapsed 56 (0.16%)   refused 0
peak over 300s  2,962 of 3,100 (95.5%)        verdict: near
distinct request digests tracked: 1,418, with zero evictions
```

Per operation, `asked` and the simulated hit rate at 5s / 30s / 300s:

| endpoint | operation | asked | share | would_hit |
|---|---|---:|---:|---|
| indexer.gno.land | `getBlocks` | 21,684 | 94.6% | 0.1% / 32.6% / **87.1%** |
| | `getTransactions` | 633 | 2.8% | 0% / 31.5% / 67.4% |
| | `latestBlockHeight` | 632 | 2.6% | 46.4% / 75.2% / 95.8% |
| indexer.onyx… | `getBlocks` | 10,931 | 89.7% | 1.6% / 24.9% / **85.0%** |
| | `getTransactions` | 640 | 5.3% | 1.3% / 31.4% / 72.2% |
| | `latestBlockHeight` | 629 | 5.0% | 46.9% / 74.2% / 95.7% |

Fourteen hundred distinct questions, asked thirty-five thousand times.

### 2. The sync pass, deterministic and re-runnable

`TestRepeatedPassesStopRefetchingFinalisedBlocks` in `pkg/syncer`. Five passes
of `syncUsers` over twelve finalised heights, which is the production shape at
small scale:

```
getBlocks over 5 passes of 12 finalised heights: 60 before, 12 after (80% removed)
```

The ratio is `1 - 1/passes`, so it is not a property of the number five. At
production's 120 passes an hour it is 99.2%, and the test fails if the uncached
arm is anything but `heights × passes` or the cached arm anything but `heights`.

### 3. Live, paired, same binaries, same database, same endpoint

Two binaries, `main` at `687a3ba` and this branch, each run for 25 minutes
against the real `indexer.gno.land` from byte-identical copies of the same
database, on this machine. A different IP from gnoscope.com, so production's
budget paid for none of it.

| | before (`687a3ba`) | after (this branch) |
|---|---:|---:|
| wall clock | 1,505s | 1,506s |
| sync passes completed | 4 | 4 |
| rate-limit stand-downs | 3 | 4 |
| demand (`asked`) | 10,117 | **11,781** |
| sent on the wire | 10,077 | 10,945 |
| answered from cache (`served`) | 0 | **795** |
| `getBlocks` asked | 9,825 | 11,485 |
| `getBlocks` served | 0 | **795 (6.92%)** |
| what a 300s cache would have saved | 0.4%  | n/a |
| cache entries at the end  | n/a | 10,643 / 20,000 |
| cache bytes at the end  | n/a | 2.9 MB / 32 MB, 0 evictions |

The after binary satisfied **16% more demand** over the same wall clock against
the same rate limiter.

**Read the last two rows of the body together, because they are the point of the
design.** This is a *catching-up* instance, not production's steady state: most
of what it asks has never been asked before, and a cache of any length saves
nothing on new ground. The 300-second simulation says such an instance would get
**0.4%**. The indefinite cache actually got **6.9%** of `getBlocks` demand,
seventeen times that, because the repeats here come roughly 390 seconds apart,
one rate-limit cycle, which is **outside the window any short-TTL cache can
see**. The TTL the issue originally proposed would have measured as working and
delivered almost nothing here.

### 4. What is still missing, and it is the big one

**Production's "after" cannot be measured without deploying, which I was told
not to do.** The figure to expect there is not 6.9%: production is caught up, so
its `getBlocks` traffic is the repeats, and its own simulation already says 87%
of it is a repeat inside five minutes with no expiry limit applied. An
indefinite cache is bounded below by that number, not above it.

The first deploy of this branch answers it in one request, and in the same shape
the prediction was made:

```
curl -s https://gnoscope.com/api/indexer/usage | jq '{
  served: .shared.served_percent,
  caches,
  getBlocks: [.endpoints[].groups[] | select(.label=="getBlocks")
              | {url: .label, served: .served_percent, would_hit: .would_hit_percent}]
}'
```

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
ok  	github.com/gnoverse/gnoscope/pkg/analyzer	1.473s
ok  	github.com/gnoverse/gnoscope/pkg/badge	(cached)
ok  	github.com/gnoverse/gnoscope/pkg/config	(cached)
ok  	github.com/gnoverse/gnoscope/pkg/discover	(cached)
ok  	github.com/gnoverse/gnoscope/pkg/genesis	0.210s
ok  	github.com/gnoverse/gnoscope/pkg/ghlab	(cached)
ok  	github.com/gnoverse/gnoscope/pkg/glossary	0.049s
ok  	github.com/gnoverse/gnoscope/pkg/gnoaddr	(cached)
ok  	github.com/gnoverse/gnoscope/pkg/gnostate	(cached)
ok  	github.com/gnoverse/gnoscope/pkg/healthlog	(cached)
ok  	github.com/gnoverse/gnoscope/pkg/httpapi	54.506s
ok  	github.com/gnoverse/gnoscope/pkg/indexer	9.099s
ok  	github.com/gnoverse/gnoscope/pkg/price	(cached)
ok  	github.com/gnoverse/gnoscope/pkg/registry	(cached)
ok  	github.com/gnoverse/gnoscope/pkg/reqmeter	(cached)
ok  	github.com/gnoverse/gnoscope/pkg/srcdiff	(cached)
ok  	github.com/gnoverse/gnoscope/pkg/srctok	(cached)
?   	github.com/gnoverse/gnoscope/pkg/stdlibs	[no test files]
ok  	github.com/gnoverse/gnoscope/pkg/store	51.672s
ok  	github.com/gnoverse/gnoscope/pkg/syncer	10.189s
ok  	github.com/gnoverse/gnoscope/pkg/tags	(cached)
ok  	github.com/gnoverse/gnoscope/pkg/traffic	(cached)
ok  	github.com/gnoverse/gnoscope/pkg/web	(cached)
$ golangci-lint run ./...
0 issues.
```

```
$ make e2e

  56 skipped
  617 passed (23.4m)
```

## What I could not do

- **I did not deploy, and the production numbers above are "before" only.** The
  instance at gnoscope.com is untouched: no config change, no restart, no
  deploy. The "after" figures come from a binary built from this branch, run on
  this machine against the same live mainnet indexer, from a byte-identical copy
  of the same database as the "before" run. A different IP, so production's
  budget was not spent on the measurement.
- **I could not reach a fully synced database inside the rate limit.** A cold
  sync spends the whole 3,100-request budget in 97 seconds and then stalls for
  300, so the warm database both runs start from is partially synced and both
  runs do some catch-up on new ground as well as the repeating work. The cache's
  hit rate on new ground is zero by construction, which pulls the measured
  figure *down*, not up.
- **I did not attribute the production `getBlocks` traffic to a specific pass
  with a counter.** The meter groups by GraphQL operation, not by caller. The
  attribution in "what I found" is the code plus the meter's shape plus a live
  sampling run; items 3 and 4 are provable from the source alone, the arithmetic
  tying them to the observed ~330 per pass is consistent rather than proven.
- **`/rename` could not be invoked.** This session has no `SlashCommand` tool,
  so the chat title was not set.
