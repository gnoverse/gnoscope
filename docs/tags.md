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
nothing but `test-only`. Two rules look for a shape of code no import or name
can say (`tags.Pattern`): `upgradable`'s implementation swap and `time-based`'s
clock comparison.

| tag | earned by |
|---|---|
| `token` | importing `grc20` or `grc20reg` |
| `nft` | importing `grc721` or `grc1155`, a fork's versioned name too (`grc721v2`) |
| `payments` | calling `banker.NewBanker` (`chain/banker`) with anything but `BankerTypeReadonly`, or `unsafe.OriginSend` (`chain/runtime/unsafe`) |
| `defi` | importing a `gno.land/r/gnoswap/` realm or `wugnot` |
| `governance` | importing a pure DAO library (an element ending in `dao`), defining the method `PreExecuteProposal` (the GovDAO's DAO interface), calling `dao.UpdateImpl`, or defining `UpdateImpl` |
| `governed` | calling a GovDAO proposal constructor: `dao.NewProposalRequest*`, `dao.NewSimpleExecutor` or `dao.MustCreateProposal` (`gno.land/r/gov/dao`), or `NewSysParam*`, `NewSet*` or `Propose*` of `gno.land/r/sys/params` |
| `voting` | a realm defining a top-level `Vote`, `CastVote` or `VoteOnProposal` |
| `claims` | a realm defining a top-level `Claim`, `ClaimAll`, `ClaimReward`, `ClaimRewards`, `ClaimRefund` or `ClaimFees` |
| `social` | importing `boards` |
| `identity` | importing `gno.land/r/sys/users`, exactly |
| `access-control` | importing `ownable`, `authorizable`, `authz`, `rbac` or `access`; defining `TransferOwnership`, `AcceptOwnership`, `TransferAdmin`, `SetAdmin` or an owner check (`assertOwner`, `assertAdmin`, `assertIsAdmin`, `requireOwner`, `requireAdmin`, `onlyOwner`, `onlyAdmin`); or a method `TransferOwnership` or `AcceptOwnership` |
| `pausable` | defining `Pause`, `Unpause`, `IsPaused`, `SetPaused`, `setPaused`, `assertNotPaused` or `requireNotPaused`, or importing `pausable` or `halt` |
| `upgradable` | an exported top-level function assigning a package-level variable whose type is an interface the package declares (the facade's swap), or importing `upgradeable`, `upgradable` or `version_manager` |
| `events` | calling `chain.Emit` |
| `time-based` | an ordering comparison (`<`, `>`, `<=`, `>=`, `Before`, `After`) of `runtime.ChainHeight()`, `time.Now()`, `time.Since` or `time.Until`, directly or through a variable assigned from one in the same function, offset by `+`/`-` or converted; not against a `math` limit |
| `crypto` | importing `crypto/sha256`, `crypto/ed25519`, `crypto/merkle` or a `merkle` package |
| `math` | importing `uint256`, `int256`, `math/bits` or `math/overflow` |
| `cross-realm` | importing any `gno.land/r/` path |
| `render` | a realm defining a top-level `Render` |
| `ui` | importing `md`, `mdtable`, `mdlist`, `mdalert`, `mdform`, `markdown`, `ui`, `pager`, `svg` or `svgbtn` |
| `interactive` | a realm importing `txlink` or `helplink`, or with a string literal containing `$help&func=` |
| `routing` | importing `mux` or `realmpath` |
| `library` | a pure package at least one other package on the chain imports |
| `self-contained` | at least one source file, and no source file importing anything under `gno.land/` |
| `test-only` | every `.gno` file is a `_test.gno` or `_filetest.gno` |

An import element matches as written or without its generation suffix:
`boards2` is `boards`, `grc721v2` is `grc721`, and `grc20` stays `grc20`.

## Calibration

Against a local copy of gnoland1, 2026-10-05 (rule set 1) and 2026-10-06
(rule set 2, the rows from `governed` on): 598 packages, 1,479 source files,
every one of which `go/parser` reads.

Rule set 2 started from the data, not from a list of words: every import path,
every call of an imported function and every declared function name across
the chain, ranked by how many packages use it, with what rule set 1 already
covered taken out. A candidate stayed if a reader of the timeline would learn
something from it, it fired on fewer than 60% of packages, and it reached at
least three of them or had a reason to exist below that. Each one was then
read against its hits and its near misses, and the false positives found that
way are the near-miss cases in `pkg/tags`' tests.

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
| `governed` | 12 | `dao.ProposalID` (the GovDAO's own implementation reads one) is not building a proposal; `r/sys/params`' constructors added for `r/gnops/valopers/proposal`, which builds its requests through them |
| `voting` | 12 | realms only: `grc20votes` and `checkpoint` are libraries that count voting power, they take no vote |
| `claims` | 26 | not `Claim*`: a fact-staking realm's `ClaimTitle` and `ClaimStatus` read a claim and pay nothing |
| `identity` | 8 | the exact path: `r/sys/users/init` is a realm of its own |
| `access-control` | 97 (43 in set 1) | widened to the hand-rolled admin: 54 more packages (52 realms, and `p/nt/ownable` itself by its methods) keep their own admin seat and owner check without importing a library |
| `pausable` | 26 | a `Pause` method (a game's clock) is not the realm pausing |
| `upgradable` | 15 | the swap must be exported and outside `init`; an unexported setter or a struct value is configuration. Implementations registering with a facade are not tagged: they are versions, the facade is what upgrades |
| `time-based` | 60 | 173 packages call `ChainHeight` and most only store it. A first cut that let any expression mentioning a height carry it to a variable found 72, among them a list length (`append` of a record holding a height) and a dice roll (`height % 6`); now only `+`, `-` and conversions carry it. A height compared to `math.MaxUint32` is an overflow guard |
| `crypto` | 41 | `crypto/bech32` is an address encoding and left out |
| `math` | 49 | plain `math` (floats, `MaxInt64`) is not this |
| `cross-realm` | 116 | |
| `ui` | 103 | `markdown/sanitize` counts: escaping user markdown for a page is building one |
| `interactive` | 22 | realms only: `txlink` itself and the libraries wrapping it draw links for others |
| `routing` | 24 | |
| `self-contained` | 154 | tests may import anything; a package that is all tests is not this |

Before rule set 2, 55 packages carried no tag; after it, 15. The 15 are young
pure libraries nothing imports yet whose imports are all general-purpose
(trees, lists, sets, `ufmt`, `uassert`), and one realm, `r/moul/demo/wikicoin/v0`,
that neither renders nor emits. The seven versioned implementations of the
upgrade patterns under `r/moul/x/upgrade/` moved to `cross-realm`.

Tried and dropped, from the same data:

| candidate | why not |
|---|---|
| `random` | 3 packages, all `math/rand`, which on chain is seeded from public state; a chip saying "random" would mislead |
| `airdrop` | 3 packages pair a `Claim` with a merkle proof, and `claims` beside `crypto` already says it |
| `game` | no code signal: the games share no import, call or name a rule could read |
| `oracle`, `multisig`, `json` | 0, 0 and 2 packages |
| `params` | 3 importers of `r/sys/params`, all building proposals, so folded into `governed` |
| reads the caller | `unsafe.PreviousRealm` or `OriginCaller` in 122 packages, for an owner check and for stamping an author alike: no single meaning |
| holds a balance | `chain.PackageAddress` in 46, half of them already `payments`; for the rest the call does not say what the address is for |
| `token` by interface | defining `BalanceOf`, `TotalSupply` and `Transfer` is also every NFT collection |

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
