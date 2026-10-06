# `/code`: the code map

Every package deployed on one chain, readable in the browser: a map of all the
code at the front door, a file tree beside a highlighted file, and a palette
that jumps to any package or file in a keystroke. It is the place to discover
what is on a chain, where `/realm/<path>` is the place to see what one realm is
doing right now. Its timeline, `/code/timeline`, is the same chain as a feed:
what was published, newest first.

The page is called **code map**, and it sits in the rail under **developer**,
first, with **timeline** after it. It used to be a top-level `code` entry; the
URLs did not move, so every `/code` link still opens the same page.

## Routes

| URL | what it shows |
|---|---|
| `/code` | the map of the whole chain |
| `/code?zoom=<namespace>` | the map zoomed into one namespace |
| `/code?zoom=gno.land/<r\|p>/<path>` | the map zoomed into one package, one cell per file |
| `/code/<r\|p>/<path>` | a package, with its main file open |
| `/code/<r\|p>/<path>/-/<file>` | one file |
| `/code/<r\|p>/<path>/-/<file>#L42` | that file, line 42 marked and scrolled to |
| `/code/<r\|p>/<prefix>` | a path with no package of its own (`r/gnoswap`): what is under it |
| `/code/<r\|p>/<path>?compare=<from>..<to>` | what changed between two submissions at the path: either height may be left out (`..` is the last publication against the one before it) |
| `/code/<r\|p>/<path>?compare=..<to>&base=<r\|p>/<other>` | the same against another path, the previous generation |
| `/code/timeline` | every publication on the chain, newest first, under a calendar of the last year |

Every URL carries `?network=`. The map's controls ride along as `?size=bytes`
and `?color=age|kind` when they differ from the default. The `/-/` separator is
gnohub's, for the same reason: a package path is arbitrarily deep, so a
trailing file name is ambiguous without one.

A package's **main file** is the file named after it (`home.gno` in
`r/moul/home`, `addrset.gno` in `p/moul/addrset/v1`), else its largest source
file that is not a test, else its first file.

## One chain at a time

The endpoints behind this page refuse "all networks", because one package path
names a different package on every chain. In all-networks mode the page picks
one (mainnet when it is configured, otherwise the first configured chain),
selects it in the header, writes it into the URL, and shows a row of chain
buttons beside the title. Switching chain keeps the path, so a package that
exists on both stays open, and one that does not says so.

## The map

A squarified treemap drawn on a canvas, nested namespace, then package, then
file. The area is lines (or bytes); the colour is per package:

| colour | what it means |
|---|---|
| `activity` (default) | calls in the tree's window (30 days), on a log scale |
| `age` | the stamp height, by rank: genesis is the oldest end of the ramp |
| `kind` | realm or pure package |
| `tag` | the package's first [code tag](tags.md) in rule order, which puts the specific ones (`token`, `payments`) ahead of the common ones (`render`). The legend's chips pick one tag, and the map lights that tag alone (`?tag=`) |

When nothing on the chain was called in the window, the map opens on `age`
instead and the legend says why: a map drawn in one flat colour shows nothing.
A colour the reader picked is kept. Test files are drawn faded, and files that
are not Go (`gnomod.toml`, a README) more faded still.

A click zooms: a namespace, then a package, then a file opens in the explorer.
Each zoom is a history entry, so back zooms out. Names are drawn only where
they fit, and the hover card carries the numbers for every cell. The layout is
vanilla JavaScript, not the d3 the contracts map uses: it is the front door of
the page, and it should not wait on a third-party script to draw.

## The explorer

The tree is virtualized: only the rows in view are in the document, so the
3,000-odd files of a busy chain cost what a screenful does. The filter box
narrows it to packages and files whose path or name contains what is typed.

The file is drawn by `codeBlock`, the same renderer the realm page and gnohub
use: the regex paint first, then the server's tokens from
`/api/source?tokens=1` for the same stamp. An import of a package on this chain
opens that package in `/code`; a reference opens its declaration, in the same
file or another one.

Above the file sit the package's stamp (block and age), its size, its calls,
callers and importers in the window, links to the realm page and to gnohub,
and the **version rail**: every generation of the same app on this chain
(`v0`, `v1`, `bubblerumble2`, ...) in deploy order, the current one marked,
from the tree's `fam` key. When the manifest counts more than one submission,
the rail also draws every `MsgAddPackage` at the path as a dot, failed ones in
red, the one whose source is shown ringed. A dot opens what that submission
changed against the successful one before it; "compare with the previous
publication" does the same for the current stamp, and a generation after the
first carries "compare with" its predecessor.

## What changed

`?compare=` turns a package's page into a diff of two submissions, from
[`/api/source/{path}/diff`](api.md#what-changed-between-two-publications): the
two sides (path, block, transaction, `failed` on a refused one), the file and
line counts, then the exported API, which says "no exported signature
changed" when that is the answer, and lists added, removed and changed
declarations with the old and the new signature otherwise. Under it each
changed file is drawn as its hunks, line numbers on both sides, removed lines
red and added ones green, `.gno` lines through the same regex highlighter the
viewer paints first with, and the unchanged stretches between hunks folded
into one line that says how long they are. A header folds a file; a file with
more than 600 changed lines starts folded. Unchanged files are named at the
end.

Three ways in besides the rail: a timeline card for a republication or a new
version carries "what changed" (a new version against the previous
generation's latest source, `prev`), and the realm page carries "changes"
beside its gnohub link.

## The timeline

A feed of every `MsgAddPackage` on the chain, the way a GitHub dashboard lists
what was pushed, drawn from `/api/code/timeline` (see
[api.md](api.md#the-code-timeline) for how each row is classified). Rows are
grouped under a header per UTC day ("today", "yesterday", then dates), each one
a card: the publisher's identicon and name, what happened ("published",
"released bubblerumble6 (6th version)", "republished", "tried to publish"), a
badge for the kind, files, lines and imports when the source is still the
stored one, the package's own one-line summary, the age (the date on hover),
the block and the transaction, and on a republication or a new version a
"what changed" link into the diff. The path opens in the code map, the
publisher opens its address page. The page never says "deploy" (see the glossary).

Names are short, because a card is read at a glance. The publisher is its
`@name` when it registered one on this chain, else a label the site already
knows it by, else the short address (`g1leu8d2…95wr`). The package is its own
name with its version (`bubblerumble6`, `relay/v0`), with the namespace in
front only when it is a name somebody chose (`moul/relay/v0`, `gnoswap/pool`),
never a `g1...` address; a path more than two segments under its namespace
keeps the tail (`moul/…/governor/v1`). The whole address and the whole path are
on hover and behind a copy button on each. One helper, `shortPkg`, does the
shortening, beside `truncAddr` which does the address.

A realm's card carries a thumbnail of what it looks like, from `/api/shot`
(the listings' size, `thumb`, in the render crop), lazy-loaded in a box of
fixed size (128 by 72) so a card is one height before, during and after the
picture arrives, and a picture that fails is replaced by a tile of the same
box. A pure package has no page to show and carries none, and neither does a
refused submission. With no capture service configured there are no pictures
at all, not a column of placeholders.

Above the feed is a calendar of the last 365 days, 53 weeks by 7, five shades
from nothing to the busiest day, from `/api/code/timeline/heatmap`. Hovering a
day says how many packages were published on it; clicking one narrows the feed
to that day, and clicking it again, or the chip it adds, widens it back.

Each current card carries the package's [code tags](tags.md), the line that
earned each one on hover; a row of tag chips under the filters, with how many
packages carry each, narrows the feed (and the calendar) to one (`tag`).

The chips choose the kinds (all, new only, or any mix of new, new version and
republished) and whether failed submissions are listed; the two boxes narrow to
a namespace or a publisher (an address or a registered name), and the calendar
follows them. Every filter is in the URL (`kind`, `failed`, `ns`, `creator`, `tag`,
`day`), so a view is a link.

The first page and the calendar go through the site's payload cache, so a
second visit paints before the network answers; the next page is fetched when
the end of the list comes within a screen and a half, and a "load more" button
does the same by hand. Measured 2026-10-05 against a local copy of gnoland1,
headless Chromium: the first card on screen 138 ms after navigation on a cold
cache, 68 ms on a reload.

## Keys

| key | where | what |
|---|---|---|
| `ctrl-k`, `cmd-k`, `/` | anywhere on `/code` | the palette |
| `j` `k` / arrows | tree, or the page when nothing is focused | move |
| `l` `h` / right, left | tree | open or close, or go to the parent |
| `enter` | tree | open the package or file |
| `space` | tree | open or close without navigating |
| `g` then `c` | anywhere | go to the code map |

`tag:<name>` in the tree filter or the palette narrows either to the packages
carrying that [code tag](tags.md) (`tag:defi`, `tag:nft pool`); the rest of
the query applies inside them. The package header carries its tags as chips,
the evidence on hover.

The palette scores every package path and every file path with a fuzzy
subsequence match: the best alignment of the query in the path, where a letter
that starts a path segment or continues a run counts for more than one strewn
along the way, and a match inside the package or file name for a little more
again. The top 50 are listed with the matched letters marked. With nothing
typed it lists the busiest packages.

## Speed

Measured 2026-10-03 against a local copy of gnoland1 (598 packages, 3,173
files, 437,673 lines), headless Chromium on an M-series laptop:

| what | time |
|---|---|
| the map's layout, 3,161 cells | 1.0 ms |
| a palette query over 3,773 paths | 0.25 to 0.9 ms |
| opening a file already fetched (another tab of the same package) | 6 to 9 ms, click to drawn and highlighted |

What makes the second and every later visit fast:

- The tree is fetched once per chain and kept in memory for its own
  `max-age` (60 s), and in the session cache so a reload paints before the
  network answers. After that the browser revalidates it with its `ETag`.
- File bodies and tokens are addressed by their stamp and immutable, so each
  one is fetched once and kept in memory; a second open of the same file is
  drawn in the same task as the click.
- Hovering a package or a file starts the fetch of its main file, its tokens
  and its manifest 60 ms later, without the refresh bar. Opening a file also
  warms the next one in its package.
- Opening another file of the package on screen redraws the file and nothing
  else.

## What it does not do

- **Live or parked.** The tree does not carry it (it would cost an RPC read per
  path); the realm page does.
- **A global palette.** `ctrl-k` belongs to `/code`. Elsewhere `/` still
  focuses the site search.
