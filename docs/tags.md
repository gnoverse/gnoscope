# Code tags

What a package's code does, as labels: `token`, `payments`, `events`,
`render`, and so on. Several per package, each one carrying the line of code
that earned it, shown on hover wherever a tag is drawn. The word itself is
in the [glossary](glossary.md); this page is how the labels are computed.

## Not the /apps category

`pkg/httpapi/apps_category.go` picks one of five buckets per app on /apps and
falls back to words in the path when the code says nothing. Tags are kept
apart from it on purpose, and differ in three ways:

- **Several at once.** A gnoswap realm is `defi`, `access-control` and
  `render` at the same time, and saying only one of them loses the other two.
- **Code only.** Every rule reads the source: an import, a call to a named
  standard-library function, a declaration. No rule looks at a word in a path,
  because a name is chosen by whoever typed it and proves nothing.
- **Always with evidence.** `imports gno.land/p/nt/grc20/v0 (token.gno:5)`,
  `calls banker.NewBanker (bazaar.gno:310) and 2 more`. A tag with no
  evidence is not a tag.

## The rules

One table, `tags.Rules` in [`pkg/tags`](../pkg/tags/tags.go), each row unit
tested with a case that earns it and a near miss that must not. Symbol rules
run on the parsed source (`go/parser`, as `pkg/srctok` does), so a call in a
comment or a string does not count, and the local name of an import is
resolved (`ur "chain/runtime/unsafe"` then `ur.OriginSend()`). Test files earn
nothing but `test-only`.

| tag | earned by |
|---|---|
| `token` | importing `grc20` or `grc20reg` |
| `nft` | importing `grc721` or `grc1155`, a fork's versioned name too (`grc721v2`) |
| `payments` | calling `banker.NewBanker` (`chain/banker`) with anything but `BankerTypeReadonly`, or `unsafe.OriginSend` (`chain/runtime/unsafe`) |
| `defi` | importing a `gno.land/r/gnoswap/` realm or `wugnot` |
| `governance` | importing a pure DAO library (an element ending in `dao`), defining the method `PreExecuteProposal` (the GovDAO's DAO interface), calling `dao.UpdateImpl`, or defining `UpdateImpl` |
| `social` | importing `boards` |
| `access-control` | importing `ownable`, `authorizable`, `authz`, `rbac` or `access` |
| `events` | calling `chain.Emit` |
| `render` | a realm defining a top-level `Render` |
| `library` | a pure package at least one other package on the chain imports |
| `test-only` | every `.gno` file is a `_test.gno` or `_filetest.gno` |

An import element matches as written or without its generation suffix:
`boards2` is `boards`, `grc721v2` is `grc721`, and `grc20` stays `grc20`.

## Calibration

Against a local copy of gnoland1, 2026-10-05: 598 packages, 1,479 source
files, every one of which `go/parser` reads.

| tag | packages | what calibration changed |
|---|---|---|
| `token` | 30 | a version-stripped match read `grc20` as `grc` and found 21; both spellings now |
| `nft` | 29 | same fix (0 before it), and `grc721v2` forks |
| `payments` | 72 | read-only bankers excluded; near misses are comments and `testing.SetOriginSend` in tests |
| `defi` | 30 | gnoswap's math libraries (`p/gnoswap/...`) alone do not count |
| `governance` | 5 | `imports r/gov/dao/...` dropped: `r/sys/names` reads the member store and is governed, not governance |
| `social` | 3 | `r/demo/profile` dropped: its one importer is `r/sys/namereg`, a registry |
| `access-control` | 43 | |
| `events` | 215 | |
| `render` | 334 | a `Render` method (`r/gnoswap/gov/governance/v1`) is not the realm's page |
| `library` | 181 | |
| `test-only` | 0 | |

55 packages carry no tag at all: mostly young pure libraries nothing imports
yet, and implementation realms that neither render nor emit.

**`defi` is not "token and payments".** Six packages carry both without
`defi`; four of them are DeFi (a DEX, an OTC desk, `wugnot` itself, a token
factory), and one is the GovDAO treasury. A rule that is wrong one time in six
is not code-derived evidence, so `defi` stays the gnoswap and wugnot imports,
and the two chips side by side say the rest.

## Where they are stored

`package_tags (network, path, tag, evidence)`, and
`package_tags_state (network, path, tx_hash, rules)` to remember which source
and which rule set a package's tags came from (a package with no tags is a real
answer and has no rows in the first table). `ReplacePackage` writes both in
the same transaction as the package's files; `SetDependencies` recomputes
`library` for every path whose importers it changed.

`RefreshPackageTags` catches up everything written another way: a database
from before the table, a package whose stored source moved, every package
after a bump of `tags.Version`. It runs once at startup, after the dependency
re-extraction, then every `-tags-interval` (10 minutes). When nothing moved it
is one query. Measured on that gnoland1 copy: 598 packages tagged from cold in
1.8 s.

## Where they are shown

Chips, one component (`tagChip`) and one colour per tag, on the timeline cards
and its tag filter, the code map (the package header, a `tag` colour mode,
`tag:<name>` in the tree filter and the palette), the realm page header, and
the /realms and /packages listings with a tag facet. The API is in
[api.md](api.md#code-tags).
