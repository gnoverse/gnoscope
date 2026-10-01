package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gnoverse/gnoscope/pkg/store"
)

// The live validator set of one network, as that network's own chain reports it.
//
// Until 2026-10-01 /validators read its set from gnockpit, which describes
// mainnet and nothing else, and served that set for every network. On onyx-1
// the page joined mainnet's four validators onto onyx's proposers, and a
// validator page said "not in the current set" for a key signing every block
// (g1g0yrm… at power 4 of 215, measured that day). The set now comes from the
// chain being shown, and each source answers only what only it can:
//
//	RPC /validators          who is in the set, and with what power. tm2
//	                         ignores page/per_page and returns all of it.
//	r/gnops/valopers         signing address -> operator -> moniker, in one
//	                         vm/qeval. The realm stores SigningAddress on every
//	                         profile since the key-rotation work, so the
//	                         consensus and operator address spaces DO join now.
//	                         The older "nothing on chain maps them" comments in
//	                         this package predate that realm version.
//	r/gov/dao renders        which proposal changed an operator's membership.
//	gnockpit                 missed blocks and block time, and only when its
//	                         `chain` is the chain this network serves.

// ValsetMember is one validator in a network's current set.
//
// VotingPower stays a string, as the RPC and gnockpit both send it, so a large
// chain cannot overflow a JS number on the way to the page.
type ValsetMember struct {
	Address     string `json:"address"`
	VotingPower string `json:"voting_power"`
	// Operator and Moniker come from r/gnops/valopers: self-declared, so a
	// claim rather than a fact. Absent for a genesis validator that never
	// registered a profile.
	Operator string `json:"operator,omitempty"`
	Moniker  string `json:"moniker,omitempty"`
	// Name is what the page should call it: the moniker, else gnockpit's name.
	Name string `json:"name,omitempty"`
	// SPOF: losing this validator alone leaves the rest at or below 2/3 of
	// total power, so consensus cannot proceed without it.
	SPOF bool `json:"spof"`
	// The liveness figures only gnockpit has, nil where it does not describe
	// this chain. Nil rather than zero: zero missed blocks is a claim.
	Missed100  *int `json:"missed_100,omitempty"`
	Missed24h  *int `json:"missed_24h,omitempty"`
	AvgBlockMs *int `json:"avg_block_ms,omitempty"`
	// Proposals are the GovDAO proposals naming this validator's operator or
	// signing address in their "Validator Updates" section, newest first.
	Proposals []ValsetProposalRef `json:"proposals,omitempty"`
}

// ValsetProposalRef is one GovDAO proposal that changes a validator's membership.
type ValsetProposalRef struct {
	ID     int    `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status,omitempty"`
	// Op is the proposal's own word for the change: add or remove. A power
	// change is an add with a new power, which is how r/sys/validators spells
	// an upsert.
	Op    string `json:"op"`
	Power int64  `json:"power,omitempty"`
}

// Valoper is one r/gnops/valopers profile, as the realm holds it right now.
type Valoper struct {
	Operator string `json:"operator"`
	Signing  string `json:"signing"`
	Moniker  string `json:"moniker"`
}

// Valset is a network's set plus the lookups the pages join against.
type Valset struct {
	Network    string         `json:"network"`
	ChainID    string         `json:"chain_id,omitempty"`
	Height     int64          `json:"height,omitempty"`
	TotalPower int64          `json:"total_power"`
	Members    []ValsetMember `json:"members"`
	// Valopers covers every registered profile, in the set or not, keyed by
	// signing address. The set alone cannot name a validator that has
	// registered and is waiting on its proposal.
	Valopers map[string]Valoper `json:"-"`
	// Proposals is keyed by whichever address the proposal wrote, operator or
	// signing; Lookup tries both.
	Proposals map[string][]ValsetProposalRef `json:"-"`
	Errors    []string                       `json:"errors,omitempty"`
}

// Member returns the set entry for a signing address.
func (v Valset) Member(addr string) (ValsetMember, bool) {
	for _, m := range v.Members {
		if m.Address == addr {
			return m, true
		}
	}
	return ValsetMember{}, false
}

// ByOperator returns the profile registered by an operator address.
func (v Valset) ByOperator(addr string) (Valoper, bool) {
	for _, p := range v.Valopers {
		if p.Operator == addr {
			return p, true
		}
	}
	return Valoper{}, false
}

// ProposalsFor returns the proposals naming either key of one validator.
func (v Valset) ProposalsFor(signing, operator string) []ValsetProposalRef {
	var out []ValsetProposalRef
	seen := map[int]bool{}
	for _, k := range []string{operator, signing} {
		if k == "" {
			continue
		}
		for _, p := range v.Proposals[k] {
			if !seen[p.ID] {
				seen[p.ID] = true
				out = append(out, p)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

// valsetCacheTTL: about twenty blocks. A set changes only by proposal, so the
// cost of a minute's staleness is a just-accepted validator appearing late.
const valsetCacheTTL = time.Minute

var valsetCache = newMemo[Valset](valsetCacheTTL, 10*time.Second)

// FetchValset returns a network's current set, best-effort and cached. A
// partial answer is served with Errors saying what is missing; only a failed
// RPC read leaves Members empty.
func (a *API) FetchValset(ctx context.Context, network string) Valset {
	rpcURL := a.rpcURLFor(network)
	return valsetCache.get(ctx, network, func(ctx context.Context) (Valset, bool) {
		vs := Valset{Network: network, Valopers: map[string]Valoper{}, Proposals: map[string][]ValsetProposalRef{}}
		if rpcURL == "" {
			vs.Errors = append(vs.Errors, "no verified RPC for this network")
			return vs, false
		}
		if chain, _, err := rpcStatus(ctx, rpcURL); err == nil {
			vs.ChainID = chain
		}
		members, height, err := fetchRPCValidators(ctx, rpcURL)
		if err != nil {
			vs.Errors = append(vs.Errors, "validators: "+err.Error())
			return vs, false
		}
		vs.Height = height
		vs.Members = members

		if vops, err := fetchValopers(ctx, rpcURL); err != nil {
			vs.Errors = append(vs.Errors, "valopers: "+err.Error())
		} else {
			vs.Valopers = vops
		}
		if props, err := fetchValsetProposals(ctx, network, rpcURL); err != nil {
			vs.Errors = append(vs.Errors, "proposals: "+err.Error())
		} else {
			vs.Proposals = props
		}

		var gnockpit map[string]GnockpitValidator
		if chain := fetchGnockpitChain(ctx); chain != "" && chain == vs.ChainID {
			gnockpit = map[string]GnockpitValidator{}
			for _, g := range FetchGnockpitValidators(ctx) {
				gnockpit[g.Address] = g
			}
		}

		for _, m := range vs.Members {
			p, _ := strconv.ParseInt(m.VotingPower, 10, 64)
			vs.TotalPower += p
		}
		for i := range vs.Members {
			m := &vs.Members[i]
			if op, ok := vs.Valopers[m.Address]; ok {
				m.Operator, m.Moniker, m.Name = op.Operator, op.Moniker, op.Moniker
			}
			if g, ok := gnockpit[m.Address]; ok {
				if m.Name == "" {
					m.Name = g.Name
				}
				m.Missed100, m.Missed24h, m.AvgBlockMs = intp(g.Missed100), intp(g.Missed24h), intp(g.AvgBlockMs)
			}
			p, _ := strconv.ParseInt(m.VotingPower, 10, 64)
			m.SPOF = isSPOF(p, vs.TotalPower)
			m.Proposals = vs.ProposalsFor(m.Address, m.Operator)
		}
		return vs, true
	})
}

func intp(n int) *int { return &n }

// isSPOF reports whether the rest of the set, without this power, fails the BFT
// quorum: strictly more than 2/3 of total.
func isSPOF(power, total int64) bool {
	if total <= 0 {
		return false
	}
	return 3*(total-power) <= 2*total
}

// fetchRPCValidators reads the whole current set from one RPC.
func fetchRPCValidators(ctx context.Context, rpcURL string) ([]ValsetMember, int64, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", rpcURL+"/validators", nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := abciClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("rpc %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, 0, err
	}
	var out struct {
		Error  *struct{ Message string } `json:"error"`
		Result struct {
			BlockHeight string `json:"block_height"`
			Validators  []struct {
				Address     string `json:"address"`
				VotingPower string `json:"voting_power"`
			} `json:"validators"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, 0, err
	}
	if out.Error != nil {
		return nil, 0, fmt.Errorf("rpc error: %s", out.Error.Message)
	}
	members := make([]ValsetMember, 0, len(out.Result.Validators))
	for _, v := range out.Result.Validators {
		members = append(members, ValsetMember{Address: v.Address, VotingPower: v.VotingPower})
	}
	height, _ := strconv.ParseInt(out.Result.BlockHeight, 10, 64)
	return members, height, nil
}

// valopersPath is the profile registry, at the same path on mainnet and onyx-1.
const valopersPath = "gno.land/r/gnops/valopers"

// valopersQuery walks the realm's own tree in one vm/qeval, so the cost is one
// round trip however many profiles exist. It reads the unexported `valopers`
// tree, which qeval allows because the expression is evaluated in the
// package's scope; the realm's Render paginates at 50 and does not print the
// signing key on its list page, so it cannot answer this in one call.
//
// Tab-separated, one profile per line. A moniker is validated against
// [a-zA-Z0-9][\w -]*, so it can hold neither a tab nor a newline.
const valopersQuery = valopersPath + `.func() string { s := ""; valopers.Iterate("", "", func(k string, v any) bool { x := v.(Valoper); s += x.SigningAddress.String() + "\t" + k + "\t" + x.Moniker + "\n"; return false }); return s }()`

func fetchValopers(ctx context.Context, rpcURL string) (map[string]Valoper, error) {
	raw, err := fetchABCIQuery(ctx, rpcURL, "vm/qeval", valopersQuery)
	if err != nil {
		return nil, err
	}
	return parseValopers(raw)
}

// parseValopers reads qeval's `("<lines>" string)` answer.
func parseValopers(raw string) (map[string]Valoper, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "(") || !strings.HasSuffix(raw, " string)") {
		return nil, fmt.Errorf("unexpected qeval answer: %.80q", raw)
	}
	quoted := strings.TrimSuffix(strings.TrimPrefix(raw, "("), " string)")
	s, err := strconv.Unquote(quoted)
	if err != nil {
		return nil, fmt.Errorf("unquote qeval answer: %w", err)
	}
	out := map[string]Valoper{}
	for _, line := range strings.Split(s, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 || !strings.HasPrefix(f[0], "g1") {
			continue
		}
		out[f[0]] = Valoper{Signing: f[0], Operator: f[1], Moniker: f[2]}
	}
	return out, nil
}

// valsetUpdateRe matches one line of r/sys/validators' "## Validator Updates"
// section, as gov/dao renders an executor built by it:
//
//   - g1manfred47kzduec920z88wfr64ylksmdcedlf5: add (power 4)
//   - g1…: remove
var valsetUpdateRe = regexp.MustCompile(`^-\s*(g1[0-9a-z]{38}):\s*(add|remove)\b(?:\s*\(power\s+(\d+)\))?`)

// parseValsetUpdates reads the changes out of one proposal's render. Only the
// section the executor wrote counts: an address mentioned in a description is
// not a membership change.
func parseValsetUpdates(md string) []struct {
	Addr, Op string
	Power    int64
} {
	var out []struct {
		Addr, Op string
		Power    int64
	}
	in := false
	for _, line := range strings.Split(md, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") {
			in = strings.EqualFold(strings.TrimLeft(t, "# "), "Validator Updates")
			continue
		}
		if !in {
			continue
		}
		if m := valsetUpdateRe.FindStringSubmatch(t); m != nil {
			p, _ := strconv.ParseInt(m[3], 10, 64)
			out = append(out, struct {
				Addr, Op string
				Power    int64
			}{m[1], m[2], p})
		}
	}
	return out
}

// valsetProposalScanLimit bounds how far back the scan walks. gov/dao lists
// five proposals a page, newest first, so the list alone never reaches the old
// ones: the scan reads the newest ID off page one and renders each proposal by
// ID instead. onyx-1 had 19 on 2026-10-01, mainnet 7.
const valsetProposalScanLimit = 200

// finalProposals holds renders of proposals that can no longer change. A
// proposal's body is fixed at creation and only its status moves, so once it
// is accepted or rejected the render is fetched never again.
var finalProposals sync.Map // network:id -> string

func fetchValsetProposals(ctx context.Context, network, rpcURL string) (map[string][]ValsetProposalRef, error) {
	listMD, err := fetchGovDAORender(ctx, rpcURL, store.GovDAOPathPrefix+":")
	if err != nil {
		return nil, err
	}
	newest := 0
	for _, p := range parseGovDAOProposalList(listMD) {
		if p.ID > newest {
			newest = p.ID
		}
	}
	lowest := newest - valsetProposalScanLimit + 1
	if lowest < 0 {
		lowest = 0
	}

	type result struct {
		id int
		md string
	}
	ids := make(chan int)
	results := make(chan result)
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range ids {
				key := network + ":" + strconv.Itoa(id)
				if md, ok := finalProposals.Load(key); ok {
					results <- result{id, md.(string)}
					continue
				}
				md, err := fetchGovDAORender(ctx, rpcURL, fmt.Sprintf("%s:%d", store.GovDAOPathPrefix, id))
				if err != nil {
					continue
				}
				if d := parseGovDAOProposalDetail(id, md); d.Status == "ACCEPTED" || d.Status == "REJECTED" {
					finalProposals.Store(key, md)
				}
				results <- result{id, md}
			}
		}()
	}
	go func() {
		for id := newest; id >= lowest; id-- {
			ids <- id
		}
		close(ids)
		wg.Wait()
		close(results)
	}()

	out := map[string][]ValsetProposalRef{}
	for r := range results {
		updates := parseValsetUpdates(r.md)
		if len(updates) == 0 {
			continue
		}
		d := parseGovDAOProposalDetail(r.id, r.md)
		status := d.Status
		if status == "" {
			status = "ACTIVE"
		}
		for _, u := range updates {
			out[u.Addr] = append(out[u.Addr], ValsetProposalRef{
				ID: r.id, Title: d.Title, Status: status, Op: u.Op, Power: u.Power,
			})
		}
	}
	for k := range out {
		sort.Slice(out[k], func(i, j int) bool { return out[k][i].ID > out[k][j].ID })
	}
	return out, nil
}
