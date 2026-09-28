# Badges

`GET /_badges/…` serves small SVG cards that other documents embed as images.

The point is a graph that survives leaving mygnoscan. A gno realm's `Render()`
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
	return "![usage](https://mygnoscan.example/_badges/realm/r/moul/home?network=mainnet)\n"
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
