# The cockpit and the widget

Two routes over one payload, answering the same question at two distances.

- **`/cockpit`** is read at a desk. Is gnoscope healthy, is the chain moving,
  and if not, what exactly.
- **`/widget`** is recognised from across a room in two seconds, on a
  television on a wall or a phone in a hallway. It is also the page other
  dashboards put in an iframe.

Both are drawn from `GET /api/cockpit`, which does the grading server-side so
the two cannot disagree.

## What it is, and what it is not

It is a **join**. Everything on these pages already existed, in six places:
[`/sanity`](../pkg/httpapi/chainhealth.go) knows the chain diagnosis,
[`/traffic`](../pkg/httpapi/api_traffic.go) knows the request volume,
`/storage` knows the footprint, the syncer knows whether its passes land, the
rail's about block knows the build, and nothing joined them. Answering "is it
all fine" meant opening six pages and holding the answers in your head.

It adds **no table, no index and no new measurement**. Where a number here
disagrees with the page that owns it, the page is right and this is the bug.
Every figure on `/cockpit` links back to its owner for exactly that reason.

It is deliberately **not** `/alerts`. That page grades what the *chain* is
doing — reverts spiking, a large transfer, a validator missing blocks — and one
of its rules costs a block fetch per block. This one grades whether the
explorer and the chain are *working*, and it is polled on a timer by a screen
nobody is standing in front of, so it must stay cheap. `/cockpit` links to
`/alerts`; it does not inline it.

## The grading

`pkg/httpapi/cockpit.go` turns the joined facts into a ranked list of findings
and one verdict. Each finding is **named** — "degraded" on a wall display tells
a passer-by nothing they can act on, "gnoland1 is wedged" sends them to the
right person — and carries its threshold in its detail line, the same rule
`/alerts` follows: a threshold a reader cannot see is an assertion.

| finding | fires when | severity |
|---|---|---|
| `chain` | the per-network diagnosis is not healthy | `down` for `wedged`, `indexer_down` and `unknown`; `warn` for `syncing`, `stale` and `node_unreachable`, which are "we cannot see" rather than "it has stopped" |
| `index-unreadable` | reading a network's stored tip failed outright, which is a different thing from there being nothing to read | `down` |
| `not-indexed` | this instance has stored no block for a network, ten minutes or more after it started | `warn` |
| `lag` | the stored tip trails the chain head by 50 blocks, or 1000 | `warn` / `down` |
| `sync` | sync passes for a network are failing consecutively; 5 in a row is an outage | `warn` / `down` |
| `errors` | more than 5% of the last 24 hours' requests answered 4xx or 5xx, over at least 200 requests, crawlers excluded | `warn` |
| `log-dropped` | the request log's buffer has thrown rows away under load | `warn` |

The verdict is `ok`, `degraded` or `down`, with a headline that **counts**
rather than names: "one thing is down", "3 things need a look", "all clear".
The naming is the findings' job and it is directly underneath on both pages.

Findings are stable-sorted so every `down` precedes every `warn`, and the rule
order is preserved within a severity. The widget shows the first three, which
is the whole reason the ranking has to be the server's.

## Cost, and why it is polled safely

`/api/cockpit` costs one indexer round trip and four node RPCs per configured
network, plus roughly twenty indexed range scans and one aggregate over
`storage_events`. That is the same order as `/api/sanity/overview`, which the
same instance already serves.

Three things keep a wall display from turning into load:

1. The endpoint's cache TTL is **60 seconds**, not the default 30 — above the
   warmer's 25-second pass, so an entry never expires between two passes.
2. `/api/cockpit` is on the **warmer's** list, so the display is served an
   entry somebody else already paid for.
3. The pages poll with a **chained `setTimeout`**, never `setInterval`. A
   background tab throttles timers, and an interval whose callback outlasts its
   period queues the overruns to fire back to back the moment the tab is
   foregrounded.

`/cockpit` re-reads every 60 seconds, `/widget` every 30.

## Surviving a month on a wall

The widget is the only page here that can be left running until the panel
remembers it. What that costs in code:

- **Nothing animates.** The only thing that moves is a number changing.
- **A deploy reloads it.** The tab is running whichever build's JavaScript it
  loaded, and any deploy can change the payload's shape. When
  `service.git_hash` moves, the page reloads itself, once — never on the first
  response, so a restart into the same build reloads nothing.
- **One bad response does not blank the screen.** The last good payload is kept.
- **It says when it cannot reach gnoscope.** That is the one finding the server
  cannot report, and it is the most important thing a wall display can say: a
  frozen screen and a healthy chain look identical otherwise.
- **The five numbers are always the same five.** A tile that appears and
  disappears moves the four beside it, and a layout that moves is one nobody
  learns the shape of.

## Framing: the header that makes it a widget

A page at `/widget` that does not say whether it may be framed is a page with a
small stylesheet. The parent cannot find out on its own: cross-origin, a
refused frame still fires `load`, so what a viewer gets is the browser's grey
error page covering the tile that would have named the tool.

So `/widget`, **and only `/widget`**, sends a
`Content-Security-Policy: frame-ancestors` header, and its value is
**configurable at runtime**:

```
-widget-frame-ancestors "'self' https://status.example"
GNOSCOPE_WIDGET_FRAME_ANCESTORS="'self' https://status.example"
```

Flag, then environment, then the default. The default is `*`.

**Why permissive, and why sent at all.** gnoscope.com is a read-only public
explorer: every byte this page can show is already there for anyone who asks,
so a list of permitted origins buys nothing and costs the one thing a widget
exists for. Worse, a list published on a public endpoint publishes the names of
whoever is allowed to frame it, which is somebody's private infrastructure and
not gnoscope's business.

An *absent* directive would also permit framing, which is what
`vigil.moul.io/widget` does. It is sent anyway because absence permits by
saying nothing: a reverse proxy adding `X-Frame-Options` to every response — a
security-header block nobody remembers configuring is the usual case — would
silently take the widget with it. A present `frame-ancestors` overrides
`X-Frame-Options` in every browser that implements both, so declaring
permission is the only form of permission that survives the proxy.

`"none"` sends no header at all. It is spelled out rather than being the empty
value, because an empty value is what a shell passes by accident.

The value is validated at startup, not at request time: a `;`, a `,`, a
newline or anything non-ASCII is refused and the process does not start. It
lands in a response header, so a newline is header injection and a `;` is a
second directive smuggled into a field that should hold only hosts.

A narrowed list is only as good as its least convenient origin. A wildcard like
`https://*.example.com` does not match a bare LAN address, so a hub opened at
`http://10.0.0.1` loses the tile while the same hub opened by name keeps it.

## Chrome

`/widget` reuses the shell's existing `embed` mode rather than growing a second
chromeless mode: the same `.embed` class on `<html>`, applied by the same
pre-paint script, triggered by the path as well as by `?embed=1`. Deferring it
to the SPA's routing would let the header render and then vanish, which inside
an iframe reads as a layout glitch on every navigation.

One addition: embed mode drops the page background so a host page can decide
it, and a television is not a host page. When `/widget` is the top-level
document it also gets `widget-standalone`, which puts the background back.

## Where the code is

| what | where |
|---|---|
| the roll-up and the grading | [`pkg/httpapi/cockpit.go`](../pkg/httpapi/cockpit.go) |
| the cache TTL and the warm target | [`pkg/httpapi/cache.go`](../pkg/httpapi/cache.go), [`warmer.go`](../pkg/httpapi/warmer.go) |
| the framing header | [`pkg/web/web.go`](../pkg/web/web.go) |
| both pages | `pkg/web/frontend/index.html`, under "`/cockpit` and `/widget`" |
| the grading's tests | [`pkg/httpapi/cockpit_test.go`](../pkg/httpapi/cockpit_test.go) |
| the header's tests | [`pkg/web/widget_test.go`](../pkg/web/widget_test.go) |
| the pages' tests | [`e2e/tests/cockpit.spec.js`](../e2e/tests/cockpit.spec.js) |
