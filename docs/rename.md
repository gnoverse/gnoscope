# mygnoscan is now gnoscope

Renamed on **2026-09-28**. Same tool, same database, same API, same URLs.

If you are here because a link, an import or a bookmark stopped looking familiar:
nothing was removed, and the table at the bottom says where everything went.

## Why

`mygnoscan` parses as "my gnoscan", and [gnoscan.io](https://gnoscan.io) is
Onbloc's explorer. The name presented this as a personal skin of somebody else's
tool, which is the opposite of what it is: the whole reason it exists is the
question generic explorers answer badly, which is what imports what and who calls
it.

That was tolerable while it was a thing one person opened. It stopped being
tolerable the moment the name started appearing on other people's pages: the
badges under [`docs/badges.md`](badges.md) put a footer on any document that
embeds one, including realm pages on gno.land.

Second reason, smaller and duller: the Go module path was `github.com/moul/mygnoscan`
while the repository has lived at `gnoverse/` for a long time. Renaming fixed a
mismatch that was already there.

## What changed

| | before | after |
|---|---|---|
| name | mygnoscan | **gnoscope** |
| site | `mygnoscan.moul.p2p.team` | **`gnoscope.com`** |
| repository | `github.com/gnoverse/mygnoscan` | **`github.com/gnoverse/gnoscope`** |
| Go module | `github.com/moul/mygnoscan` | **`github.com/gnoverse/gnoscope`** |
| binary | `mygnoscan` | **`gnoscope`** |
| container image | `ghcr.io/gnoverse/mygnoscan` | **`ghcr.io/gnoverse/gnoscope`** |
| default database | `mygnoscan.db` | **`gnoscope.db`** |

## What did not change

- **Every URL.** Paths, query parameters, `/api/*`, `/_badges/*`, `/mcp`: identical.
  `mygnoscan.moul.p2p.team/...` answers a permanent redirect to the same path on
  `gnoscope.com`, so an old link lands on the page it always did.
- **The API.** No endpoint renamed, no field renamed, no response shape touched.
  A client written against mygnoscan works against gnoscope with no change beyond
  the host, and it does not need that either while the redirect stands.
- **The database.** Same schema, same file. An existing deployment keeps its data;
  point the new binary at the old `.db`, or rename the file, whichever you prefer.
- **The MCP endpoint**, its tools and their arguments.

## If you depend on it

**A link or a bookmark:** nothing to do. The redirect is permanent and is not
scheduled to be removed.

**A Go import:** the module path moved, so this is the one thing that needs a
change on your side.

```sh
go mod edit -replace github.com/moul/mygnoscan=github.com/gnoverse/gnoscope@latest  # or
grep -rl github.com/moul/mygnoscan . | xargs sed -i 's|github.com/moul/mygnoscan|github.com/gnoverse/gnoscope|g'
```

**A container:** `ghcr.io/gnoverse/mygnoscan` keeps its existing tags and stops
gaining new ones. Move to `ghcr.io/gnoverse/gnoscope`.

**A clone:** GitHub redirects the old repository path indefinitely, so an existing
remote keeps working. To make it explicit:

```sh
git remote set-url origin git@github.com:gnoverse/gnoscope.git
```

## The name

A scope is an instrument for looking closely at one thing. The mark is an iris
diaphragm: a lens, and also a hub with three edges leaving it, which is the shape
of the answer this tool actually gives.
