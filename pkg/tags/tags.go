// Package tags labels a package by what its code does: it moves a token, it
// sends coins, it emits events, it draws a page. Several labels at once, each
// one carrying the line of code that earned it.
//
// It is not the /apps category guesser (httpapi/apps_category.go), and it is
// kept apart on purpose. That one picks a single bucket per app and falls
// back to words in the path when the code says nothing; a tag is never a
// guess. Every rule here reads the source: an import, a call to a named
// standard-library function, a declaration. A word in a path is chosen by
// whoever named the package and proves nothing about what it does, so no rule
// may look at one.
//
// The rules are a table (Rules), not code paths, so a rule is added, tested
// and calibrated in one place. Symbol rules run on the parsed source
// (go/parser, as pkg/srctok does: gno is Go syntax), never on a regular
// expression over bodies, so a call mentioned in a comment or a string does
// not count, and a test calling testing.SetOriginSend is not a realm taking
// payments.
//
// Test files never earn a tag except test-only: a test that imports grc20 to
// build a fixture is not a token.
package tags

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
)

// Version names the rule set. It is stored beside every tag and in the sync
// state; bumping it recomputes every package's tags once, on the next start.
// Bump it whenever a rule changes what it would say about any package.
const Version = "2"

// Tag is one label on one package and the evidence for it: the import path or
// the symbol that produced it and the file and line it is on, e.g.
// `imports gno.land/p/nt/grc20/v0 (token.gno:5)`.
type Tag struct {
	Tag string `json:"tag"`
	Why string `json:"why"`
}

// File is one source file of a package.
type File struct {
	Name string
	Body string
}

// Import matches an import path. Path matches it exactly and Prefix matches
// its start, standard library included (`crypto/sha256`, `gno.land/r/`);
// Segment matches any element of a gno.land path that equals it, as written
// or without a generation suffix (see spellings); Suffix matches any
// element's stem ending in it (`commondao`, `udao`). Pure restricts the match
// to gno.land/p/ imports, for rules where only a library says "this is built
// with X".
type Import struct {
	Path    string
	Prefix  string
	Segment string
	Suffix  string
	Pure    bool
}

// Call matches a call to an exported function of an imported package, by the
// package's import path and the function's name: `banker.NewBanker` is
// {Import: "chain/banker", Name: "NewBanker"}, whatever the file names the
// import locally. A Name ending in `*` matches every function starting with
// what precedes it (`NewSysParam*`). NotArg0 skips a call whose first
// argument is that selector or identifier: a read-only banker sends nothing.
type Call struct {
	Import  string
	Name    string
	NotArg0 string
}

// Rule is one row of the table.
type Rule struct {
	Tag string
	// Means is what the rule checks, in a sentence, served by /api/tags and
	// shown beside the evidence. It describes the rule, not the word.
	Means   string
	Imports []Import
	Calls   []Call
	// Defines names top-level functions (no receiver) whose declaration earns
	// the tag; Methods names methods, on any type.
	Defines []string
	Methods []string
	// Strings are substrings of a string literal in the code (never of a
	// comment): `$help&func=` is a link the page draws to a transaction form.
	Strings []string
	// Pattern names a shape of code no import or name can say, checked by
	// its own function: see the Pattern constants.
	Pattern Pattern
	// RealmOnly and PureOnly restrict the rule to one kind of package.
	RealmOnly bool
	PureOnly  bool
}

// Pattern is a shape of code a rule looks for.
type Pattern int

const (
	// NoPattern is the zero value: the rule reads imports, calls and names.
	NoPattern Pattern = iota
	// ImplSwap is the facade of an upgradable realm: an exported top-level
	// function assigns a package-level variable whose type is an interface
	// the package itself declares, so a call replaces the implementation
	// every other function goes through.
	ImplSwap
	// ClockCompare is an ordering comparison (<, >, <=, >=, or a time's
	// Before/After) of the block height or the clock: runtime.ChainHeight,
	// time.Now, time.Since or time.Until, directly or through a variable
	// assigned from one in the same function.
	ClockCompare
)

// The tags.
const (
	Token         = "token"
	NFT           = "nft"
	Payments      = "payments"
	DeFi          = "defi"
	Governance    = "governance"
	Governed      = "governed"
	Voting        = "voting"
	Claims        = "claims"
	Social        = "social"
	Identity      = "identity"
	AccessControl = "access-control"
	Pausable      = "pausable"
	Upgradable    = "upgradable"
	Events        = "events"
	TimeBased     = "time-based"
	Crypto        = "crypto"
	Math          = "math"
	CrossRealm    = "cross-realm"
	Render        = "render"
	UI            = "ui"
	Interactive   = "interactive"
	Routing       = "routing"
	Library       = "library"
	SelfContained = "self-contained"
	TestOnly      = "test-only"
)

// Rules is the table, in display order. Library, self-contained and test-only
// are rows here for their order and their sentence; their logic is not an
// import or a call and lives in Derive and LibraryTag.
//
// Calibrated against gnoland1 on 2026-10-05 and, for the rows added in rule
// set 2, on 2026-10-06; the counts and the false positives each rule was cut
// to avoid are in docs/tags.md.
var Rules = []Rule{
	{Tag: Token, Means: "imports a fungible token standard (grc20) or the token registry",
		Imports: []Import{{Segment: "grc20"}, {Segment: "grc20reg"}}},
	{Tag: NFT, Means: "imports a non-fungible token standard (grc721 or grc1155)",
		Imports: []Import{{Segment: "grc721"}, {Segment: "grc1155"}}},
	{Tag: Payments, Means: "sends or receives coins: creates a banker that can send, or reads the coins sent with a call",
		Calls: []Call{
			{Import: "chain/banker", Name: "NewBanker", NotArg0: "BankerTypeReadonly"},
			{Import: "chain/runtime/unsafe", Name: "OriginSend"},
		}},
	{Tag: DeFi, Means: "imports a gnoswap realm or wrapped GNOT",
		Imports: []Import{{Prefix: "gno.land/r/gnoswap/"}, {Segment: "wugnot"}}},
	// Defining a DAO, not being governed by one. Importing gno.land/r/gov/dao
	// is what any realm with proposal-gated admin calls does: r/sys/namereg
	// does it, and namereg is governed, not governance (the lesson
	// apps_category.go records). Importing the GovDAO's own packages is not
	// enough either: r/sys/names reads the member store to check a deployer,
	// and is still only governed. So: built on a DAO library, or implementing
	// the GovDAO's DAO interface, or installing an implementation of it.
	{Tag: Governance, Means: "is built on a DAO library, implements the GovDAO's DAO interface, or installs an implementation of it",
		Imports: []Import{{Suffix: "dao", Pure: true}},
		Methods: []string{"PreExecuteProposal"},
		Calls:   []Call{{Import: "gno.land/r/gov/dao", Name: "UpdateImpl"}},
		Defines: []string{"UpdateImpl"}},
	// The other half of the namereg lesson: a realm whose admin actions go
	// through a GovDAO vote builds the proposal requests itself, so the call
	// that builds one is the evidence. Reading dao.ProposalID is not.
	{Tag: Governed, Means: "builds GovDAO proposals (r/gov/dao or r/sys/params request constructors), so its changes pass a vote",
		Calls: []Call{
			{Import: "gno.land/r/gov/dao", Name: "NewProposalRequest*"},
			{Import: "gno.land/r/gov/dao", Name: "NewSimpleExecutor"},
			{Import: "gno.land/r/gov/dao", Name: "MustCreateProposal"},
			{Import: "gno.land/r/sys/params", Name: "NewSysParam*"},
			{Import: "gno.land/r/sys/params", Name: "NewSet*"},
			{Import: "gno.land/r/sys/params", Name: "Propose*"},
		}},
	{Tag: Voting, Means: "lets callers vote: a realm defining Vote, CastVote or VoteOnProposal",
		Defines: []string{"Vote", "CastVote", "VoteOnProposal"}, RealmOnly: true},
	// Not every Claim* function: a fact-staking realm's ClaimTitle and
	// ClaimStatus read a claim, they pay nothing out.
	{Tag: Claims, Means: "lets callers claim what they are owed: a realm defining Claim, ClaimAll, ClaimReward(s), ClaimRefund or ClaimFees",
		Defines: []string{"Claim", "ClaimAll", "ClaimReward", "ClaimRewards", "ClaimRefund", "ClaimFees"}, RealmOnly: true},
	// Not r/sys/users: resolving a @name is something any realm does. Not
	// r/demo/profile either: its one importer on gnoland1 is r/sys/namereg,
	// which shows a profile beside a name and is a registry, not a social app.
	{Tag: Social, Means: "imports a boards (forum) package",
		Imports: []Import{{Segment: "boards"}}},
	// The registry every realm can resolve a @name through. Its own two
	// clients that only register (r/sys/users/init) count: they are about
	// names, which is what the chip says.
	{Tag: Identity, Means: "resolves or registers @usernames: imports r/sys/users",
		Imports: []Import{{Path: "gno.land/r/sys/users"}}},
	// A library, or the hand-rolled version of one: transferring the admin
	// seat, or the guard every admin function opens with. Methods count for
	// the transfer pair only, which is what an ownership library exports.
	{Tag: AccessControl, Means: "imports an ownership, authorization or role package, or defines its own admin transfer or owner check",
		Imports: []Import{{Segment: "ownable"}, {Segment: "authorizable"}, {Segment: "authz"},
			{Segment: "rbac"}, {Segment: "access"}},
		Defines: []string{"TransferOwnership", "AcceptOwnership", "TransferAdmin", "SetAdmin",
			"assertOwner", "assertAdmin", "assertIsAdmin", "requireOwner", "requireAdmin", "onlyOwner", "onlyAdmin"},
		Methods: []string{"TransferOwnership", "AcceptOwnership"}},
	{Tag: Pausable, Means: "can be paused: defines Pause, Unpause, IsPaused, SetPaused or a not-paused guard, or imports a pause or halt package",
		Imports: []Import{{Segment: "pausable"}, {Segment: "halt"}},
		Defines: []string{"Pause", "Unpause", "IsPaused", "SetPaused", "setPaused", "assertNotPaused", "requireNotPaused"}},
	{Tag: Upgradable, Means: "can swap its implementation: an exported function replaces a package-level interface value, or it imports an upgrade library",
		Imports: []Import{{Segment: "upgradeable"}, {Segment: "upgradable"}, {Segment: "version_manager"}},
		Pattern: ImplSwap},
	{Tag: Events, Means: "emits chain events (chain.Emit)",
		Calls: []Call{{Import: "chain", Name: "Emit"}}},
	// Reading the height to stamp a record is not this: 173 packages call
	// ChainHeight and most of them only store it. Ordering against it is a
	// deadline, a lockup or a cooldown.
	{Tag: TimeBased, Means: "acts on time: compares the block height or the clock (deadlines, lockups, cooldowns)",
		Pattern: ClockCompare},
	// bech32 is an address encoding, not cryptography, and is left out.
	{Tag: Crypto, Means: "hashes, or verifies a signature or a merkle proof: imports crypto/sha256, crypto/ed25519, crypto/merkle or a merkle package",
		Imports: []Import{{Path: "crypto/sha256"}, {Path: "crypto/ed25519"}, {Path: "crypto/merkle"}, {Segment: "merkle"}}},
	{Tag: Math, Means: "does wide-integer or overflow-checked arithmetic: imports uint256, int256, math/bits or math/overflow",
		Imports: []Import{{Segment: "uint256"}, {Segment: "int256"}, {Path: "math/bits"}, {Path: "math/overflow"}}},
	{Tag: CrossRealm, Means: "imports another realm, so it calls into or reads another realm's state",
		Imports: []Import{{Prefix: "gno.land/r/"}}},
	{Tag: Render, Means: "defines Render, the page a realm draws for itself",
		Defines: []string{"Render"}, RealmOnly: true},
	{Tag: UI, Means: "builds pages with a markdown, table, SVG, UI kit or pager package",
		Imports: []Import{{Segment: "md"}, {Segment: "mdtable"}, {Segment: "mdlist"}, {Segment: "mdalert"},
			{Segment: "mdform"}, {Segment: "markdown"}, {Segment: "ui"}, {Segment: "pager"},
			{Segment: "svg"}, {Segment: "svgbtn"}}},
	{Tag: Interactive, Means: "its page links to its own transaction forms: imports txlink or helplink, or draws a $help&func= link",
		Imports: []Import{{Segment: "txlink"}, {Segment: "helplink"}},
		Strings: []string{"$help&func="}, RealmOnly: true},
	{Tag: Routing, Means: "serves several pages by path: imports a router (mux) or realmpath",
		Imports: []Import{{Segment: "mux"}, {Segment: "realmpath"}}},
	{Tag: Library, Means: "a pure package at least one other package on this chain imports", PureOnly: true},
	{Tag: SelfContained, Means: "imports nothing outside the standard library"},
	{Tag: TestOnly, Means: "every source file is a test"},
}

// Means returns a tag's sentence, or "" for an unknown tag.
func Means(tag string) string {
	for _, r := range Rules {
		if r.Tag == tag {
			return r.Means
		}
	}
	return ""
}

// Rank is a tag's position in the table, for sorting. Unknown tags sort last.
func Rank(tag string) int {
	for i, r := range Rules {
		if r.Tag == tag {
			return i
		}
	}
	return len(Rules)
}

// Known reports whether tag is in the table.
func Known(tag string) bool { return Rank(tag) < len(Rules) }

// Sort orders tags by the table.
func Sort(ts []Tag) {
	sort.SliceStable(ts, func(i, j int) bool { return Rank(ts[i].Tag) < Rank(ts[j].Tag) })
}

// IsTest reports whether a file is one of gno's two test conventions.
func IsTest(name string) bool {
	return strings.HasSuffix(name, "_test.gno") || strings.HasSuffix(name, "_filetest.gno")
}

func isRealm(path string) bool { return strings.HasPrefix(path, "gno.land/r/") }
func isPure(path string) bool  { return strings.HasPrefix(path, "gno.land/p/") }

// hit is one piece of evidence: what matched, and where.
type hit struct {
	what, file string
	line       int
}

// Derive computes every tag a package's own source earns. Library is not
// among them, because it depends on other packages: see LibraryTag.
//
// Deterministic: files are read in name order and the first hit in that
// order is the evidence, with a count of the rest.
func Derive(path string, files []File) []Tag {
	files = append([]File(nil), files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })

	var src []File
	gno, tests := 0, 0
	for _, f := range files {
		if !strings.HasSuffix(f.Name, ".gno") {
			continue
		}
		gno++
		if IsTest(f.Name) {
			tests++
			continue
		}
		src = append(src, f)
	}

	var out []Tag
	if gno > 0 && tests == gno {
		out = append(out, Tag{Tag: TestOnly, Why: "all " + plural(gno, "source file") + " are tests"})
	}

	parsed := parseAll(src)
	if t, ok := selfContained(src, parsed); ok {
		out = append(out, t)
	}
	for _, r := range Rules {
		if (r.RealmOnly && !isRealm(path)) || (r.PureOnly && !isPure(path)) {
			continue
		}
		var hits []hit
		for _, pf := range parsed {
			hits = append(hits, matchImports(r, pf)...)
			hits = append(hits, matchCalls(r, pf)...)
			hits = append(hits, matchDefines(r, pf)...)
			hits = append(hits, matchStrings(r, pf)...)
		}
		switch r.Pattern {
		case ImplSwap:
			hits = append(hits, matchImplSwap(parsed)...)
		case ClockCompare:
			for _, pf := range parsed {
				hits = append(hits, matchClockCompare(pf)...)
			}
		}
		if len(hits) == 0 {
			continue
		}
		out = append(out, Tag{Tag: r.Tag, Why: why(hits)})
	}
	Sort(out)
	return out
}

// LibraryTag says whether a pure package earns library, from the packages on
// its network that import it. dependents must be sorted; the first one is
// named in the evidence.
func LibraryTag(path string, dependents []string) (Tag, bool) {
	if !isPure(path) || len(dependents) == 0 {
		return Tag{}, false
	}
	w := "imported by " + dependents[0]
	if n := len(dependents) - 1; n > 0 {
		w += " and " + plural(n, "other package")
	}
	return Tag{Tag: Library, Why: w}, true
}

func why(hits []hit) string {
	h := hits[0]
	w := h.what + " (" + h.file + ":" + strconv.Itoa(h.line) + ")"
	if n := len(hits) - 1; n > 0 {
		w += " and " + strconv.Itoa(n) + " more"
	}
	return w
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// parsedFile is one file's imports and, when it parsed whole, its syntax.
type parsedFile struct {
	name    string
	fset    *token.FileSet
	f       *ast.File // nil when only the imports parsed
	imports []importAt
	local   map[string]string // local name -> import path
}

type importAt struct {
	path string
	line int
}

func parseAll(files []File) []parsedFile {
	var out []parsedFile
	for _, f := range files {
		fset := token.NewFileSet()
		af, err := parser.ParseFile(fset, f.Name, f.Body, parser.SkipObjectResolution)
		pf := parsedFile{name: f.Name, fset: fset, local: map[string]string{}}
		if err != nil {
			// A file gno accepts and Go's parser does not still has an import
			// block Go can read; its symbols are skipped rather than guessed.
			af, err = parser.ParseFile(fset, f.Name, f.Body, parser.ImportsOnly|parser.SkipObjectResolution)
			if err != nil || af == nil {
				continue
			}
		} else {
			pf.f = af
		}
		for _, spec := range af.Imports {
			p, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				continue
			}
			pf.imports = append(pf.imports, importAt{path: p, line: fset.Position(spec.Pos()).Line})
			name := p[strings.LastIndexByte(p, '/')+1:]
			if spec.Name != nil {
				name = spec.Name.Name
			}
			pf.local[name] = p
		}
		out = append(out, pf)
	}
	return out
}

// spellings is a path element as written and without its generation suffix:
// `boards2` is also `boards`, `grc721v2` is also `grc721`, and `grc20`, whose
// number is part of its name, stays `grc20` as well.
func spellings(seg string) []string {
	out := []string{seg}
	t := strings.TrimRight(seg, "0123456789")
	if t != seg && t != "" {
		out = append(out, t)
		if strings.HasSuffix(t, "v") && len(t) > 1 {
			out = append(out, t[:len(t)-1])
		}
	}
	return out
}

func (m Import) match(imp string) bool {
	if m.Pure && !isPure(imp) {
		return false
	}
	if m.Path != "" {
		return imp == m.Path
	}
	if m.Prefix != "" {
		return strings.HasPrefix(imp, m.Prefix)
	}
	if !strings.HasPrefix(imp, "gno.land/") {
		return false
	}
	// Elements after gno.land/<r|p>/: the namespace counts, a namespace named
	// after a standard is rare and the evidence would show it.
	segs := strings.Split(imp, "/")
	if len(segs) < 3 {
		return false
	}
	for _, s := range segs[2:] {
		for _, sp := range spellings(s) {
			if m.Segment != "" && sp == m.Segment {
				return true
			}
			if m.Suffix != "" && strings.HasSuffix(sp, m.Suffix) {
				return true
			}
		}
	}
	return false
}

func matchImports(r Rule, pf parsedFile) []hit {
	var out []hit
	for _, imp := range pf.imports {
		for _, m := range r.Imports {
			if m.match(imp.path) {
				out = append(out, hit{what: "imports " + imp.path, file: pf.name, line: imp.line})
				break
			}
		}
	}
	return out
}

func matchCalls(r Rule, pf parsedFile) []hit {
	if len(r.Calls) == 0 || pf.f == nil {
		return nil
	}
	var out []hit
	ast.Inspect(pf.f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		x, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		imp, ok := pf.local[x.Name]
		if !ok {
			return true
		}
		for _, c := range r.Calls {
			if c.Import != imp || !nameMatch(c.Name, sel.Sel.Name) {
				continue
			}
			if c.NotArg0 != "" && len(call.Args) > 0 && lastName(call.Args[0]) == c.NotArg0 {
				continue
			}
			out = append(out, hit{what: "calls " + x.Name + "." + sel.Sel.Name, file: pf.name,
				line: pf.fset.Position(call.Pos()).Line})
		}
		return true
	})
	return out
}

// lastName is the identifier an expression ends in: `banker.BankerTypeReadonly`
// and `BankerTypeReadonly` both give BankerTypeReadonly.
func lastName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return v.Sel.Name
	}
	return ""
}

func matchDefines(r Rule, pf parsedFile) []hit {
	if (len(r.Defines) == 0 && len(r.Methods) == 0) || pf.f == nil {
		return nil
	}
	var out []hit
	for _, d := range pf.f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		names, what := r.Defines, "defines "
		if fn.Recv != nil {
			names, what = r.Methods, "defines the method "
		}
		for _, name := range names {
			if fn.Name.Name == name {
				out = append(out, hit{what: what + name, file: pf.name, line: pf.fset.Position(fn.Pos()).Line})
			}
		}
	}
	return out
}

// nameMatch is a Call's Name against a function's: exact, or a prefix when
// the pattern ends in `*`.
func nameMatch(pattern, name string) bool {
	if p, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(name, p)
	}
	return pattern == name
}

func matchStrings(r Rule, pf parsedFile) []hit {
	if len(r.Strings) == 0 || pf.f == nil {
		return nil
	}
	var out []hit
	ast.Inspect(pf.f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		for _, s := range r.Strings {
			if strings.Contains(lit.Value, s) {
				out = append(out, hit{what: "draws a " + s + " link", file: pf.name, line: pf.fset.Position(lit.Pos()).Line})
				break
			}
		}
		return true
	})
	return out
}

// selfContained is the self-contained tag: at least one source file, and no
// import of anything under gno.land/ in any of them.
func selfContained(src []File, parsed []parsedFile) (Tag, bool) {
	if len(src) == 0 || len(parsed) != len(src) {
		return Tag{}, false
	}
	std := 0
	for _, pf := range parsed {
		for _, imp := range pf.imports {
			if strings.HasPrefix(imp.path, "gno.land/") {
				return Tag{}, false
			}
			std++
		}
	}
	if std == 0 {
		return Tag{Tag: SelfContained, Why: "imports nothing (" + plural(len(src), "source file") + ")"}, true
	}
	return Tag{Tag: SelfContained, Why: "imports only the standard library (" + plural(len(src), "source file") + ")"}, true
}

// matchImplSwap finds the ImplSwap pattern across a package's files: the
// interface and the variable may be declared in one file and swapped in
// another.
func matchImplSwap(parsed []parsedFile) []hit {
	ifaces := map[string]bool{}
	for _, pf := range parsed {
		if pf.f == nil {
			continue
		}
		for _, d := range pf.f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, sp := range gd.Specs {
				if ts, ok := sp.(*ast.TypeSpec); ok {
					if _, ok := ts.Type.(*ast.InterfaceType); ok {
						ifaces[ts.Name.Name] = true
					}
				}
			}
		}
	}
	if len(ifaces) == 0 {
		return nil
	}
	vars := map[string]bool{}
	for _, pf := range parsed {
		if pf.f == nil {
			continue
		}
		for _, d := range pf.f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, sp := range gd.Specs {
				vs, ok := sp.(*ast.ValueSpec)
				if !ok {
					continue
				}
				if id, ok := vs.Type.(*ast.Ident); ok && ifaces[id.Name] {
					for _, n := range vs.Names {
						vars[n.Name] = true
					}
				}
			}
		}
	}
	if len(vars) == 0 {
		return nil
	}
	var out []hit
	for _, pf := range parsed {
		if pf.f == nil {
			continue
		}
		for _, d := range pf.f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !fn.Name.IsExported() || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				as, ok := n.(*ast.AssignStmt)
				if !ok || as.Tok != token.ASSIGN {
					return true
				}
				for _, l := range as.Lhs {
					if id, ok := l.(*ast.Ident); ok && vars[id.Name] {
						out = append(out, hit{what: fn.Name.Name + " replaces the implementation " + id.Name,
							file: pf.name, line: pf.fset.Position(as.Pos()).Line})
					}
				}
				return true
			})
		}
	}
	return out
}

// clockValue says which clock an expression's value is, if any: a call to
// one, resolved through the file's imports, a variable in tv, or one of
// those offset (+, -), converted (`int64(h)`) or read through a method of
// the time it returned (`time.Now().Unix()`). Anything else that merely
// mentions a clock, a struct literal carrying a height or a value derived
// by `%` or a call, is not: that is a record or a dice roll, not a time.
func clockValue(e ast.Expr, local map[string]string, tv map[string]string) string {
	switch v := e.(type) {
	case *ast.ParenExpr:
		return clockValue(v.X, local, tv)
	case *ast.Ident:
		return tv[v.Name]
	case *ast.BinaryExpr:
		if v.Op != token.ADD && v.Op != token.SUB {
			return ""
		}
		if s := clockValue(v.X, local, tv); s != "" {
			return s
		}
		return clockValue(v.Y, local, tv)
	case *ast.CallExpr:
		switch fun := v.Fun.(type) {
		case *ast.Ident:
			if len(v.Args) == 1 && conversions[fun.Name] {
				return clockValue(v.Args[0], local, tv)
			}
		case *ast.SelectorExpr:
			if x, ok := fun.X.(*ast.Ident); ok {
				switch imp := local[x.Name]; {
				case imp == "chain/runtime" && fun.Sel.Name == "ChainHeight":
					return x.Name + ".ChainHeight"
				case imp == "time" && (fun.Sel.Name == "Now" || fun.Sel.Name == "Since" || fun.Sel.Name == "Until"):
					return x.Name + "." + fun.Sel.Name
				}
			}
			return clockValue(fun.X, local, tv)
		}
	}
	return ""
}

var conversions = map[string]bool{"int": true, "int32": true, "int64": true, "uint": true, "uint32": true, "uint64": true}

// mathLimit is `math.MaxUint32` and its kind: comparing a height to one is
// an overflow guard, not a deadline.
func mathLimit(e ast.Expr, local map[string]string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && local[x.Name] == "math"
}

func matchClockCompare(pf parsedFile) []hit {
	if pf.f == nil {
		return nil
	}
	var out []hit
	for _, d := range pf.f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		// Variables assigned from a clock in this function, in source order:
		// `now := runtime.ChainHeight()` then `if now > deadline`.
		tv := map[string]string{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.AssignStmt:
				if len(v.Lhs) != len(v.Rhs) {
					return true
				}
				for i, r := range v.Rhs {
					if s := clockValue(r, pf.local, tv); s != "" {
						if id, ok := v.Lhs[i].(*ast.Ident); ok {
							tv[id.Name] = s
						}
					}
				}
			case *ast.BinaryExpr:
				switch v.Op {
				case token.LSS, token.GTR, token.LEQ, token.GEQ:
					if mathLimit(v.X, pf.local) || mathLimit(v.Y, pf.local) {
						return true
					}
					s := clockValue(v.X, pf.local, tv)
					if s == "" {
						s = clockValue(v.Y, pf.local, tv)
					}
					if s != "" {
						out = append(out, hit{what: "compares " + s, file: pf.name, line: pf.fset.Position(v.Pos()).Line})
					}
				}
			case *ast.CallExpr:
				sel, ok := v.Fun.(*ast.SelectorExpr)
				if !ok || (sel.Sel.Name != "Before" && sel.Sel.Name != "After") {
					return true
				}
				if s := clockValue(sel.X, pf.local, tv); s != "" {
					out = append(out, hit{what: "compares " + s, file: pf.name, line: pf.fset.Position(v.Pos()).Line})
				}
			}
			return true
		})
	}
	return out
}
