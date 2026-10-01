package store

import (
	"fmt"
	"sort"
	"testing"

	"github.com/gnoverse/gnoscope/pkg/achievements"
	"github.com/gnoverse/gnoscope/pkg/config"
)

// The catalog is twenty-odd hand-written queries against nine tables, and a
// query that references a column that does not exist fails at *run* time, on a
// timer, into a log line nobody reads — after which the badges simply never
// appear and nothing says why. So the first test here runs every definition
// against the real schema, and the second checks that each one awards the badge
// to the address it is supposed to and to nobody else.
//
// Verified the way AGENTS.md asks for: each definition below was checked to go
// red when the fact it depends on is removed from the fixture, not merely to be
// green with it there.

// seedAchievementWorld writes one small, deliberately asymmetric chain:
//
//	alice   deploys a /p/ package and a /r/ realm and a home realm, imports
//	        someone else's code, calls her own realm, gets called by bob,
//	        registers a name, wraps and unwraps, grants and revokes a session.
//	bob     only ever calls alice's realm and receives coin. He is the control:
//	        every builder badge must miss him.
//	carol   is a delegated key of alice's, and does nothing itself.
func seedAchievementWorld(t *testing.T, db *DB) {
	t.Helper()
	const net = "mainnet"
	db.SetConfiguredNetworks([]config.NetworkConfig{{ID: net}})

	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}

	// alice's packages. The /p/ one imports a path she does not own, which is
	// what first-import is about; bob's realm imports hers, which is what
	// imported-by-other is about.
	must("pkg", db.UpsertPackage(net, "gno.land/p/alice/util", "util", "g1alice", "TX1", 10, "2026-01-01T00:00:00Z", false, 1))
	must("sub", db.InsertPackageSubmission(net, "TX1", 0, "gno.land/p/alice/util", "util", "g1alice", 10, "2026-01-01T00:00:00Z", false, 1, "", true))
	must("deps", db.SetDependencies(net, "gno.land/p/alice/util", []string{"gno.land/p/demo/avl"}))

	must("pkg", db.UpsertPackage(net, "gno.land/r/alice/shop", "shop", "g1alice", "TX2", 11, "2026-01-02T00:00:00Z", true, 1))
	must("sub", db.InsertPackageSubmission(net, "TX2", 0, "gno.land/r/alice/shop", "shop", "g1alice", 11, "2026-01-02T00:00:00Z", true, 1, "", true))

	must("pkg", db.UpsertPackage(net, "gno.land/r/alice/home", "home", "g1alice", "TX3", 12, "2026-01-03T00:00:00Z", true, 1))
	must("sub", db.InsertPackageSubmission(net, "TX3", 0, "gno.land/r/alice/home", "home", "g1alice", 12, "2026-01-03T00:00:00Z", true, 1, "", true))

	// bob's realm, which imports alice's package.
	must("pkg", db.UpsertPackage(net, "gno.land/r/bob/app", "app", "g1bob", "TX4", 13, "2026-01-04T00:00:00Z", true, 1))
	must("deps", db.SetDependencies(net, "gno.land/r/bob/app", []string{"gno.land/p/alice/util"}))

	// dave imports only himself, and is imported only by himself. He is the
	// control for the two dependency badges specifically: without him both
	// queries pass with their "and it is not your own code" clause deleted,
	// because nobody else in this world imports themselves. Packages only, no
	// submission rows, so he does not move any other badge's expected set.
	must("pkg", db.UpsertPackage(net, "gno.land/p/dave/lib", "lib", "g1dave", "TX19", 14, "2026-01-19T00:00:00Z", false, 1))
	must("pkg", db.UpsertPackage(net, "gno.land/r/dave/app", "app", "g1dave", "TX20", 15, "2026-01-20T00:00:00Z", true, 1))
	must("deps", db.SetDependencies(net, "gno.land/r/dave/app", []string{"gno.land/p/dave/lib"}))

	// Calls. alice calls her own shop; bob calls it too.
	must("call", db.InsertCall(net, "TX5", 20, 0, "2026-01-05T00:00:00Z", "g1alice", "gno.land/r/alice/shop", "Buy", "", "", true))
	must("call", db.InsertCall(net, "TX6", 21, 0, "2026-01-06T00:00:00Z", "g1bob", "gno.land/r/alice/shop", "Buy", "", "", true))

	// wugnot, profile, govdao.
	must("call", db.InsertCall(net, "TX7", 22, 0, "2026-01-07T00:00:00Z", "g1alice", "gno.land/r/gnoland/wugnot", "Deposit", "", "", true))
	must("call", db.InsertCall(net, "TX8", 23, 0, "2026-01-08T00:00:00Z", "g1alice", "gno.land/r/gnoland/wugnot", "Withdraw", "", "", true))
	must("call", db.InsertCall(net, "TX9", 24, 0, "2026-01-09T00:00:00Z", "g1alice", "gno.land/r/demo/profile", "SetStringField", "", "", true))
	must("call", db.InsertCall(net, "TX10", 25, 0, "2026-01-10T00:00:00Z", "g1alice", "gno.land/r/gov/dao", "MustVoteOnProposalSimple", "", "", true))
	must("call", db.InsertCall(net, "TX11", 26, 0, "2026-01-11T00:00:00Z", "g1alice", "gno.land/r/gov/dao", "ExecuteProposal", "", "", true))

	// A run, a send, a receive.
	must("run", db.InsertMsgRun(net, "TX12", 27, "2026-01-12T00:00:00Z", "g1alice", "package main", "", true))
	must("send", db.InsertBankSend(net, "TX13", 28, "2026-01-13T00:00:00Z", "g1alice", "g1bob", "1000000ugnot", true))

	// Tokens: alice's shop issues one, and it moves from alice to bob.
	must("token", db.InsertTokenTransfer(net, "TX14", 0, TokenTransfer{
		Token: "gno.land/r/alice/shop.shop.0000001", From: "g1alice", To: "g1bob",
		Value: 5, BlockHeight: 29, BlockTime: "2026-01-14T00:00:00Z"}))

	// Identity.
	must("user", db.UpsertUser(net, User{Name: "alice", Address: "g1alice", TxHash: "TX15", BlockHeight: 30, BlockTime: "2026-01-15T00:00:00Z"}))

	// Sessions: alice grants carol a key, then revokes it.
	must("grant", db.UpsertSessionGrant(SessionGrant{Network: net, SessionAddr: "g1carol", Master: "g1alice",
		AllowPaths: []string{"vm/exec:gno.land/r/alice/shop"}, GrantedHeight: 31, GrantedTime: "2026-01-16T00:00:00Z", GrantedTx: "TX16"}))
	must("revoke", db.RevokeSessionGrant(net, "g1carol", 32, "2026-01-17T00:00:00Z", "TX17"))

	// carol actually signs something with the key she was granted. This is what
	// separates session-used from session-created, and it is recorded nowhere
	// else: the call it signed names ALICE as its caller, because a session
	// signs as its master.
	must("call", db.InsertCall(net, "TX21", 34, 0, "2026-01-21T00:00:00Z", "g1alice", "gno.land/r/alice/shop", "Buy", "", "", true))
	must("session tx", db.RecordSessionTx(net, "TX21", "g1carol", 34))

	// Tool memos. All on bob's calls, so no other badge's expected set moves,
	// plus one on a send of alice's: that leg of the union is a different
	// table, and the LIKE that covers gnomi's two spellings has to match the
	// second one too. TX23 carries a stamp that no longer has a badge, so a
	// memo badge that matched too loosely would show up here.
	for i, m := range []struct{ hash, memo, fn string }{
		{"TX22", "Executed through gnoswap.io", "Swap"},
		{"TX23", "gnopublish", "Deploy"},
		{"TX25", "Gnomi.fun", "Buy"},
	} {
		h := 35 + i
		must("call", db.InsertCall(net, m.hash, h, 0, "2026-01-22T00:00:00Z", "g1bob", "gno.land/r/alice/shop", m.fn, "", "", true))
		must("memo", db.UpsertTxMemos(net, []TxMemoRow{{Hash: m.hash, Memo: m.memo, BlockHeight: h, BlockTime: "2026-01-22T00:00:00Z"}}))
	}
	must("send", db.InsertBankSend(net, "TX27", 40, "2026-01-23T00:00:00Z", "g1alice", "g1bob", "1ugnot", true))
	must("memo", db.UpsertTxMemos(net, []TxMemoRow{{Hash: "TX27", Memo: "gnomi", BlockHeight: 40, BlockTime: "2026-01-23T00:00:00Z"}}))

	// Bubble Rumble. bob bids on the fifth generation. alice, who already
	// calls realms so no other badge moves, creates a pool on the real realm
	// (the host's call, not a play) and bids on a lookalike under her own
	// namespace. Only bob plays.
	const br = "gno.land/r/g1leu8d2vsplhehcfkjg50mwgdpxdkt8tztu95wr/bubblerumble"
	must("call", db.InsertCall(net, "TX28", 41, 0, "2026-01-24T00:00:00Z", "g1bob", br+"5", "Bid", "", "", true))
	must("call", db.InsertCall(net, "TX29", 42, 0, "2026-01-24T00:00:00Z", "g1alice", br+"5", "CreatePool", "", "", true))
	must("call", db.InsertCall(net, "TX30", 43, 0, "2026-01-24T00:00:00Z", "g1alice", "gno.land/r/alice/bubblerumble", "Bid", "", "", true))

	// whale is the volume control: the tiers are the only badges that need an
	// account with more history than a hand-written fixture would otherwise
	// have, and they are exactly the badges that would pass a small fixture
	// with the wrong threshold compiled in.
	//
	// 1,000 calls, one per transaction. Every message is its own tx hash on
	// purpose: nthSQL collapses a transaction carrying several messages to one
	// event, and a fixture of one message per tx cannot tell a query that
	// forgot to that a query that did.
	for i := 0; i < 1000; i++ {
		must("whale call", db.InsertCall(net, fmt.Sprintf("W%04d", i), 100+i, 0, "2026-02-01T00:00:00Z",
			"g1whale", "gno.land/r/alice/shop", "Buy", "", "", true))
	}
	// Ten distinct paths, and one of them submitted twice. The redeploy is what
	// makes package-10 worth its dedup key: counted by submission rather than
	// by path, eleven rows would award the tenth-package badge to somebody who
	// published nine.
	for i := 1; i <= 10; i++ {
		must("whale pkg", db.InsertPackageSubmission(net, fmt.Sprintf("WP%02d", i), 0,
			fmt.Sprintf("gno.land/p/whale/lib%d", i), "lib", "g1whale", 1200+i, "2026-02-02T00:00:00Z", false, 1, "", true))
	}
	must("whale redeploy", db.InsertPackageSubmission(net, "WP01B", 0,
		"gno.land/p/whale/lib1", "lib", "g1whale", 1300, "2026-02-03T00:00:00Z", false, 1, "", true))

	// A v1 and then a v2 of the same path, which is the only way to change a
	// package that is not private. lib10 above ends in a digit and is the
	// control for the suffix trimming: it must not read as a version of lib1.
	must("whale v1", db.InsertPackageSubmission(net, "WV1", 0,
		"gno.land/p/whale/mod/v1", "mod", "g1whale", 1310, "2026-02-04T00:00:00Z", false, 1, "", true))
	must("whale v2", db.InsertPackageSubmission(net, "WV2", 0,
		"gno.land/p/whale/mod/v2", "mod", "g1whale", 1311, "2026-02-05T00:00:00Z", false, 1, "", true))

	// nine is the control for package-10's dedup key: nine paths, each
	// published twice. Counted by submission he clears ten and the badge is
	// wrong; counted by path he is one short, which is the answer.
	for i := 1; i <= 9; i++ {
		must("nine pkg", db.InsertPackageSubmission(net, fmt.Sprintf("N%02d", i), 0,
			fmt.Sprintf("gno.land/p/nine/lib%d", i), "lib", "g1nine", 1400+i, "2026-02-06T00:00:00Z", false, 1, "", true))
		must("nine again", db.InsertPackageSubmission(net, fmt.Sprintf("N%02dB", i), 0,
			fmt.Sprintf("gno.land/p/nine/lib%d", i), "lib", "g1nine", 1420+i, "2026-02-07T00:00:00Z", false, 1, "", true))
	}

	// dave is the control for version-bump's suffix trimming. lib1 and lib10
	// share every character up to the digits, so a query that trims digits and
	// compares numbers awards him a version bump he never shipped. The guard is
	// that a real version segment ends in "/v".
	//
	// He is also the control for the tool badges, and that is why his memo is
	// a sentence rather than a stamp: every other memo in this world names a
	// tool, so without one that names none, each tool query passes with its
	// predicate deleted. It sits on a package submission, which is also the
	// only leg of memoBadgeSQL's union nothing else exercises.
	must("dave lib1", db.InsertPackageSubmission(net, "D01", 0,
		"gno.land/p/dave/lib1", "lib", "g1dave", 1500, "2026-02-08T00:00:00Z", false, 1, "", true))
	must("memo", db.UpsertTxMemos(net, []TxMemoRow{{Hash: "D01", Memo: "just a note", BlockHeight: 1500, BlockTime: "2026-02-08T00:00:00Z"}}))
	must("dave lib10", db.InsertPackageSubmission(net, "D10", 0,
		"gno.land/p/dave/lib10", "lib", "g1dave", 1501, "2026-02-09T00:00:00Z", false, 1, "", true))

	// A validator, which is nobody else in this world.
	must("valoper", db.InsertValoperRegistration(net, "TX18", 33, "2026-01-18T00:00:00Z", "g1val", "Register", "g1val", "val-1", true))
}

// holders returns the addresses holding one badge, sorted, so an assertion can
// compare whole sets rather than probe them one at a time. Probing one address
// is how a query that awards a badge to everybody passes.
func holders(t *testing.T, db *DB, slug string) []string {
	t.Helper()
	rows, total, err := db.AchievementHolders("mainnet", slug, 500, 0)
	if err != nil {
		t.Fatalf("AchievementHolders(%s): %v", slug, err)
	}
	if total != len(rows) {
		t.Fatalf("%s: total %d but %d rows, so the count and the page disagree", slug, total, len(rows))
	}
	out := make([]string, 0, len(rows))
	for _, h := range rows {
		out = append(out, h.Address)
	}
	sort.Strings(out)
	return out
}

func equalSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Every definition has to at least execute against the real schema. This is the
// test that catches a renamed column, and it is separate from the behaviour
// table below because a syntax error there would fail twenty assertions at once
// and say nothing about which query is broken.
func TestEveryAchievementQueryRuns(t *testing.T) {
	db := NewTestDB(t)
	db.SetConfiguredNetworks([]config.NetworkConfig{{ID: "mainnet"}})
	if err := db.RefreshAchievements(); err != nil {
		t.Fatalf("RefreshAchievements on an empty database: %v", err)
	}
	if len(achievements.Indexed()) == 0 {
		t.Fatal("no indexed definitions, so this test proves nothing")
	}
}

func TestAchievementsAwardTheRightAddresses(t *testing.T) {
	db := NewTestDB(t)
	seedAchievementWorld(t, db)
	if err := db.RefreshAchievements(); err != nil {
		t.Fatalf("RefreshAchievements: %v", err)
	}

	tests := []struct {
		slug string
		want []string
		why  string
	}{
		{"first-tx", []string{"g1alice", "g1bob", "g1dave", "g1nine", "g1whale"}, "everyone who signed something; carol only ever received a grant, and a session signs as its master"},
		{"first-gnot-sent", []string{"g1alice"}, "only alice sent"},
		{"first-gnot-received", []string{"g1bob"}, "only bob received"},
		{"first-call", []string{"g1alice", "g1bob", "g1whale"}, "everyone but carol called a realm"},
		{"first-run", []string{"g1alice"}, "only alice ran a script"},
		{"first-package", []string{"g1alice", "g1dave", "g1nine", "g1whale"}, "bob deployed a realm, not a package"},
		{"first-realm", []string{"g1alice"}, "bob's realm has no submission row, only a package row"},
		{"home-realm", []string{"g1alice"}, "only alice deployed …/home"},
		{"first-import", []string{"g1alice", "g1bob"}, "alice imports p/demo/avl, bob imports alice's util; dave imports only his own lib"},
		{"imported-by-other", []string{"g1alice"}, "bob imports alice; nobody imports bob, and dave importing dave does not count"},
		{"own-realm-call", []string{"g1alice"}, "alice called her own shop"},
		{"called-by-other", []string{"g1alice"}, "bob called alice's shop"},
		{"username", []string{"g1alice"}, "only alice registered"},
		{"profile", []string{"g1alice"}, "only alice set a profile field"},
		{"wrap-wugnot", []string{"g1alice"}, "only alice deposited"},
		{"unwrap-wugnot", []string{"g1alice"}, "only alice withdrew"},
		{"grc20-sent", []string{"g1alice"}, "the transfer left alice"},
		{"grc20-received", []string{"g1bob"}, "the transfer reached bob"},
		{"token-issuer", []string{"g1alice"}, "the token's realm is alice's"},
		{"session-created", []string{"g1alice"}, "alice is the master"},
		{"session-revoked", []string{"g1alice"}, "alice revoked it"},
		{"session-used", []string{"g1alice"}, "carol signed TX21, and the badge belongs to the master who granted her"},
		{"govdao-vote", []string{"g1alice"}, "only alice voted"},
		{"govdao-execute", []string{"g1alice"}, "only alice executed"},
		{"validator", []string{"g1val"}, "only g1val registered"},

		// The tiers. alice signs 11 transactions in this world, which is what
		// makes tx-10 a real set rather than whale alone: a query that counted
		// messages instead of transactions, or that awarded on "has ever
		// signed", would put bob in here too.
		{"tx-10", []string{"g1alice", "g1nine", "g1whale"}, "alice signs 11 transactions, nine 18, whale 1,012; bob signs 6 and dave 2"},
		{"tx-100", []string{"g1whale"}, "only whale gets past a hundred"},
		{"tx-1000", []string{"g1whale"}, "only whale gets past a thousand"},
		{"package-10", []string{"g1whale"}, "whale published twelve distinct paths; nine published eighteen times across nine paths and is one short"},
		{"redeploy", []string{"g1nine", "g1whale"}, "both published twice to a path they had already published to"},
		{"version-bump", []string{"g1whale"}, "whale shipped mod/v2 after mod/v1; dave's lib10 is not a version of his lib1"},

		// The tool badges, which read a memo rather than a message.
		{"tool-gnoswap", []string{"g1bob"}, "bob's TX22 carries gnoswap's stamp"},
		{"tool-gnomi", []string{"g1alice", "g1bob"}, "bob's call says Gnomi.fun and alice's send says gnomi; one LIKE covers both"},
		{"bubblerumble", []string{"g1bob"}, "bob bid; alice only created a pool and bid on a lookalike path"},
	}

	// Every indexed definition must appear above. A badge added to the catalog
	// with no row here would otherwise ship untested, which on a query written
	// by hand is the same as shipping it wrong.
	covered := map[string]bool{}
	for _, tc := range tests {
		covered[tc.slug] = true
	}
	for _, def := range achievements.Indexed() {
		if !covered[def.Slug] {
			t.Errorf("achievement %q has no case in this table", def.Slug)
		}
	}

	for _, tc := range tests {
		t.Run(tc.slug, func(t *testing.T) {
			got := holders(t, db, tc.slug)
			if !equalSet(got, tc.want) {
				t.Errorf("%s: holders %v, want %v (%s)", tc.slug, got, tc.want, tc.why)
			}
		})
	}
}

// A send to your own address is not somebody sending you coin, and the badge's
// own How line promises as much. The query did not enforce it, so the site was
// contradicting itself with its own authority.
func TestSelfSendDoesNotAwardReceived(t *testing.T) {
	db := NewTestDB(t)
	const net = "mainnet"
	db.SetConfiguredNetworks([]config.NetworkConfig{{ID: net}})

	// alice pays herself; bob is paid by alice. Only bob has received anything.
	if err := db.InsertBankSend(net, "TXSELF", 10, "2026-01-01T00:00:00Z", "g1alice", "g1alice", "1000000ugnot", true); err != nil {
		t.Fatalf("InsertBankSend: %v", err)
	}
	if err := db.InsertBankSend(net, "TXREAL", 11, "2026-01-02T00:00:00Z", "g1alice", "g1bob", "1000000ugnot", true); err != nil {
		t.Fatalf("InsertBankSend: %v", err)
	}
	if err := db.InsertTokenTransfer(net, "TXTOKSELF", 0, TokenTransfer{
		Token: "gno.land/r/demo/tok.TOK.0", From: "g1alice", To: "g1alice",
		Value: 5, BlockHeight: 12, BlockTime: "2026-01-03T00:00:00Z"}); err != nil {
		t.Fatalf("InsertTokenTransfer: %v", err)
	}
	if err := db.RefreshAchievements(); err != nil {
		t.Fatalf("RefreshAchievements: %v", err)
	}

	if got := holders(t, db, "first-gnot-received"); !equalSet(got, []string{"g1bob"}) {
		t.Errorf("first-gnot-received holders %v, want only g1bob: alice paid herself", got)
	}
	if got := holders(t, db, "grc20-received"); len(got) != 0 {
		t.Errorf("grc20-received holders %v, want none: the only transfer was alice to herself", got)
	}
	// The sending half is unaffected: alice really did send.
	if got := holders(t, db, "first-gnot-sent"); !equalSet(got, []string{"g1alice"}) {
		t.Errorf("first-gnot-sent holders %v, want g1alice", got)
	}
}

// The first unlock is the fact a badge carries, and "first" is the part a
// GROUP BY gets wrong quietly: without the MIN it would report whichever row
// the planner happened to reach last.
func TestAchievementRecordsTheFirstOccurrenceNotTheLatest(t *testing.T) {
	db := NewTestDB(t)
	const net = "mainnet"
	db.SetConfiguredNetworks([]config.NetworkConfig{{ID: net}})

	for _, c := range []struct {
		tx     string
		height int
		time   string
	}{
		{"TXLATE", 900, "2026-06-01T00:00:00Z"},
		{"TXFIRST", 100, "2026-01-01T00:00:00Z"},
		{"TXMID", 500, "2026-03-01T00:00:00Z"},
	} {
		if err := db.InsertCall(net, c.tx, c.height, 0, c.time, "g1alice", "gno.land/r/demo/foo", "Bar", "", "", true); err != nil {
			t.Fatalf("InsertCall: %v", err)
		}
	}
	if err := db.RefreshAchievements(); err != nil {
		t.Fatalf("RefreshAchievements: %v", err)
	}

	got, err := db.AddressAchievements(net, "g1alice")
	if err != nil {
		t.Fatalf("AddressAchievements: %v", err)
	}
	var call *Unlock
	for i := range got {
		if got[i].Slug == "first-call" {
			call = &got[i]
		}
	}
	if call == nil {
		t.Fatal("no first-call badge")
	}
	if call.Height != 100 || call.TxHash != "TXFIRST" || call.Time != "2026-01-01T00:00:00Z" {
		t.Errorf("first-call recorded %d/%s/%s, want the earliest: 100/TXFIRST/2026-01-01T00:00:00Z",
			call.Height, call.TxHash, call.Time)
	}
}

// Every table here is network-scoped, and AGENTS.md's first invariant is that a
// query which forgets that silently merges two chains. For achievements the
// merge is worse than a wrong number: it would award a badge on mainnet for
// something an address did on a testnet.
func TestAchievementsStayWithinTheirNetwork(t *testing.T) {
	db := NewTestDB(t)
	db.SetConfiguredNetworks([]config.NetworkConfig{{ID: "mainnet"}, {ID: "testnet"}})

	if err := db.InsertCall("testnet", "TX1", 10, 0, "2026-01-01T00:00:00Z", "g1alice", "gno.land/r/gnoland/wugnot", "Deposit", "", "", true); err != nil {
		t.Fatalf("InsertCall: %v", err)
	}
	if err := db.RefreshAchievements(); err != nil {
		t.Fatalf("RefreshAchievements: %v", err)
	}

	if got, _, err := db.AchievementHolders("mainnet", "wrap-wugnot", 10, 0); err != nil {
		t.Fatalf("AchievementHolders: %v", err)
	} else if len(got) != 0 {
		t.Errorf("mainnet has %d wrap-wugnot holders from a testnet deposit: %+v", len(got), got)
	}
	if got, _, err := db.AchievementHolders("testnet", "wrap-wugnot", 10, 0); err != nil {
		t.Fatalf("AchievementHolders: %v", err)
	} else if len(got) != 1 {
		t.Errorf("testnet has %d wrap-wugnot holders, want 1", len(got))
	}
}

// The rebuild replaces rather than accumulates. Without that, a re-sync or a
// chain reset would leave badges standing for facts the index no longer holds,
// and nothing would ever take them down again.
func TestRefreshAchievementsIsIdempotentAndDropsStaleBadges(t *testing.T) {
	db := NewTestDB(t)
	seedAchievementWorld(t, db)

	if err := db.RefreshAchievements(); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	first, err := db.AddressAchievements("mainnet", "g1alice")
	if err != nil {
		t.Fatalf("AddressAchievements: %v", err)
	}
	if err := db.RefreshAchievements(); err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	second, err := db.AddressAchievements("mainnet", "g1alice")
	if err != nil {
		t.Fatalf("AddressAchievements: %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("alice had %d badges then %d after an identical rebuild", len(first), len(second))
	}

	// Now take the fact away and rebuild. The badge must go with it.
	if _, err := db.db.Exec(`DELETE FROM calls WHERE pkg_path LIKE '%/wugnot'`); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := db.RefreshAchievements(); err != nil {
		t.Fatalf("third refresh: %v", err)
	}
	third, err := db.AddressAchievements("mainnet", "g1alice")
	if err != nil {
		t.Fatalf("AddressAchievements: %v", err)
	}
	for _, u := range third {
		if u.Slug == "wrap-wugnot" {
			t.Error("wrap-wugnot survived the deposit being removed, so the rebuild accumulates instead of replacing")
		}
	}
}

func TestDirectoryPeopleRanksAndFilters(t *testing.T) {
	db := NewTestDB(t)
	seedAchievementWorld(t, db)
	if err := db.RefreshAchievements(); err != nil {
		t.Fatalf("RefreshAchievements: %v", err)
	}

	t.Run("ranked by badge count", func(t *testing.T) {
		people, total, err := db.DirectoryPeople(PeopleQuery{Network: "mainnet", Limit: 10})
		if err != nil {
			t.Fatalf("DirectoryPeople: %v", err)
		}
		if total != len(people) {
			t.Fatalf("total %d, rows %d", total, len(people))
		}
		if len(people) == 0 {
			t.Fatal("nobody in the directory")
		}
		if people[0].Address != "g1alice" {
			t.Errorf("top of the directory is %s with %d badges, want g1alice", people[0].Address, people[0].Count)
		}
		if people[0].Name != "alice" {
			t.Errorf("alice's row carries the name %q, want \"alice\"", people[0].Name)
		}
		for i := 1; i < len(people); i++ {
			if people[i].Count > people[i-1].Count {
				t.Errorf("row %d has more badges than row %d, so the ranking is not sorted", i, i-1)
			}
		}
	})

	t.Run("has is an AND", func(t *testing.T) {
		// bob has first-call but not first-realm; alice has both. Filtering on
		// the pair must leave alice alone, which is the assertion an OR would
		// fail.
		people, _, err := db.DirectoryPeople(PeopleQuery{
			Network: "mainnet", Has: []string{"first-call", "first-realm"}, Limit: 10})
		if err != nil {
			t.Fatalf("DirectoryPeople: %v", err)
		}
		if len(people) != 1 || people[0].Address != "g1alice" {
			t.Errorf("filtering on first-call AND first-realm gave %d rows (%+v), want just g1alice", len(people), people)
		}
	})

	t.Run("named only", func(t *testing.T) {
		people, _, err := db.DirectoryPeople(PeopleQuery{Network: "mainnet", NamedOnly: true, Limit: 10})
		if err != nil {
			t.Fatalf("DirectoryPeople: %v", err)
		}
		if len(people) != 1 || people[0].Address != "g1alice" {
			t.Errorf("named-only gave %+v, want just g1alice", people)
		}
	})

	t.Run("q matches a name and an address prefix", func(t *testing.T) {
		byName, _, err := db.DirectoryPeople(PeopleQuery{Network: "mainnet", Q: "ali", Limit: 10})
		if err != nil {
			t.Fatalf("DirectoryPeople: %v", err)
		}
		if len(byName) != 1 || byName[0].Address != "g1alice" {
			t.Errorf("q=ali gave %+v, want g1alice", byName)
		}
		byAddr, _, err := db.DirectoryPeople(PeopleQuery{Network: "mainnet", Q: "g1bob", Limit: 10})
		if err != nil {
			t.Fatalf("DirectoryPeople: %v", err)
		}
		if len(byAddr) != 1 || byAddr[0].Address != "g1bob" {
			t.Errorf("q=g1bob gave %+v, want g1bob", byAddr)
		}
	})

	t.Run("badges come back with the row", func(t *testing.T) {
		people, _, err := db.DirectoryPeople(PeopleQuery{Network: "mainnet", Has: []string{"home-realm"}, Limit: 10})
		if err != nil {
			t.Fatalf("DirectoryPeople: %v", err)
		}
		if len(people) != 1 {
			t.Fatalf("want one row, got %d", len(people))
		}
		found := false
		for _, b := range people[0].Badges {
			if b == "home-realm" {
				found = true
			}
		}
		if !found {
			t.Errorf("the row matched on home-realm but its badges are %v", people[0].Badges)
		}
		if people[0].Count != len(people[0].Badges) {
			t.Errorf("count %d but %d badges listed", people[0].Count, len(people[0].Badges))
		}
	})
}
