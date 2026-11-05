package httpapi

import (
	"context"
	"fmt"
	"testing"
)

// The answers below are what rpc.gno.land returned for GNFT 1 on 2026-11-05,
// verbatim, except where a case says otherwise.
func gnftChain(overrides map[string]string) func(context.Context, string) (string, error) {
	const pool = "gno.land/r/gnoland/wugnot.wugnot:gno.land/r/gnoswap/gns.GNS:3000"
	answers := map[string]string{
		`gno.land/r/gnoswap/gnft.OwnerOf("1")`:                    `("g1q6d4ns7zkr492rgl0pcgf5ajaf2dlz0nnptky3" .uverse.address)` + "\n(undefined)",
		`gno.land/r/gnoswap/gnft.Name()`:                          `("GNOSWAP NFT" string)`,
		`gno.land/r/gnoswap/gnft.Symbol()`:                        `("GNFT" string)`,
		`gno.land/r/gnoswap/gnft.TokenURI("1")`:                   `("data:image/svg+xml;base64,PHN2Zz4=" string)` + "\n(undefined)",
		`gno.land/r/gnoswap/position.GetPositionPoolKey(1)`:       `("` + pool + `" string)` + "\n(undefined)",
		`gno.land/r/gnoswap/position.GetPositionTicks(1)`:         "(-9180 int32)\n(36900 int32)\n(undefined)",
		`gno.land/r/gnoswap/position.GetPositionLiquidity(1)`:     `("584708087353" string)` + "\n(undefined)",
		`gno.land/r/gnoswap/position.IsInRange(1)`:                "(true bool)\n(undefined)",
		`gno.land/r/gnoswap/position.IsBurned(1)`:                 "(false bool)\n(undefined)",
		`gno.land/r/gnoswap/position.GetPositionTokenBalances(1)`: "(196619569577 int64)\n(813384024312 int64)\n(undefined)",
		`gno.land/r/gnoswap/position.GetUnclaimedFee(1)`:          `("57269" string)` + "\n" + `("1091920" string)` + "\n(undefined)",
		`gno.land/r/gnoswap/staker.IsStaked(1)`:                   "(true bool)",
		`gno.land/r/gnoswap/staker.GetDepositOwner(1)`:            `("g1gdg8jjm56xnk4jckj3x9yuc3fp5zh2ekaq48ty" .uverse.address)` + "\n(undefined)",
		`gno.land/r/gnoswap/staker.CollectableEmissionReward(1)`:  "(859685412 int64)\n(undefined)",
		`gno.land/r/gnoswap/pool.GetSlot0Tick("` + pool + `")`:    "(13902 int32)\n(undefined)",
		`gno.land/r/gnoswap/gnft.OwnerOf("999999")`:               `("" .uverse.address)` + "\n" + `(&(struct{("invalid token id" string)} errors.errorString) *errors.errorString)`,
		`gno.land/r/x/art.OwnerOf("7")`:                           `("g1xvunup2m747gu6yr5qpmpehal5tr654cl0qtmk" .uverse.address)`,
		`gno.land/r/x/art.Name()`:                                 `("Art" string)`,
		`gno.land/r/x/art.Symbol()`:                               `("ART" string)`,
		`gno.land/r/x/art.TokenURI("7")`:                          `("https://example.com/7.json" string)`,
	}
	for k, v := range overrides {
		answers[k] = v
	}
	return func(_ context.Context, expr string) (string, error) {
		if v, ok := answers[expr]; ok {
			return v, nil
		}
		return "", fmt.Errorf("no answer for %s", expr)
	}
}

func TestReadNFT(t *testing.T) {
	meta := func(tok string) (string, int) {
		switch tok {
		case "gno.land/r/gnoland/wugnot.wugnot":
			return "WUGNOT", 6
		case "gno.land/r/gnoswap/gns.GNS":
			return "GNS", 6
		}
		return "", 0
	}
	tests := []struct {
		name       string
		collection string
		id         string
		overrides  map[string]string
		wantErr    bool
		check      func(t *testing.T, it *NFTItem)
	}{
		{
			name: "staked position resolves to its depositor", collection: gnoswapNFT, id: "1",
			check: func(t *testing.T, it *NFTItem) {
				if it.Kind != "gnoswap-position" {
					t.Fatalf("kind = %q", it.Kind)
				}
				if it.Owner != "g1gdg8jjm56xnk4jckj3x9yuc3fp5zh2ekaq48ty" {
					t.Errorf("owner = %q, want the depositor, not the staker realm", it.Owner)
				}
				if it.Custodian != "g1q6d4ns7zkr492rgl0pcgf5ajaf2dlz0nnptky3" {
					t.Errorf("custodian = %q", it.Custodian)
				}
				p := it.Position
				if p.Token0Symbol != "WUGNOT" || p.Token1Symbol != "GNS" || p.Fee != 3000 {
					t.Errorf("pool = %s/%s %d", p.Token0Symbol, p.Token1Symbol, p.Fee)
				}
				if p.TickLower != -9180 || p.TickUpper != 36900 || p.TickCurrent == nil || *p.TickCurrent != 13902 {
					t.Errorf("ticks = %d..%d now %v", p.TickLower, p.TickUpper, p.TickCurrent)
				}
				if p.InRange == nil || !*p.InRange || !p.Staked || p.Burned {
					t.Errorf("in_range=%v staked=%v burned=%v", p.InRange, p.Staked, p.Burned)
				}
				if p.Amount0 == nil || *p.Amount0 != 196619569577 || p.Fee1 != "1091920" || p.Reward == nil || *p.Reward != 859685412 {
					t.Errorf("amounts = %v %v fee1=%s reward=%v", p.Amount0, p.Amount1, p.Fee1, p.Reward)
				}
				// 1.0001^13902 is the 4.0154 pkg/price cross-checks against.
				if p.PriceNow < 4.01 || p.PriceNow > 4.02 || p.PriceLower >= p.PriceNow || p.PriceUpper <= p.PriceNow {
					t.Errorf("prices = %v < %v < %v", p.PriceLower, p.PriceNow, p.PriceUpper)
				}
				if p.PriceScale != 1 {
					t.Errorf("price scale = %v, want 1 for two 6-decimal tokens", p.PriceScale)
				}
				if len(it.Errors) != 0 {
					t.Errorf("errors = %v", it.Errors)
				}
			},
		},
		{
			name: "unstaked position keeps its owner and has no custodian", collection: gnoswapNFT, id: "1",
			overrides: map[string]string{
				`gno.land/r/gnoswap/gnft.OwnerOf("1")`:     `("g1gdg8jjm56xnk4jckj3x9yuc3fp5zh2ekaq48ty" .uverse.address)` + "\n(undefined)",
				`gno.land/r/gnoswap/staker.IsStaked(1)`:    "(false bool)",
				`gno.land/r/gnoswap/position.IsInRange(1)`: "(false bool)\n(undefined)",
			},
			check: func(t *testing.T, it *NFTItem) {
				if it.Owner != "g1gdg8jjm56xnk4jckj3x9yuc3fp5zh2ekaq48ty" || it.Custodian != "" {
					t.Errorf("owner=%q custodian=%q", it.Owner, it.Custodian)
				}
				if it.Position.Reward != nil {
					t.Errorf("an unstaked position has no reward to read")
				}
				if it.Position.InRange == nil || *it.Position.InRange {
					t.Errorf("in_range = %v, want false", it.Position.InRange)
				}
			},
		},
		{
			name: "a getter that fails is reported and the rest still answers", collection: gnoswapNFT, id: "1",
			overrides: map[string]string{
				`gno.land/r/gnoswap/position.GetUnclaimedFee(1)`: `("" string)` + "\n" + `("" string)` + "\n" + `(&(struct{("position is burned" string)} errors.errorString) *errors.errorString)`,
			},
			check: func(t *testing.T, it *NFTItem) {
				if it.Errors["GetUnclaimedFee"] != "position is burned" {
					t.Errorf("errors = %v", it.Errors)
				}
				if it.Position.Liquidity != "584708087353" {
					t.Errorf("liquidity = %q", it.Position.Liquidity)
				}
			},
		},
		{
			name: "an id that does not exist is an error, not an item owned by nobody", collection: gnoswapNFT, id: "999999",
			wantErr: true,
		},
		{
			name: "any other collection gets the generic view", collection: "gno.land/r/x/art", id: "7",
			check: func(t *testing.T, it *NFTItem) {
				if it.Kind != "grc721" || it.Position != nil {
					t.Errorf("kind=%q position=%v", it.Kind, it.Position)
				}
				if it.Name != "Art" || it.Symbol != "ART" || it.TokenURI != "https://example.com/7.json" {
					t.Errorf("meta = %q %q %q", it.Name, it.Symbol, it.TokenURI)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			it, err := readNFT(context.Background(), gnftChain(tt.overrides), tt.collection, tt.id, meta)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want an error, got %+v", it)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, it)
		})
	}
}

func TestNFTInputShape(t *testing.T) {
	tests := []struct {
		collection, id string
		ok             bool
	}{
		{"gno.land/r/gnoswap/gnft", "42", true},
		{"gno.land/r/g1nmqhha0ua730vejxqhrelldm4pga5j5ptle5ea/guardians", "1067", true},
		{"gno.land/p/nt/grc721", "1", false},
		{"gno.land/r/x/art.Name()", "1", false},
		{"gno.land/r/x/art", `1"); panic("`, false},
		{"gno.land/r/x/art", "", false},
	}
	for _, tt := range tests {
		got := nftCollectionRe.MatchString(tt.collection) && nftIDRe.MatchString(tt.id)
		if got != tt.ok {
			t.Errorf("%q %q: accepted=%v, want %v", tt.collection, tt.id, got, tt.ok)
		}
	}
}
