package httpapi

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"testing"

	"github.com/gnoverse/gnoscope/pkg/genesis"
)

const (
	airdropVesting = "g1pku9u3jwr8k8vjpfypqzd0uhmrwr35zk0f8u7p"
	airdropPlain   = "g1j3et7juxr3npgdll3lml3mpv0y6m49rztjnf76"
	airdropMissing = "g1manfred47kzduec920z88wfr64ylksmdcedlf5"
)

func airdrop(t *testing.T, api *API, addr string) airdropResponse {
	t.Helper()
	rec, body := get(t, api.HandleAirdrop, "/api/airdrop?address="+addr)
	if rec.Code != 200 {
		t.Fatalf("%s: status %d, body %s", addr, rec.Code, body)
	}
	var out airdropResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func loadSheet(t *testing.T, api *API) {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write([]byte(airdropVesting + "=332000000000000ugnot;vesting=318720000000000ugnot,1789225200,1852383600\n" +
		airdropPlain + "=112318842569897ugnot\n"))
	w.Close()
	// The marker must name the pinned sheet, as a real import does.
	if _, err := genesis.Import(api.db, &b, genesis.SourceURL, genesis.SourceSHA256); err != nil {
		t.Fatal(err)
	}
}

func TestAirdropSaysNotLoadedBeforeTheSheetIsThere(t *testing.T) {
	api, _ := newTestAPI(t)
	got := airdrop(t, api, airdropPlain)
	// Neither "found" nor "absent": an unloaded sheet knows nothing, and
	// "not in genesis" there would tell a recipient they got nothing.
	if got.Status != genesisNotLoaded {
		t.Fatalf("status = %q, want %q", got.Status, genesisNotLoaded)
	}
}

func TestAirdropFindsAnAllocationUnderAnyPrefix(t *testing.T) {
	api, _ := newTestAPI(t)
	loadSheet(t, api)

	_, data, _ := genesis.Decode(airdropVesting)
	cosmos, _ := genesis.Encode("cosmos", data)

	for _, in := range []string{airdropVesting, cosmos} {
		got := airdrop(t, api, in)
		if got.Status != genesisFound || got.Address != airdropVesting || got.Ugnot != 332000000000000 {
			t.Errorf("%s = %+v, want the NT allocation under %s", in, got.genesisAnswer, airdropVesting)
		}
		if got.Vesting == nil || got.Vesting.End != 1852383600 || got.Vesting.Delayed {
			t.Errorf("%s vesting = %+v", in, got.Vesting)
		}
	}
	if got := airdrop(t, api, cosmos); !got.Converted {
		t.Error("a cosmos1 input was not reported as converted")
	}
	if got := airdrop(t, api, airdropVesting); got.Converted {
		t.Error("a g1 input was reported as converted")
	}

	if got := airdrop(t, api, airdropPlain); got.Status != genesisFound || got.Vesting != nil {
		t.Errorf("plain allocation = %+v, want found without vesting", got.genesisAnswer)
	}
	if got := airdrop(t, api, airdropMissing); got.Status != genesisAbsent {
		t.Errorf("status = %q, want %q for an address the sheet does not hold", got.Status, genesisAbsent)
	}
}

func TestAirdropRejectsWhatIsNotAnAddress(t *testing.T) {
	api, _ := newTestAPI(t)
	loadSheet(t, api)
	if got := airdrop(t, api, "hello"); got.Status != genesisInvalid {
		t.Errorf("status = %q, want %q", got.Status, genesisInvalid)
	}
	rec, _ := get(t, api.HandleAirdrop, "/api/airdrop")
	if rec.Code != 400 {
		t.Errorf("a missing address answered %d, want 400", rec.Code)
	}
}
