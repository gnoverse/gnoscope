package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Validator uptime: how many of the last N blocks each validator signed.
//
// A block's last_commit carries the precommits that sealed the block before it:
// one slot per validator, null where that validator did not sign. Counting the
// non-null ones is participation, which is what "was this validator doing its
// job" means. The columns gnockpit supplies (missed_100) say the same for
// mainnet only and come from a third party; this reads the chain.
//
// The denominator is the blocks actually read, never the blocks asked for: a
// block the node would not serve is not a block the validator missed.

// ValidatorUptime is one validator's record over the window.
type ValidatorUptime struct {
	Address string `json:"address"`
	Name    string `json:"name,omitempty"`
	// Signed and Missed add up to the blocks read.
	Signed int `json:"signed"`
	Missed int `json:"missed"`
	// Uptime is Signed over the blocks read, 0..1.
	Uptime float64 `json:"uptime"`
}

type validatorUptimeResponse struct {
	Network string `json:"network"`
	// Requested is how many blocks were asked for, Read how many came back.
	Requested  int               `json:"requested"`
	Read       int               `json:"read"`
	FromHeight int64             `json:"from_height,omitempty"`
	ToHeight   int64             `json:"to_height,omitempty"`
	Validators []ValidatorUptime `json:"validators"`
	// Note says what the figure cannot tell you.
	Note string `json:"note"`
}

const (
	uptimeDefaultBlocks = 100
	uptimeMaxBlocks     = 200
	uptimeCacheTTL      = 30 * time.Second
	uptimeConcurrency   = 8
)

var uptimeCache = newMemo[validatorUptimeResponse](uptimeCacheTTL, 5*time.Second)

// blockSigners reads one block's commit: the validators that signed the block
// before it. ok is false when the block could not be read at all.
func fetchRPCBlockSigners(ctx context.Context, rpcURL string, height int64) (map[string]bool, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", rpcURL+"/block?height="+strconv.FormatInt(height, 10), nil)
	if err != nil {
		return nil, err
	}
	resp, err := abciClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rpc %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var out struct {
		Error  *struct{ Message string } `json:"error"`
		Result struct {
			Block struct {
				LastCommit struct {
					Precommits []*struct {
						ValidatorAddress string `json:"validator_address"`
					} `json:"precommits"`
				} `json:"last_commit"`
			} `json:"block"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	if out.Error != nil {
		return nil, fmt.Errorf("rpc: %s", out.Error.Message)
	}
	signers := map[string]bool{}
	for _, p := range out.Result.Block.LastCommit.Precommits {
		if p != nil && p.ValidatorAddress != "" {
			signers[p.ValidatorAddress] = true
		}
	}
	return signers, nil
}

// tallyUptime counts, for each member, the blocks it signed among those read.
// blocks holds one signer set per block read. Sorted worst first, because the
// question the page answers is who is not keeping up.
func tallyUptime(members []ValsetMember, blocks []map[string]bool) []ValidatorUptime {
	out := make([]ValidatorUptime, 0, len(members))
	for _, m := range members {
		u := ValidatorUptime{Address: m.Address, Name: m.Name}
		for _, signers := range blocks {
			if signers[m.Address] {
				u.Signed++
			} else {
				u.Missed++
			}
		}
		if n := u.Signed + u.Missed; n > 0 {
			u.Uptime = float64(u.Signed) / float64(n)
		}
		out = append(out, u)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Uptime != out[j].Uptime {
			return out[i].Uptime < out[j].Uptime
		}
		return out[i].Address < out[j].Address
	})
	return out
}

// HandleValidatorUptime serves /api/validators/uptime?blocks=N.
func (a *API) HandleValidatorUptime(w http.ResponseWriter, r *http.Request) {
	network := a.singleNetwork(r)
	if network == "" {
		jsonError(w, "no network configured", 404)
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("blocks"))
	if n <= 0 {
		n = uptimeDefaultBlocks
	}
	n = min(n, uptimeMaxBlocks)
	JSONResponse(w, a.validatorUptime(r.Context(), network, n))
}

// validatorUptime reads the uptime of a network's validators over its last n
// blocks. Shared by the endpoint and by the alert rule that watches it.
//
// Nothing to read is an answer, not a failure: the page draws a dash for it,
// and a 5xx would be logged by the browser as an error on every load of
// /validators for a network whose node is not reachable.
func (a *API) validatorUptime(ctx context.Context, network string, n int) validatorUptimeResponse {
	unread := func(why string) validatorUptimeResponse {
		return validatorUptimeResponse{Network: network, Requested: n, Validators: []ValidatorUptime{}, Note: why}
	}
	rpcURL := a.rpcURLFor(network)
	if rpcURL == "" {
		return unread("no verified RPC for this network, so no block commits were read")
	}
	vs := a.FetchValset(ctx, network)
	if len(vs.Members) == 0 || vs.Height == 0 {
		return unread("the validator set could not be read, so no block commits were read")
	}

	return uptimeCache.get(ctx, network+"|"+strconv.Itoa(n), func(ctx context.Context) (validatorUptimeResponse, bool) {
		to := vs.Height
		from := max(to-int64(n)+1, 2)

		sets := make([]map[string]bool, to-from+1)
		var wg sync.WaitGroup
		sem := make(chan struct{}, uptimeConcurrency)
		for h := from; h <= to; h++ {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int, h int64) {
				defer wg.Done()
				defer func() { <-sem }()
				if s, err := fetchRPCBlockSigners(ctx, rpcURL, h); err == nil {
					sets[i] = s
				}
			}(int(h-from), h)
		}
		wg.Wait()

		read := make([]map[string]bool, 0, len(sets))
		for _, s := range sets {
			if s != nil {
				read = append(read, s)
			}
		}
		out := validatorUptimeResponse{
			Network: network, Requested: n, Read: len(read), FromHeight: from, ToHeight: to,
			Validators: tallyUptime(vs.Members, read),
			Note:       "signing in the block's commit, read from the node. A validator that joined inside the window counts the blocks before it joined as missed.",
		}
		// A window where most blocks failed to read is not an answer worth
		// holding for the full TTL.
		return out, len(read)*2 >= len(sets)
	})
}
