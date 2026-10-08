package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gnoverse/gnoscope/pkg/store"
)

// "What is this address?", answered before the reader has to guess.
//
// A gno address is forty characters and nothing else. It carries no type, no
// prefix and no marker: a person's wallet, a realm's treasury, a realm's
// storage deposit, a delegated signing key and an account that has never done
// anything all look identical. So the address page used to open on a table of
// transactions and leave the most basic question unanswered, and for a realm
// account it opened on an *empty* table, which reads as "nothing here" when the
// truth was "15.4M GNS, and this is gnoswap's pool".
//
// Every fact below is either proved or absent. Nothing here is a heuristic, and
// where the chain genuinely cannot answer (was this address in genesis, when it
// carries no vesting schedule) the field is simply missing rather than guessed:
// a confidently wrong identity on a block explorer is worse than no identity,
// because a reader acts on it.
//
// The four sources, and what only each one can say:
//
//	package_accounts   this address is a package's account, and which one.
//	                   Derived, because the chain stores a hash of the path and
//	                   never the path (pkg/gnoaddr).
//	auth/accounts      whether it has ever signed, and whether it was funded at
//	                   genesis. Live RPC: neither fact is in any index.
//	session_grants     whether it is a delegated key, and whose. Only the grant
//	                   transaction ties the two together (pkg/httpapi/api_sessions.go).
//	storage            what it has done since, and what it deployed.

// identityKind is the one-word verdict the page leads with. Ordered by how much
// it tells a reader, strongest first, and resolved in that order.
const (
	// identityPackage: the banker of a deployed package. Its money is the
	// realm's, and nobody holds a key for it.
	identityPackage = "package"
	// identityPackageDeposit: the ugnot locked against a package's bytes. A
	// different account from the banker, with a different balance.
	identityPackageDeposit = "package_deposit"
	// identitySessionKey: a delegated key. It signs, but every message it sends
	// is recorded under its master, so its own page is empty by construction.
	identitySessionKey = "session_key"
	// identitySigner: the chain holds a public key for it, which it records the
	// first time it verifies a signature and never before. Somebody has the
	// private key.
	identitySigner = "signer"
	// identityUnsigned: the chain knows the account -- it has held coins -- but
	// has never seen it sign. A realm this explorer has not indexed, a funded
	// key nobody has used yet, or a typo somebody sent money to.
	identityUnsigned = "unsigned"
	// identityConsensusKey: a validator's signing key. It signs blocks, not
	// transactions, so the chain usually holds no account for it, and without
	// this verdict the page called a validator signing every block "no
	// account" (onyx-1, 2026-10-01).
	identityConsensusKey = "consensus_key"
	// identityUnknown: the chain has no account at all. Either nothing was ever
	// sent here, or the RPC could not be reached, and chain.exists says which.
	identityUnknown = "unknown"
)

// addressIdentity is the whole answer, as facts rather than prose. The page
// writes the sentences; putting them here would mean the API could only ever be
// read by this frontend.
type addressIdentity struct {
	Address string `json:"address"`
	Network string `json:"network,omitempty"`
	Kind    string `json:"kind"`

	// Package is set when this address is a package's account.
	Package *store.PackageAccount `json:"package,omitempty"`

	// Chain is what auth/accounts says, or nil when the node could not be
	// asked. ChainError distinguishes the two: absent chain data with no error
	// never happens, and a reader must not read "could not ask" as "no account".
	Chain      *identityChain `json:"chain,omitempty"`
	ChainError string         `json:"chain_error,omitempty"`

	// User is the r/sys/users registration pointing here, if any.
	User *store.User `json:"user,omitempty"`

	// SessionOf is the master this address signs for, when it is a delegated
	// key. SessionGrants counts the grants it has held, because a key can be
	// granted, revoked and granted again.
	SessionOf     string `json:"session_of,omitempty"`
	SessionGrants int    `json:"session_grants,omitempty"`

	// Delegates is how many keys this address has granted, the other direction.
	Delegates int `json:"delegates,omitempty"`

	// Genesis is what the gnoland-1 genesis sheet says about this address, on
	// mainnet only and only when the sheet is loaded. It is the one exact answer
	// to "was this in genesis" that the chain cannot give: Chain.Vesting proves a
	// yes, and nothing on chain can prove a no.
	Genesis *genesisAnswer `json:"genesis,omitempty"`

	// Validator is the moniker this address registered on r/gnops/valopers.
	// Self-declared, which is why it is not a label: anyone may claim any name.
	Validator string `json:"validator,omitempty"`

	// Valset is this address's place in the network's validator set, as
	// either key of a validator: the one it signs blocks with, or the operator
	// its profile is registered under. Live, so only with one network.
	Valset *identityValset `json:"valset,omitempty"`

	// Transactions is what storage has seen this address sign: distinct
	// transactions, not messages, so it can be read against the chain's own
	// sequence number. Deploys are not here on purpose: /api/address already
	// reports them to the same page.
	Transactions int `json:"transactions"`

	// FirstSeen is the first call it ever signed. Absent for an address that
	// has never called anything, which includes every realm.
	FirstSeen *store.FirstSeenRow `json:"first_seen,omitempty"`

	// Label, LabelKind and LabelWhy are the merged registry's name for it, so
	// the page has one place to read an identity from rather than two.
	Label     string `json:"label,omitempty"`
	LabelKind string `json:"label_kind,omitempty"`
	LabelWhy  string `json:"label_why,omitempty"`
}

// identityValset joins a validator's two keys and the proposals that seated it.
type identityValset struct {
	// Role is which key this address is: "signing" or "operator".
	Role        string              `json:"role"`
	Moniker     string              `json:"moniker,omitempty"`
	Operator    string              `json:"operator,omitempty"`
	Signing     string              `json:"signing"`
	InSet       bool                `json:"in_set"`
	VotingPower string              `json:"voting_power,omitempty"`
	TotalPower  int64               `json:"total_power,omitempty"`
	Proposals   []ValsetProposalRef `json:"proposals,omitempty"`
}

// valsetIdentity places addr in the set, or returns nil when it is neither key
// of any validator or registered profile.
func valsetIdentity(vs Valset, addr string) *identityValset {
	prof, ok := vs.Valopers[addr]
	role := "signing"
	if !ok {
		if prof, ok = vs.ByOperator(addr); ok {
			role = "operator"
		}
	}
	signing := addr
	if ok {
		signing = prof.Signing
	}
	m, inSet := vs.Member(signing)
	if !ok && !inSet {
		return nil
	}
	return &identityValset{
		Role: role, Moniker: prof.Moniker, Operator: prof.Operator, Signing: signing,
		InSet: inSet, VotingPower: m.VotingPower, TotalPower: vs.TotalPower,
		Proposals: vs.ProposalsFor(signing, prof.Operator),
	}
}

// identityChain is auth/accounts, flattened and with the one inference the
// chain's own encoding invites spelled out.
type identityChain struct {
	// Exists is false when the query succeeded and returned null: no account
	// object, which means this address has never held ugnot. It says nothing
	// about GRC20 holdings, which live in realm state and not in the bank.
	Exists bool `json:"exists"`

	Coins string `json:"coins,omitempty"`

	// HasSigned is the exact test for "is a key behind this address", and it is
	// the one fact on this page that nothing else can supply. tm2 records an
	// account's public key the first time it verifies a signature from it and
	// never otherwise, so a null public_key on a funded account is proof that
	// nobody has ever signed with it.
	HasSigned  bool   `json:"has_signed"`
	PubKeyType string `json:"pub_key_type,omitempty"`

	// Sequence is how many transactions it has signed.
	Sequence      int64 `json:"sequence"`
	AccountNumber int64 `json:"account_number,omitempty"`

	// Vesting is present only on an account funded at genesis.
	//
	// Nothing on gno.land can set a vesting schedule after genesis: the only
	// caller of SetVesting is InitChainerConfig.applyBalance
	// (gno.land/pkg/gnoland/app.go), which runs once, and no message reaches
	// it. So a schedule here *proves* a genesis allocation.
	//
	// ⚠️ The converse does not hold. A genesis entry with no vesting clause is
	// an ordinary account afterwards and indistinguishable from one funded a
	// year later, so the absence of this field is not evidence of anything.
	// The only exact source for that is the genesis file itself, and the public
	// RPC cannot serve it: /genesis is over 250MB and the node's 30-second
	// write deadline truncates it mid-stream, with no /genesis_chunked to fall
	// back on (measured against rpc.gno.land, 2026-09-28).
	Vesting *identityVesting `json:"vesting,omitempty"`
}

// identityVesting is the schedule, as the chain spells it.
//
// The amount stays a coin string rather than a number for the reason every
// other coin field here does: a coin string is a list ("5foo,100ugnot"), and
// parsing one into an int invents a denom assumption. See pkg/store/schema.go
// on coin_transfers.
type identityVesting struct {
	Original  string `json:"original"`
	StartTime int64  `json:"start_time"`
	EndTime   int64  `json:"end_time"`
	// Delayed is a schedule that releases nothing before EndTime. The chain
	// leaves start_time out for it (so StartTime is 0, which is 1970, not a
	// date), and only says so in a "type" field every other account lacks.
	Delayed bool `json:"delayed,omitempty"`
}

// HandleAddressIdentity serves GET /api/address/{addr}/identity.
//
// Kept apart from HandleAddress rather than folded into it. That endpoint is
// the transaction pager, it is the one the page reloads as a reader walks
// through history, and it is already the heaviest query on the page. This one
// makes a live RPC call, so pinning it to the pager would put a network
// round-trip behind every "next page" click.
func (a *API) HandleAddressIdentity(w http.ResponseWriter, r *http.Request) {
	network := a.networkParam(r)
	addr := r.PathValue("addr")

	// An empty address is not a lookup: db.Search("") matches every package on
	// the chain, so the deploys figure would come back as the whole index and
	// the verdict with it.
	if addr == "" {
		jsonError(w, "missing address", 400)
		return
	}

	out := addressIdentity{Address: addr, Network: network, Kind: identityUnknown}

	if pa, ok, err := a.db.LookupPackageAccount(network, addr); err != nil {
		jsonError(w, err.Error(), 500)
		return
	} else if ok {
		out.Package = &pa
		out.Kind = identityPackage
		if pa.Deposit {
			out.Kind = identityPackageDeposit
		}
	}

	grants, err := a.db.SessionGrantsByAddress(network, addr)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	if len(grants) > 0 {
		out.SessionOf = grants[0].Master
		out.SessionGrants = len(grants)
		// A session key is never also a package account -- one is a hash of a
		// path, the other a key somebody generated -- so this cannot overwrite
		// a stronger verdict in practice. Guarded anyway, because "in practice"
		// is how a page ends up calling gnoswap's pool a delegated key.
		if out.Package == nil {
			out.Kind = identitySessionKey
		}
	}
	granted, err := a.db.SessionGrantsByMaster(network, addr)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	out.Delegates = len(granted)

	if u, err := a.db.UserByAddress(network, addr); err == nil && u != nil {
		out.User = u
	}

	if regs, err := a.db.ValoperRegistrations(network); err == nil {
		for _, reg := range regs {
			if reg.Address == addr && reg.Moniker != "" {
				out.Validator = reg.Moniker
				break
			}
		}
	}

	// The one activity figure the verdict leans on. A single row is fetched
	// rather than none because AddressTransactions reports the total alongside
	// the window and there is no count-only form of it.
	//
	// Deploys are deliberately not counted here. /api/address already returns
	// them and the page fetches both endpoints together, so counting them again
	// would run db.Search a second time on every address page load, for a
	// number the caller already has.
	// Txs, not Messages: the verdict beside it reads "has signed N
	// transactions", and the chain's own sequence number is what a reader
	// checks it against.
	if _, total, err := a.db.AddressTransactions(network, addr, 1, 0); err == nil {
		out.Transactions = total.Txs
	}
	if fs, ok, err := a.db.FirstSeenAt(network, store.FirstSeenAddress, addr); err == nil && ok {
		out.FirstSeen = &fs
	}

	if labels, err := a.labelsFor(network); err == nil {
		if l, ok := labels[addr]; ok {
			out.Label, out.LabelKind, out.LabelWhy = l.Label, l.Kind, l.Why
		}
	}

	// Live, last, and only when one chain is selected: an account object is per
	// chain, and merging two would be the same category error as summing two
	// chains' balances.
	if network != "" {
		chain, err := fetchAccount(r.Context(), addr, a.rpcURLFor(network))
		switch {
		case err != nil:
			out.ChainError = err.Error()
		default:
			out.Chain = chain
			if out.Kind == identityUnknown {
				switch {
				case chain.HasSigned:
					out.Kind = identitySigner
				case chain.Exists:
					out.Kind = identityUnsigned
				}
			}
		}
	}
	if network != "" {
		out.Valset = valsetIdentity(a.FetchValset(r.Context(), network), addr)
		if out.Valset != nil && out.Valset.Role == "signing" && out.Package == nil &&
			(out.Kind == identityUnknown || out.Kind == identityUnsigned) && out.Transactions == 0 {
			out.Kind = identityConsensusKey
		}
	}
	// Storage can prove a signer even when the RPC could not be reached: a row
	// naming this address as a caller is a transaction it signed.
	if out.Kind == identityUnknown && out.Transactions > 0 {
		out.Kind = identitySigner
	}

	// The genesis sheet is gnoland-1's, so it speaks only for mainnet, and only
	// to an address that is a bech32 g1 key (a package account hashed from a path
	// is one too, and is correctly "not in genesis").
	if network == genesisNetwork {
		if g := a.genesisLookup(addr); g.Status == genesisFound || g.Status == genesisAbsent {
			out.Genesis = &g
		}
	}

	JSONResponse(w, out)
}

// fetchAccount reads auth/accounts/<addr> and flattens it.
//
// A nil error with Exists false is a successful read of an address the chain
// has no account for, which is the common and correct answer for a realm whose
// money is all GRC20: gno.land/r/gnoswap/pool holds 15.4M GNS and has no bank
// account at all. Separating that from "the node could not be asked" is the
// same distinction fetchBalanceErr had to make, and for the same reason.
func fetchAccount(ctx context.Context, addr, rpcURL string) (*identityChain, error) {
	raw, err := fetchABCIQuery(ctx, rpcURL, "auth/accounts/"+addr, "")
	if err != nil {
		return nil, err
	}
	return decodeAccount(raw)
}

// decodeAccount is the parsing half, split out so it can be tested against the
// chain's real payloads without a node. Every branch below was written from one:
// a signer with a vesting schedule, a realm's banker with coins and no public
// key, and a realm holding only GRC20, for which the chain answers "null".
func decodeAccount(raw string) (*identityChain, error) {
	body := strings.TrimSpace(raw)
	if body == "" || body == "null" {
		return &identityChain{}, nil
	}

	// Numbers arrive as strings, the way amino's JSON encoding writes int64.
	// The account is wrapped in a type key on this chain (BaseAccount, or
	// BaseSessionAccount for a session); the inner shape is the same, so it is
	// decoded twice rather than switched on, and a shape neither matches leaves
	// every field zero instead of failing the page.
	var wrapper struct {
		Base *accountJSON `json:"BaseAccount"`
	}
	if err := json.Unmarshal([]byte(body), &wrapper); err != nil {
		return nil, err
	}
	acct := wrapper.Base
	if acct == nil || acct.Address == "" {
		var flat accountJSON
		if err := json.Unmarshal([]byte(body), &flat); err != nil {
			return nil, err
		}
		acct = &flat
	}
	if acct.Address == "" {
		return &identityChain{}, nil
	}

	out := &identityChain{
		Exists:        true,
		Coins:         acct.Coins,
		Sequence:      atoi64(acct.Sequence),
		AccountNumber: atoi64(acct.AccountNumber),
	}
	if acct.PublicKey != nil && acct.PublicKey.Type != "" {
		out.HasSigned = true
		out.PubKeyType = acct.PublicKey.Type
	}
	if v := acct.Vesting; v != nil && v.OriginalVesting != "" {
		out.Vesting = &identityVesting{
			Original:  v.OriginalVesting,
			StartTime: atoi64(v.StartTime),
			EndTime:   atoi64(v.EndTime),
			Delayed:   v.Type == "delayed",
		}
	}
	return out, nil
}

type accountJSON struct {
	Address       string `json:"address"`
	Coins         string `json:"coins"`
	AccountNumber string `json:"account_number"`
	Sequence      string `json:"sequence"`
	PublicKey     *struct {
		Type string `json:"@type"`
	} `json:"public_key"`
	Vesting *struct {
		OriginalVesting string `json:"original_vesting"`
		StartTime       string `json:"start_time"`
		EndTime         string `json:"end_time"`
		Type            string `json:"type"`
	} `json:"vesting"`
}
