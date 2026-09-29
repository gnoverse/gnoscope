# QA pass

A page-by-page check of a **running** gnoscope, plus the numbers, kept out of
CI on purpose. Run it when you want to know how the deployment is doing, not
on every push.

```bash
make qa                 # probe production, record the run
make qa-report          # the table
```

## What it is for, and why it is not the e2e suite

`e2e/` runs on every push against a seeded fixture and answers *did this change
break the code*. This answers two questions that one cannot:

- **Is every page still drawing?** Against real data, on the real host, where
  an endpoint can be slow or a realm can have a shape the fixture never has.
- **Has anything got slower?** e2e asserts and throws the numbers away, so a
  page that doubled in cost while staying correct passes it in silence.

### The rule every check follows

**Assert that something specific was drawn. Never that no error was raised.**

On 2026-09-29 `/dashboards` spent forty minutes on production rendering the
words "charts unavailable" and nothing else. No exception, no failed request,
no empty `<main>`, and 424 green e2e tests over the top of it, because a page
that politely reports its own failure produces none of the signals those tests
look for.

So each page carries a `proof`: a selector that exists only if the page did its
job. `#view-dashboards canvas`, not "the page loaded".

The same trap caught this tool during its own first run. The default proof was
`.view.active main *`, which matches `.skeleton` placeholders, so every page
"finished" in about 45ms having fetched nothing. It is
`.view.active main *:not(.skeleton)` now, and readiness needs the skeletons
gone as well.

## The two clocks

| | |
|---|---|
| `readyMs` | the reader can see what they came for (the `proof` is visible) |
| `settledMs` | nothing anywhere on the page is still a pulsing grey box |

They diverge when a page paints quickly and leaves a background tab loading,
which is a real and common shape here, and reporting one number for both
mislabels it either as a broken page or as a fast one. A page whose skeletons
never clear is marked `slow` with a note, never `fail`: it did draw.

## Adding and removing checks

Edit [`checks.json`](./checks.json). Nothing else.

```json
{ "id": "realm-deps", "path": "/realm/r/gov/dao?tab=deps",
  "proof": "#dep-graph svg", "budgetMs": 12000 }
```

- `proof` defaults to "the active view drew a non-skeleton element". Give a
  real one wherever the page has something specific to prove.
- `budgetMs` defaults to `budgets.readyMs` for pages, `budgets.apiMs` for
  endpoints. Over budget is `slow`, not `fail`.
- `comment` is for the next reader and is ignored by the runner. Use it to say
  why a check exists, especially when the budget is a number you do not like.

**History survives the churn**, which is the whole reason results are stored
the way they are. Each run records a flat list of observations keyed by check
id, never a fixed set of columns. A check added today is blank in the runs
before it existed. A check deleted today keeps its history and stops gaining
columns. No migration, and nothing silently drops.

Verified by doing it: removing `glossary`, removing `api-glossary` and adding
`api-apps` produced a table where the two removed rows kept their old numbers
with an empty newest column, and the new row had only a newest column.

## Where the results live, and why they are committed

`qa/results/` is **in the repo**, one JSON file per run. That is a decision, not
a default, and it was taken deliberately after the alternative was costed:
moul, 2026-09-29.

What committing buys is the only thing that makes a trend table worth having:
the history survives a fresh clone, and every machine and every session reads
the same numbers. A local-only `qa/results/` is gone the first time somebody
checks the repo out somewhere else, and invisible to everyone but its author,
which leaves the table answering "has this got slower since I last ran it"
instead of "has this got slower".

What it costs is real and you will notice it: a run produces a file that wants
committing, in a checkout several agent sessions share, so it turns up in
somebody else's `git status` mid-task. That was judged the smaller problem.

One file per run rather than one appended file, because two sessions probing at
once would conflict on a single file and never on separate ones.

**Do not gitignore this directory as tidying.** If the growth becomes a real
problem the answer costed at the time was pruning to the last N runs, which
bounds the repo without giving up the shared history. Dropping it to local-only
gives up the property it exists for.

## Reading the table

```
  check                9afbab0/15:21  9afbab0/15:22   trend
 !address-detail                FAIL             47
  realm-deps                      865            255   -71%
```

- `!` a run in this window failed the check. `~` over budget in the newest run.
- A repeated build hash gets the time appended, because running the same build
  twice is how you tell a flaky check from a real regression.
- `trend` compares the last two runs that both have a number, and prints only
  when the change clears **both** 20% and 40ms. A percentage alone makes 45ms
  to 59ms look like a 31% regression; an absolute alone stays silent on an
  endpoint doubling from 200ms.

Flakiness is the thing a single run cannot show you and this can. The first
recorded pass had `/storage` throwing a JSON parse error, `/api/blocks`
answering 500 and `/api/block/1000` answering 404. All three passed on the
next run against the same build.

## Flags

```bash
node qa/run.mjs --target http://localhost:8888   # a local build
node qa/run.mjs --only dashboards,api-realm      # a subset, by id
node qa/run.mjs --no-store                       # print, record nothing

node qa/report.mjs --runs 20                     # more history
node qa/report.mjs --metric settledMs            # or domInteractiveMs, apiRequests…
node qa/report.mjs --only page                   # or api
node qa/report.mjs --markdown                    # for pasting into an issue
```

`run.mjs` exits non-zero on a failure and zero on merely slow, so it can gate
something later without a budget nobody has tuned yet blocking anybody.

## Requirements

The Playwright browser from `e2e/` (`cd e2e && npm ci`). Node 22 or newer;
unlike the e2e fixture this needs no `node:sqlite`. Nothing here ships in the
binary.

`ignoreRequests` in `checks.json` lists request failures the environment owns
rather than the site, each with its reason, the same rule as
`EXPECTED_FAILURES` in `e2e/tests/helpers.js`. Anything not listed fails its
check.
