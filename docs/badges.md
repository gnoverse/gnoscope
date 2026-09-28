# Badges

`GET /_badges/…` serves small SVG images that other documents embed.

Two shapes, for two readers: a **card** with a graph on it, which is a figure
inside a document, and a **shield**, the one-line label/message plate a README
carries in a row at the top.

The point is a graph that survives leaving gnoscope. A gno realm's `Render()`
returns markdown and gnoweb turns that into HTML, so an `![](…)` is the only
hook a realm has into anything the chain does not store. Point one at a route
here and a realm page shows its own usage graph, drawn from the index, with the
chain holding none of it.

They work anywhere else markdown does too: a README, a wiki, a status page.

## Routes

### `GET /_badges/realm/{path...}`

One realm's activity over a window. The path is the package path with the
`gno.land/` prefix dropped, and a trailing `.svg` is optional.

```
/_badges/realm/r/gnoland/blog?network=mainnet
/_badges/realm/r/gnoland/blog.svg?network=mainnet&metric=callers&days=90
```

| parameter | values | default |
|---|---|---|
| `network` | a configured network ID | every configured network the package is in |
| `metric` | `messages`, `callers` | `messages` |
| `days` | 2 to 365 | 30 |
| `theme` | `auto`, `light`, `dark` | `auto` |

`messages` is everything aimed at the realm: direct `MsgCall`s plus the
`MsgRun`s that reference it, the same union the realm page's calls tab counts.

`callers` is the **distinct addresses per bucket**, which is a different shape
entirely: one bot calling hourly draws a wall on `messages` and a flat 1 on
`callers`. It does not sum across buckets, and the badge does not print a total
for it for that reason: an address that comes back on three days is in all three.

### `GET /_badges/network`

Chain-wide activity: calls, deploys, `MsgRun`s and bank sends, the same four the
analytics page charts. `?network=` is **required** here: there is no package to
scope by, so without one it would count every chain ever synced into one line,
and a badge has no room for a network picker.

| parameter | values | default |
|---|---|---|
| `network` | a configured network ID | *required* |
| `days` | 2 to 365 | 30 |
| `theme` | `auto`, `light`, `dark` | `auto` |

### `GET /_badges/shield/{kind}/{path...}`

The other shape: one question, one word, in the 20-pixel label/message plate
shields.io made the convention of every repository's front page. Where a card
is a figure inside a document, a shield stands in a row at the top of a README
beside a CI badge somebody else drew, so the geometry, the 11px Verdana stack
and the one-pixel text shadow are copied from shields deliberately.

```
/_badges/shield/status/r/moul/home?network=mainnet
/_badges/shield/txs/r/moul/home?network=mainnet
/_badges/shield/txs/r/moul/home?network=mainnet&days=30
/_badges/shield/users/r/moul/home.svg?network=mainnet&style=flat-square
```

| kind | says | where it comes from |
|---|---|---|
| `status` | `live`, `parked`, `absent`, `unknown` | the chain, live (`vm/qpkgmeta_json`) |
| `txs` | distinct transactions that reached the realm | the index |
| `messages` | the messages inside them, calls and `MsgRun`s alike | the index |
| `users` | how many different addresses sent them | the index |
| `version` | `r3`, the third accepted submission at this path | the index |

| parameter | values | default |
|---|---|---|
| `network` | a configured network ID | every configured network the package is in, and the first configured network for `status` |
| `days` | 1 to 365 | all of history |
| `label` | any text, replacing the left plate | per kind |
| `color` | a shields colour name or a hex triplet | per kind |
| `labelColor` | same | `#555` |
| `style` | `flat`, `flat-square` | `flat` |

`status` is the only kind that asks the chain rather than the index, because
`absent` has to be answerable for a path nothing has ever been deployed to: a
badge in the README of a realm that is not live yet is exactly the case it
exists for. An RPC that does not answer reads `unknown`, never `absent` — the
difference is "we could not ask" versus "your realm is not there", and one of
those is alarming.

`version` is the closest thing a chain can answer. gno stores no version field,
so this counts the submissions at the path that were accepted: `r3` is the
third release, whatever the source calls itself. A package the index holds with
no submission of its own arrived in genesis and says `genesis` rather than a
number.

`days` defaults to all of history here and to 30 on the cards above, on
purpose: a graph needs a window to be drawn over, while the number a README
wants is usually the total.

**A path the index does not hold is explained, not refused.** Writing a badge
into a README before the realm is deployed is a normal thing to do, and "no
such package" under it reads as a typo its author would then go hunting for. So
the counting kinds ask the chain what the path is, and say which case it is:
`not deployed`, `parked`, or `0` for a realm the chain holds and the index has
not caught up with. Only when the chain cannot be reached either does the badge
report a failure, because then nothing here knows anything about the path.

### `GET /api/shield/{kind}/{path...}`

The same five answers as [shields.io endpoint
JSON](https://shields.io/badges/endpoint-badge), for anyone who would rather
shields drew the badge:

```
https://img.shields.io/endpoint?url=https%3A%2F%2Fgnoscope.example%2Fapi%2Fshield%2Ftxs%2Fr%2Fmoul%2Fhome%3Fnetwork%3Dmainnet
```

Worth the extra hop when you want a style this renderer does not draw
(`for-the-badge`, `social`, `plastic`), a `logo=`, or one CDN serving every
badge on the page. The cost is that the badge now depends on two hosts instead
of one.

It answers `200` with `"isError": true` on a failure rather than a 4xx:
shields draws its own generic error for a non-200 and throws away the body that
said which path was not found.

## Putting one in a README

A badge carries no link of its own, so wrap it:

```markdown
[![realm](https://gnoscope.example/_badges/shield/status/r/moul/home?network=mainnet)](https://gnoscope.example/realm/r/moul/home)
[![txs](https://gnoscope.example/_badges/shield/txs/r/moul/home?network=mainnet)](https://gnoscope.example/realm/r/moul/home)
[![users](https://gnoscope.example/_badges/shield/users/r/moul/home?network=mainnet)](https://gnoscope.example/realm/r/moul/home)
```

GitHub does not fetch these from the reader's browser: it proxies them through
camo, which fetches once and caches for its own interval. So a number on a
README updates when camo refreshes it, not when `max-age` expires, and a badge
that looks stale on GitHub is usually not stale here. `Cache-Control` and the
`ETag` are still the right headers to send, because every other reader of a
README (a wiki, a docs site, a terminal client) does honour them.

## Behaviour worth knowing

**The bucket size is chosen, not asked for.** Up to 3 days is hourly, up to 120
daily, beyond that weekly. A badge has no zoom control, so `granularity=hourly`
over a year would put 8,760 points 0.05 pixels apart with no way to tell that is
what happened.

**Buckets are dense.** A day with no traffic is a zero, not a gap. A sparkline
drawn from a sparse series closes its own gaps, so a realm that went quiet for a
fortnight would be drawn as one that declined gently over it.

**Failures are drawn, not returned.** An `<img>` has no error channel: a browser
handed a 404 fires `onerror` and paints the broken-image glyph, discarding a
body that could have said `no such package: gno.land/r/moul/hom`. So every
failure is a 200 carrying a card that says what went wrong. Everything that is
not a browser can still tell them apart: an error badge carries
`X-Badge-Error: <reason>` and `Cache-Control: no-store`, so a monitor can alert
on the header and no cache pins a typo's answer.

**They are cached at both layers.** `Cache-Control: public, max-age=300,
stale-while-revalidate=600` plus an `ETag` over the rendered bytes, and the
response cache keeps the aggregate. A badge is fetched on every read of whatever
embeds it, by readers who never come here, which is why this is part of the
feature rather than a nicety.

**They reference nothing external.** An SVG loaded through `<img>` runs in the
SVG spec's secure animated mode: no scripts, no external references, no web
fonts, no fetches of any kind. Everything is inline, the font is a system stack,
and `theme=auto` resolves through `prefers-color-scheme`, which is evaluated
against the reader's *system* setting, so the embedding page's own light/dark
toggle is invisible from inside an image. Pass `theme=light` or `theme=dark`
where the surrounding document knows better.

## Embedding in a gno realm

```go
func Render(path string) string {
	return "![usage](https://gnoscope.example/_badges/realm/r/moul/home?network=mainnet)\n"
}
```

⚠️ **On gno.land this renders as a broken image unless the badge host is in
gnoweb's CSP allowlist.** gnoweb serves a `Content-Security-Policy` whose
`img-src` is a hardcoded list (`cspImgHost` in
[`gno.land/cmd/gnoweb/main.go`](https://github.com/gnolang/gno/blob/master/gno.land/cmd/gnoweb/main.go)),
and nothing about the realm or the markdown is at fault when an image outside it
does not appear. Read it off any realm page to see the current list:

```bash
curl -sI https://gno.land/r/gnoland/blog | grep -i content-security-policy
```

As of 2026-09-28 that list carries `'self'`, `data:`, and a handful of
providers: `gnolang.github.io`, `assets.gnoteam.com`, `sa.gno.services`, imgur,
`*.github.io`, `github.com`, `*.githubusercontent.com`, `ipfs.io` and
`cloudflare-ipfs.com`. The source comment above the list invites additions by
pull request.

Three ways through it, in the order they are worth trying:

1. **Add the badge host upstream.** One line in `cspImgHost`. Live, no mirror,
   nothing to keep in sync, but it takes a gnoweb release and deploy to reach
   gno.land.
2. **Mirror the SVGs to a GitHub Pages site**, which is already allowlisted via
   `*.github.io`. Works today; as fresh as whatever refreshes it.
3. **Inline the SVG as a `data:image/svg+xml` URI in the realm's own markdown.**
   Also works today: gnoweb's image validator rejects every `data:` URI *except*
   `image/svg+xml`
   ([`ext_imgvalidator.go`](https://github.com/gnolang/gno/blob/master/gno.land/pkg/gnoweb/markdown/ext_imgvalidator.go)).
   The bytes then live on chain and refreshing them costs a transaction.

Outside gno.land (a README, a wiki, a docs site) none of this applies and the
plain URL is enough.
