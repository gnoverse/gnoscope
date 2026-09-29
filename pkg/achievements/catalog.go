// Package achievements is the catalog of things an address can have done on
// chain, and the SQL that decides who has done them.
//
// The point is not gamification for its own sake. gno.land has a long list of
// capabilities most people never find out exist — that you can wrap ugnot, that
// a realm can import another realm's package, that an account can delegate a
// scoped signing key and revoke it later — and none of them are discoverable by
// reading a block explorer's tables. An achievement names the capability, says
// what it is for, and tells a reader the one command that earns it. The badge is
// the hook; the `How` line is the actual product.
//
// Two rules hold this together:
//
//   - An achievement is a *fact from the index*, never a judgement. Every
//     definition below is one query over tables this instance already fills from
//     the chain, and it answers "when did this address first do this" with a
//     block height and a transaction hash. Nothing is awarded by hand, nothing is
//     inferred from a heuristic, and a claim nobody can click through to is a
//     claim this package will not make.
//   - The catalog is append-only in spirit. Slugs are the identity of a badge
//     and they leak into URLs (/directory/people?has=first-realm) and into the
//     achievements table's primary key, so renaming one silently unawards it for
//     everybody. Add definitions; do not repurpose them.
//
// Adding one is a single entry in Catalog below plus a row in the catalog test.
// The rollup picks it up on its next pass and backfills every address that ever
// qualified, because every query is over all of history rather than a window.
package achievements

import "fmt"

// Group buckets the catalog for display. The order of the constants is the
// order the groups are shown in, which is roughly the order a newcomer meets
// them: you send coins before you deploy a realm, and you deploy a realm before
// you delegate a key to it.
type Group string

const (
	GroupStart    Group = "start"
	GroupVolume   Group = "volume"
	GroupBuild    Group = "build"
	GroupIdentity Group = "identity"
	GroupMoney    Group = "money"
	GroupKeys     Group = "keys"
	GroupTools    Group = "tools"
	GroupGovern   Group = "govern"
)

// GroupOrder is the display order, and the only place it is defined.
var GroupOrder = []Group{
	GroupStart, GroupVolume, GroupBuild, GroupIdentity,
	GroupMoney, GroupKeys, GroupTools, GroupGovern,
}

// GroupLabel is what a reader sees above each bucket.
var GroupLabel = map[Group]string{
	GroupStart:    "getting started",
	GroupVolume:   "going the distance",
	GroupBuild:    "building",
	GroupIdentity: "identity",
	GroupMoney:    "money",
	GroupKeys:     "keys and sessions",
	GroupTools:    "tools of the trade",
	GroupGovern:   "governance",
}

// Def is one achievement.
type Def struct {
	// Slug identifies the badge forever. Kebab-case, stable, never reused.
	Slug string `json:"slug"`

	// Name is the badge as a reader sees it, phrased as the deed rather than
	// the reward: "Published a realm", not "Realm Master". A badge that names
	// the deed teaches the deed.
	Name string `json:"name"`

	// Emoji is the whole icon. No sprite entry, no asset: the catalog is data
	// that ships in JSON and gets rendered as text, so a new badge costs
	// nothing in the frontend.
	Emoji string `json:"emoji"`

	Group Group `json:"group"`

	// What says, in one line, which on-chain fact unlocked this. It is the
	// honest description of the query below it, so that a reader who disagrees
	// with their badge can tell exactly what was measured.
	What string `json:"what"`

	// How is the line that makes this package worth building: what somebody
	// who does *not* have the badge would do to earn it. Written as an
	// instruction, not a description, and naming the real command or realm
	// wherever one exists.
	//
	// ⚠️ Never a gas number. Four of these shipped with one, invented to look
	// plausible, and every one was wrong: `-gas-wanted 200000` on a send that
	// costs 1,238,665 when it has to create the account, `2000000` on a call
	// that measured 11,732,203. gno prices a transaction on what the code
	// actually does, so the number is not knowable from the shape of the
	// command; `-simulate only` is how the chain tells you, and saying that is
	// the instruction worth giving. A figure typed here is a figure a stranger
	// pastes.
	How string `json:"how"`

	// SQL yields one row per address that has ever unlocked this, as
	// (address, block_height, block_time, tx_hash), where the three trailing
	// columns describe the *first* time it happened.
	//
	// The network is bound as the named parameter @net, which SQLite binds to
	// every occurrence from a single sql.Named. That is deliberate: every
	// table here is network-scoped (see AGENTS.md's first invariant) and a
	// query that forgets the filter silently merges two chains' histories into
	// one address's timeline.
	//
	// The first-occurrence columns rely on SQLite's documented bare-column
	// rule: in a query whose only aggregate is a single MIN(), the bare columns
	// are taken from the row that produced the minimum. That is what makes
	// "the height, time and hash of the first one" a plain GROUP BY rather than
	// a window function over the whole table.
	//
	// Not exported as JSON. A reader is owed What and How; the SQL is an
	// implementation detail and putting it in the API would freeze it.
	SQL string `json:"-"`

	// Live marks a badge whose SQL can UNDER-report, so an address's own page
	// also asks the chain and ORs the answer in.
	//
	// It is a supplement, never a second source of truth: a live read can only
	// ever add a badge the index missed, so the two cannot disagree about a
	// badge somebody holds. There is one today (session-used) and the reason is
	// in its comment. The directory does not do the live read, which would cost
	// one RPC call per row; the API says which badges are Live so the UI can
	// explain the difference rather than look inconsistent.
	Live bool `json:"live,omitempty"`

	// Of names the badge this one is a bigger version of, and Threshold is how
	// many it takes. Both empty on a standalone badge.
	//
	// The pair exists because "first transaction" and "a thousand
	// transactions" are the same deed at two scales, and a reader who has the
	// first wants to know the next rung is there rather than meeting it as an
	// unrelated badge. Nothing in the rollup reads either field: a tier is an
	// ordinary definition with an ordinary query, and these two are for
	// display and for the test that keeps a ladder's rungs in order.
	Of        string `json:"of,omitempty"`
	Threshold int    `json:"threshold,omitempty"`
}

// signedMessages is every message this index attributes to a signer, as
// (address, block_height, block_time, tx_hash), one row per MESSAGE.
//
// One definition rather than four copies, because three badges read it and a
// fifth table would otherwise have to be added to each of them separately. It
// is rows per message and not per transaction: a transaction carrying three
// calls appears three times, which is right for "have you ever done this" and
// wrong for "how many transactions have you signed". nthSQL is what collapses
// it, and the reason it takes a dedup key.
const signedMessages = `
	SELECT caller AS address, block_height, block_time, tx_hash FROM calls WHERE network = @net AND success = 1
	UNION ALL
	SELECT from_address, block_height, block_time, tx_hash FROM bank_sends WHERE network = @net AND success = 1 AND from_address <> ''
	UNION ALL
	SELECT caller, block_height, block_time, tx_hash FROM msg_runs WHERE network = @net AND success = 1
	UNION ALL
	SELECT creator, block_height, block_time, tx_hash FROM package_submissions WHERE network = @net AND success = 1
`

// nthSQL turns a stream of events into the badge for having done n of them,
// unlocked at the block of the nth.
//
// dedup names the column that makes two rows the same event: "tx_hash" so a
// transaction carrying several messages counts once, "path" so a package
// redeployed nine times counts once. inner has to select that column; it does
// not have to be one of the four the rollup reads.
//
// The window function is what makes the unlock honest. The obvious query is a
// HAVING COUNT(*) >= n with a MIN(block_height) beside it, and it awards the
// badge at the block of the FIRST event, so a thousandth-transaction badge
// would claim to have been earned on the day the account was created. Ordering
// by (block_height, tx_hash) rather than height alone keeps the answer stable
// when n events share a block.
func nthSQL(inner, dedup string, n int) string {
	return fmt.Sprintf(`SELECT address, block_height, block_time, tx_hash FROM (
		SELECT address, block_height, block_time, tx_hash,
		       ROW_NUMBER() OVER (PARTITION BY address ORDER BY block_height, tx_hash) AS rn
		FROM (
			SELECT address, MIN(block_height) AS block_height, block_time, tx_hash
			FROM (%s) GROUP BY address, %s
		)
	) WHERE rn = %d`, inner, dedup, n)
}

// memoBadgeSQL awards a badge to the signer of any transaction whose memo
// matches pred, which is a SQL predicate over the alias `m` (tx_memos).
//
// Written as a builder because the four tool badges differ only in that
// predicate, and a hand-copied four-way union per badge is four chances to
// forget one of the message tables. The join is driven from tx_memos, which is
// both the small side (only transactions carrying a memo are stored) and the
// indexed one (idx_tx_memos_memo), so the predicate narrows first and the
// message tables are then hit by (network, tx_hash).
//
// pred is interpolated, not bound: every caller is a constant in this file, and
// a bound parameter cannot express the LIKE one of them needs alongside the
// three equalities. Nothing here may ever take a predicate from a request.
func memoBadgeSQL(pred string) string {
	return fmt.Sprintf(`SELECT address, MIN(block_height) AS block_height, block_time, tx_hash FROM (
		SELECT c.caller AS address, m.block_height, m.block_time, m.tx_hash
		  FROM tx_memos m JOIN calls c ON c.network = m.network AND c.tx_hash = m.tx_hash
		 WHERE m.network = @net AND (%[1]s) AND c.success = 1
		UNION ALL
		SELECT r.caller, m.block_height, m.block_time, m.tx_hash
		  FROM tx_memos m JOIN msg_runs r ON r.network = m.network AND r.tx_hash = m.tx_hash
		 WHERE m.network = @net AND (%[1]s) AND r.success = 1
		UNION ALL
		SELECT b.from_address, m.block_height, m.block_time, m.tx_hash
		  FROM tx_memos m JOIN bank_sends b ON b.network = m.network AND b.tx_hash = m.tx_hash
		 WHERE m.network = @net AND (%[1]s) AND b.success = 1 AND b.from_address <> ''
		UNION ALL
		SELECT p.creator, m.block_height, m.block_time, m.tx_hash
		  FROM tx_memos m JOIN package_submissions p ON p.network = m.network AND p.tx_hash = m.tx_hash
		 WHERE m.network = @net AND (%[1]s) AND p.success = 1 AND p.creator <> ''
	) GROUP BY address`, pred)
}

// Catalog is every achievement, in display order within its group.
//
// Kept as one slice rather than a map so the order is the file's order: a badge
// grid that reshuffles between loads is unreadable, and Go map iteration would
// do exactly that.
var Catalog = []Def{
	// --- getting started ---------------------------------------------------
	{
		Slug: "first-tx", Name: "First transaction", Emoji: "🌱", Group: GroupStart,
		What: "signed anything at all: a call, a send, a script or a deploy",
		How:  "any transaction counts. `gnokey maketx send` to a friend is the shortest one.",
		SQL:  `SELECT address, MIN(block_height) AS block_height, block_time, tx_hash FROM (` + signedMessages + `) GROUP BY address`,
	},
	{
		Slug: "first-gnot-sent", Name: "Sent GNOT", Emoji: "💸", Group: GroupStart,
		What: "sent native ugnot to another address with a BankMsgSend",
		How:  "`gnokey maketx send -to <address> -send 1000000ugnot -simulate only <key>` first: it prints the gas this actually costs, because a send that has to create the recipient's account costs several times one that does not. Then run it again with that `-gas-wanted` and drop `-simulate only`.",
		SQL: `SELECT from_address AS address, MIN(block_height) AS block_height, block_time, tx_hash
			FROM bank_sends
			WHERE network = @net AND success = 1 AND COALESCE(ugnot_amount, 0) > 0 AND from_address <> ''
			GROUP BY from_address`,
	},
	{
		Slug: "first-gnot-received", Name: "Received GNOT", Emoji: "📥", Group: GroupStart,
		What: "was on the receiving end of a BankMsgSend carrying ugnot",
		How:  "ask someone to send you some, or use a faucet. This one is not something you do to yourself.",
		// `from_address <> to_address`, because the How line above promises it.
		// Without it a send to your own address awarded the badge, which made
		// the sentence a lie the site was telling with its own authority.
		// called-by-other and imported-by-other already excluded self; these two
		// did not.
		SQL: `SELECT to_address AS address, MIN(block_height) AS block_height, block_time, tx_hash
			FROM bank_sends
			WHERE network = @net AND success = 1 AND COALESCE(ugnot_amount, 0) > 0 AND to_address <> ''
			  AND from_address <> to_address
			GROUP BY to_address`,
	},
	{
		Slug: "first-call", Name: "Called a realm", Emoji: "📞", Group: GroupStart,
		What: "ran an exported function on a realm with MsgCall",
		How:  "`gnokey maketx call -pkgpath gno.land/r/demo/userbook -func SignUp -simulate only <key>`, read the `suggested gas-wanted` it prints, then run it for real with that number. A call's gas is whatever the realm's code does, so it is measured rather than known.",
		SQL: `SELECT caller AS address, MIN(block_height) AS block_height, block_time, tx_hash
			FROM calls WHERE network = @net AND success = 1 GROUP BY caller`,
	},
	{
		Slug: "first-run", Name: "Ran a script", Emoji: "📜", Group: GroupStart,
		What: "executed gno source directly on chain with MsgRun",
		How:  "write a `main()` that imports the realms you want, then `gnokey maketx run <key> script.gno`. One transaction, several realms, no deploy. Size it with `-simulate only` first: a script's gas is every realm it touches.",
		SQL: `SELECT caller AS address, MIN(block_height) AS block_height, block_time, tx_hash
			FROM msg_runs WHERE network = @net AND success = 1 GROUP BY caller`,
	},

	// --- going the distance ------------------------------------------------
	//
	// The same deeds as above, counted. A first transaction says somebody
	// arrived; a thousand says they stayed, and the two are worth telling
	// apart. Each rung names the rung below it in Of, so a page can draw a
	// ladder instead of four unrelated badges, and each unlocks at the block of
	// the Nth event rather than the first: a badge that claims to have been
	// earned at the block where the *first* transaction landed is a badge
	// lying about when.
	{
		Slug: "tx-10", Name: "Ten transactions", Emoji: "🔟", Group: GroupVolume,
		Of: "first-tx", Threshold: 10,
		What: "signed ten transactions, of any kind",
		How:  "keep going. Ten is roughly one session of actually trying things.",
		SQL:  nthSQL(signedMessages, "tx_hash", 10),
	},
	{
		Slug: "tx-100", Name: "A hundred transactions", Emoji: "💯", Group: GroupVolume,
		Of: "tx-10", Threshold: 100,
		What: "signed a hundred transactions, of any kind",
		How:  "a hundred is where a script starts paying for itself. `gnokey maketx run` batches a session's worth of calls into one signature.",
		SQL:  nthSQL(signedMessages, "tx_hash", 100),
	},
	{
		Slug: "tx-1000", Name: "A thousand transactions", Emoji: "🏆", Group: GroupVolume,
		Of: "tx-100", Threshold: 1000,
		What: "signed a thousand transactions, of any kind",
		How:  "nobody types a thousand of these. Delegate a scoped session key and let something else sign them for you.",
		SQL:  nthSQL(signedMessages, "tx_hash", 1000),
	},
	{
		Slug: "package-10", Name: "Ten packages", Emoji: "🧱", Group: GroupVolume,
		Of: "first-package", Threshold: 10,
		What: "published ten distinct paths, counting each path once however often it was redeployed",
		How:  "split what you are building into packages small enough to be worth importing on their own.",
		SQL: nthSQL(`SELECT creator AS address, block_height, block_time, tx_hash, path
			FROM package_submissions
			WHERE network = @net AND success = 1 AND creator <> ''`, "path", 10),
	},

	// --- building ----------------------------------------------------------
	{
		Slug: "first-package", Name: "Published a package", Emoji: "📦", Group: GroupBuild,
		What: "deployed a pure package (a /p/ path: library code, no state)",
		How:  "`gnokey maketx addpkg -pkgpath gno.land/p/<your-namespace>/<name> -pkgdir . <key>`. Register the namespace first, and measure with `-simulate only`: a deploy is priced on the bytes it stores.",
		SQL: `SELECT creator AS address, MIN(block_height) AS block_height, block_time, tx_hash
			FROM package_submissions
			WHERE network = @net AND success = 1 AND is_realm = 0 AND creator <> ''
			GROUP BY creator`,
	},
	{
		Slug: "first-realm", Name: "Published a realm", Emoji: "🏛", Group: GroupBuild,
		What: "deployed a realm (an /r/ path: keeps state between calls, renders a page)",
		How:  "same addpkg, a /r/ path, and an exported `Render(path string) string` so it has a page on gnoweb.",
		SQL: `SELECT creator AS address, MIN(block_height) AS block_height, block_time, tx_hash
			FROM package_submissions
			WHERE network = @net AND success = 1 AND is_realm = 1 AND creator <> ''
			GROUP BY creator`,
	},
	{
		Slug: "home-realm", Name: "Made a home realm", Emoji: "🏠", Group: GroupBuild,
		What: "deployed a realm at <namespace>/home, the page gno.land treats as yours",
		How:  "deploy anything renderable to `gno.land/r/<your-namespace>/home`. It is the closest thing the chain has to a personal site.",
		SQL: `SELECT creator AS address, MIN(block_height) AS block_height, block_time, tx_hash
			FROM package_submissions
			WHERE network = @net AND success = 1 AND is_realm = 1 AND creator <> '' AND path LIKE 'gno.land/r/%/home'
			GROUP BY creator`,
	},
	{
		Slug: "first-import", Name: "Imported someone's code", Emoji: "🔗", Group: GroupBuild,
		What: "published a package that imports a gno.land path you did not deploy",
		How:  "`import \"gno.land/p/demo/avl\"` and use its Tree instead of a map. Composability is the reason the chain stores source.",
		SQL: `SELECT p.creator AS address, MIN(p.block_height) AS block_height, p.block_time, p.tx_hash
			FROM packages p
			JOIN dependencies d ON d.network = p.network AND d.package_path = p.path
			WHERE p.network = @net AND p.creator <> '' AND d.import_path LIKE 'gno.land/%'
			  AND NOT EXISTS (
			    SELECT 1 FROM packages mine
			    WHERE mine.network = p.network AND mine.path = d.import_path AND mine.creator = p.creator)
			GROUP BY p.creator`,
	},
	{
		Slug: "imported-by-other", Name: "Someone imported you", Emoji: "🌟", Group: GroupBuild,
		What: "a package you deployed is imported by a package somebody else deployed",
		How:  "not something you can do alone. Publish something small and genuinely reusable under /p/, and document it.",
		SQL: `SELECT mine.creator AS address, MIN(other.block_height) AS block_height, other.block_time, other.tx_hash
			FROM packages mine
			JOIN dependencies d ON d.network = mine.network AND d.import_path = mine.path
			JOIN packages other ON other.network = mine.network AND other.path = d.package_path
			WHERE mine.network = @net AND mine.creator <> '' AND other.creator <> mine.creator
			GROUP BY mine.creator`,
	},
	{
		Slug: "own-realm-call", Name: "Used your own realm", Emoji: "🪞", Group: GroupBuild,
		What: "called a function on a realm you deployed yourself",
		How:  "deploy a realm, then call it. Being your own first user is how you find out the signature is wrong.",
		SQL: `SELECT c.caller AS address, MIN(c.block_height) AS block_height, c.block_time, c.tx_hash
			FROM calls c
			JOIN packages p ON p.network = c.network AND p.path = c.pkg_path
			WHERE c.network = @net AND c.success = 1 AND p.creator = c.caller
			GROUP BY c.caller`,
	},
	{
		Slug: "called-by-other", Name: "Someone used your realm", Emoji: "👥", Group: GroupBuild,
		What: "somebody other than you called a realm you deployed",
		How:  "also not something you can do alone, and the one that actually means the realm works for a stranger.",
		SQL: `SELECT p.creator AS address, MIN(c.block_height) AS block_height, c.block_time, c.tx_hash
			FROM calls c
			JOIN packages p ON p.network = c.network AND p.path = c.pkg_path
			WHERE c.network = @net AND c.success = 1 AND p.creator <> '' AND p.creator <> c.caller
			GROUP BY p.creator`,
	},

	{
		Slug: "redeploy", Name: "Redeployed a package", Emoji: "🔁", Group: GroupBuild,
		What: "published to a path you had already published to, so the second submission replaced the first",
		How:  "deploy a realm with `private = true` in its gnomod.toml and the path stays yours to overwrite. It is the only way to fix a realm in place, and it wipes the realm's state, so read what you are about to lose first.",
		// ⚠️ This counts submissions to a path, which is honest but wider than
		// "edited and shipped again". Under the inert code-submission policy a
		// parked package answers nothing to vm/qfile, so a deploy script that
		// verifies by querying the path concludes it failed and submits the
		// identical bytes again (see package_submissions in store/schema.go).
		// Those resubmissions are real second submissions and are counted as
		// such; the What line says "published to a path you had already
		// published to" rather than "changed a package" for exactly that
		// reason.
		SQL: `SELECT address, MIN(block_height) AS block_height, block_time, tx_hash FROM (
			SELECT creator AS address, block_height, block_time, tx_hash,
			       ROW_NUMBER() OVER (PARTITION BY creator, path ORDER BY block_height, tx_hash) AS n
			FROM package_submissions
			WHERE network = @net AND success = 1 AND creator <> ''
		) WHERE n = 2 GROUP BY address`,
	},
	{
		Slug: "version-bump", Name: "Shipped a v2", Emoji: "⬆️", Group: GroupBuild,
		What: "published <path>/vN after having published the same path at a lower version",
		How:  "a published package is immutable unless it is private, so a fix ships as a new path: `gno.land/p/<ns>/<name>/v2` beside the v1 that other people already import. Leave the old one up; somebody depends on it.",
		// The base is the path with its trailing digits trimmed, which for a
		// versioned path leaves ".../v" and for every other path leaves
		// something that does not end in "/v" and is excluded by the LIKE.
		// rtrim with a character set is the closest SQLite has to a suffix
		// regex, and it is exact here because a version segment is digits to
		// the end of the string.
		SQL: `SELECT later.creator AS address, MIN(later.block_height) AS block_height, later.block_time, later.tx_hash
			FROM package_submissions later
			JOIN package_submissions earlier
			  ON earlier.network = later.network AND earlier.creator = later.creator
			 AND rtrim(earlier.path, '0123456789') = rtrim(later.path, '0123456789')
			 AND CAST(substr(earlier.path, length(rtrim(earlier.path, '0123456789')) + 1) AS INTEGER)
			   < CAST(substr(later.path, length(rtrim(later.path, '0123456789')) + 1) AS INTEGER)
			WHERE later.network = @net AND later.success = 1 AND earlier.success = 1
			  AND later.creator <> '' AND rtrim(later.path, '0123456789') LIKE '%/v'
			GROUP BY later.creator`,
	},

	// --- identity ----------------------------------------------------------
	{
		Slug: "username", Name: "Registered a username", Emoji: "🪪", Group: GroupIdentity,
		What: "holds a name in the chain's user registry, r/sys/users",
		How:  "register through `gno.land/r/gnoland/users/v1`. The name becomes your namespace, so /r/<name>/… is yours to deploy under.",
		SQL: `SELECT address, MIN(block_height) AS block_height, block_time, tx_hash
			FROM users WHERE network = @net AND deleted = 0 AND address <> '' GROUP BY address`,
	},
	{
		Slug: "profile", Name: "Customized a profile", Emoji: "✨", Group: GroupIdentity,
		What: "set a field on a profile realm (a Set… call on a …/profile path)",
		How:  "`gnokey maketx call -pkgpath gno.land/r/demo/profile -func SetStringField -args DisplayName -args \"your name\" …`. Avatar and bio are the same call with a different field.",
		SQL: `SELECT caller AS address, MIN(block_height) AS block_height, block_time, tx_hash
			FROM calls
			WHERE network = @net AND success = 1 AND pkg_path LIKE '%/profile' AND func_name LIKE 'Set%'
			GROUP BY caller`,
	},

	// --- money -------------------------------------------------------------
	{
		Slug: "wrap-wugnot", Name: "Wrapped GNOT", Emoji: "🎁", Group: GroupMoney,
		What: "called Deposit on a wugnot realm, turning native coin into a GRC20 balance",
		How:  "`gnokey maketx call -pkgpath gno.land/r/gnoland/wugnot -func Deposit -send 1000000ugnot …`. Wrapped ugnot is what a GRC20-only contract can accept.",
		SQL: `SELECT caller AS address, MIN(block_height) AS block_height, block_time, tx_hash
			FROM calls
			WHERE network = @net AND success = 1 AND pkg_path LIKE '%/wugnot' AND func_name = 'Deposit'
			GROUP BY caller`,
	},
	{
		Slug: "unwrap-wugnot", Name: "Unwrapped GNOT", Emoji: "🔓", Group: GroupMoney,
		What: "called Withdraw on a wugnot realm, turning the GRC20 balance back into coin",
		How:  "`… -func Withdraw -args <amount>`. Worth doing once, so you know your coin is not stuck in there.",
		SQL: `SELECT caller AS address, MIN(block_height) AS block_height, block_time, tx_hash
			FROM calls
			WHERE network = @net AND success = 1 AND pkg_path LIKE '%/wugnot' AND func_name = 'Withdraw'
			GROUP BY caller`,
	},
	{
		Slug: "grc20-sent", Name: "Sent a token", Emoji: "🪙", Group: GroupMoney,
		What: "a GRC20 Transfer event moved tokens out of this address",
		How:  "`gnokey maketx call -pkgpath <token realm> -func Transfer -args <to> -args <amount> …`. Different from a coin send: tokens live in a realm's ledger, not the bank.",
		SQL: `SELECT from_addr AS address, MIN(block_height) AS block_height, block_time, tx_hash
			FROM token_transfers WHERE network = @net AND from_addr <> '' AND value > 0 GROUP BY from_addr`,
	},
	{
		Slug: "grc20-received", Name: "Received a token", Emoji: "💰", Group: GroupMoney,
		What: "a GRC20 Transfer event moved tokens into this address",
		How:  "hold any GRC20. Wrapping ugnot is the shortest path and earns two badges at once.",
		// Self-excluded for the same reason as first-gnot-received. A mint has an
		// empty `from`, so it is not caught by this and still counts, which is
		// right: a token arriving from nowhere is not one you sent yourself.
		SQL: `SELECT to_addr AS address, MIN(block_height) AS block_height, block_time, tx_hash
			FROM token_transfers
			WHERE network = @net AND to_addr <> '' AND value > 0 AND from_addr <> to_addr
			GROUP BY to_addr`,
	},
	{
		Slug: "token-issuer", Name: "Issued a token", Emoji: "🏭", Group: GroupMoney,
		What: "a GRC20 token from a package you deployed changed hands",
		How:  "deploy a realm that instantiates `grc20.NewToken`, then mint some. The badge lands the first time the token actually moves.",
		SQL: `SELECT p.creator AS address, MIN(t.block_height) AS block_height, t.block_time, t.tx_hash
			FROM token_transfers t
			JOIN packages p ON p.network = t.network AND p.path = t.pkg_path
			WHERE t.network = @net AND p.creator <> '' GROUP BY p.creator`,
	},

	// --- keys and sessions -------------------------------------------------
	{
		Slug: "session-created", Name: "Created a session key", Emoji: "🔑", Group: GroupKeys,
		What: "granted a delegated signing key with auth/create_session",
		How:  "mint a key, then `gnokey maketx auth create_session` with `-allow-path` and a spend limit. The key signs for you, scoped, without ever holding your mnemonic.",
		SQL: `SELECT master AS address, MIN(granted_height) AS block_height, granted_time AS block_time, granted_tx AS tx_hash
			FROM session_grants WHERE network = @net AND master <> '' GROUP BY master`,
	},
	{
		Slug: "session-used", Name: "Signed with a session key", Emoji: "🖋", Group: GroupKeys, Live: true,
		What: "a session key granted by this account has signed at least one transaction",
		How:  "use the delegated key instead of your master key for the realm you scoped it to. That is the whole point of granting one.",
		// Awarded to the MASTER, which is the only account a reader can act on.
		// A session signs *as its master*: calls, msg_runs and bank_sends all
		// record the master's address and the session address appears in none
		// of them, so the link exists in exactly one place, the
		// signature.session_addr the sweep writes into session_txs.
		//
		// Live as well as indexed, and the two cannot disagree. session_txs is
		// filled only when the indexer that served the sweep models signatures,
		// and one of the two mainnet indexers does not (gnoscope#433), so this
		// query under-reports on a chain swept by the wrong one. The address
		// page therefore also reads auth/accounts/<master>/sessions and ORs in
		// a grant whose Sequence has moved. The live read can only ever add a
		// badge, never take one away, which is what makes both safe.
		//
		// The live read alone was not enough, and that is why this query
		// exists: it sees only grants that still exist, so a key that was used
		// and then revoked left no evidence anywhere. session_txs outlives the
		// grant.
		SQL: `SELECT g.master AS address, MIN(t.block_height) AS block_height,
			       COALESCE(x.block_time, '') AS block_time, t.tx_hash
			FROM session_txs t
			JOIN session_grants g ON g.network = t.network AND g.session_addr = t.session_addr
			LEFT JOIN transactions x ON x.network = t.network AND x.tx_hash = t.tx_hash
			WHERE t.network = @net AND g.master <> ''
			GROUP BY g.master`,
	},
	{
		Slug: "session-revoked", Name: "Revoked a session key", Emoji: "♻️", Group: GroupKeys,
		What: "ended a grant with auth/revoke_session or auth/revoke_all_sessions",
		How:  "`gnokey maketx auth revoke_session -session-key <address>`. Granting is half the skill; being able to take it back is the other half.",
		SQL: `SELECT master AS address, MIN(revoked_height) AS block_height, revoked_time AS block_time, revoked_tx AS tx_hash
			FROM session_grants WHERE network = @net AND master <> '' AND revoked_height IS NOT NULL GROUP BY master`,
	},

	// --- tools of the trade ------------------------------------------------
	//
	// The transaction memo is the only place the chain records which *client*
	// composed a transaction. Nothing else survives: the signature says who,
	// the messages say what, and the tool that assembled them is gone by the
	// time a block has it. Four tools stamp one, measured over 46,445 mainnet
	// transactions sampled 2026-09-29 at five points across the chain.
	//
	// ⚠️ A memo is free text the signer chooses, so every badge here is
	// evidence of a claim rather than proof of one: anyone can type
	// "gnopublish" into a memo and earn the badge without ever running it.
	// That is worth having anyway, because the honest reading of the badge is
	// "this transaction says it came from X", which is exactly what the What
	// lines say. Nothing downstream should treat it as attestation.
	//
	// Adena is the obvious absence, and it was looked for: not one of the
	// 46,445 sampled transactions carries a memo naming it, so the wallet
	// stamps nothing and there is no honest query to write. The same is true of
	// gnoweb and of gnokey itself. If Adena ever starts stamping one, this is
	// the group the badge belongs in.
	{
		Slug: "tool-gnoswap", Name: "Traded on gnoswap.io", Emoji: "🔀", Group: GroupTools,
		What: "signed a transaction stamped `Executed through gnoswap.io`, the memo the GnoSwap web app writes",
		How:  "swap something on gnoswap.io. The app stamps the memo for you; the DEX itself is a set of realms you can also call directly.",
		SQL:  memoBadgeSQL(`m.memo = 'Executed through gnoswap.io'`),
	},
	{
		Slug: "tool-gnopublish", Name: "Deployed with gnopublish", Emoji: "🚀", Group: GroupTools,
		What: "signed a transaction stamped `gnopublish`",
		How:  "gnopublish wraps addpkg so publishing a package is one command rather than a path, a directory and a gas figure you had to measure first.",
		SQL:  memoBadgeSQL(`m.memo = 'gnopublish'`),
	},
	{
		Slug: "tool-gnoblog", Name: "Posted with gnoblog-cli", Emoji: "✍️", Group: GroupTools,
		What: "signed a transaction stamped `Posted from gnoblog-cli`",
		How:  "gnoblog-cli publishes a markdown file to a blog realm as one call, front matter and all, instead of hand-escaping a post into an argument.",
		SQL:  memoBadgeSQL(`m.memo = 'Posted from gnoblog-cli'`),
	},
	{
		Slug: "tool-gnomi", Name: "Used Gnomi", Emoji: "🎰", Group: GroupTools,
		What: "signed a transaction whose memo starts with `gnomi`, which covers both stamps the app has used (`Gnomi.fun` and `gnomi`)",
		How:  "gnomi.fun is a token launchpad on gno.land. Buying or selling through it stamps the memo.",
		// A prefix rather than the two exact strings, because the app has
		// changed its stamp once already ("Gnomi.fun" and "gnomi" both appear
		// in the same sample) and a third spelling would silently stop
		// awarding this. LIKE is case-insensitive for ASCII in SQLite, which is
		// what makes one pattern cover both.
		SQL: memoBadgeSQL(`m.memo LIKE 'gnomi%'`),
	},

	// --- governance --------------------------------------------------------
	{
		Slug: "govdao-vote", Name: "Voted in GovDAO", Emoji: "🗳", Group: GroupGovern,
		What: "cast a vote on a proposal in r/gov/dao",
		How:  "GovDAO membership comes first, and it is granted by proposal. Members vote with `MustVoteOnProposalSimple`.",
		SQL: `SELECT caller AS address, MIN(block_height) AS block_height, block_time, tx_hash
			FROM calls
			WHERE network = @net AND success = 1 AND pkg_path LIKE '%/r/gov/dao' AND func_name LIKE '%Vote%'
			GROUP BY caller`,
	},
	{
		Slug: "govdao-execute", Name: "Executed a proposal", Emoji: "⚙️", Group: GroupGovern,
		What: "called ExecuteProposal on r/gov/dao, applying a passed proposal",
		How:  "anyone may execute a proposal that has passed. It is the step people forget, and nothing happens until somebody pays for it.",
		SQL: `SELECT caller AS address, MIN(block_height) AS block_height, block_time, tx_hash
			FROM calls
			WHERE network = @net AND success = 1 AND pkg_path LIKE '%/r/gov/dao' AND func_name = 'ExecuteProposal'
			GROUP BY caller`,
	},
	{
		Slug: "validator", Name: "Registered a validator", Emoji: "🛡", Group: GroupGovern,
		What: "appears in the valopers registry as a registered validator",
		How:  "run a node, then register it in `gno.land/r/gnops/valopers`. Registration is public and being in the valset is a separate GovDAO decision. ⚠️ `Register` rejects a key that is **already** an active validator (`ErrFrontrunValidator`), so an operator in the set cannot register a profile for that key.",
		SQL: `SELECT address, MIN(block_height) AS block_height, block_time, tx_hash
			FROM valoper_registrations
			WHERE network = @net AND success = 1 AND address <> '' GROUP BY address`,
	},
}

// bySlug indexes the catalog once, at init, so lookups are not a linear scan
// through a slice that only grows.
var bySlug = func() map[string]*Def {
	m := make(map[string]*Def, len(Catalog))
	for i := range Catalog {
		m[Catalog[i].Slug] = &Catalog[i]
	}
	return m
}()

// Lookup returns the definition for a slug, or nil.
func Lookup(slug string) *Def { return bySlug[slug] }

// Indexed returns the definitions the rollup can compute, which is every one
// that carries SQL. The rollup iterates this rather than Catalog so a live-only
// badge cannot silently become "nobody has it".
func Indexed() []Def {
	out := make([]Def, 0, len(Catalog))
	for _, d := range Catalog {
		if d.SQL != "" {
			out = append(out, d)
		}
	}
	return out
}
