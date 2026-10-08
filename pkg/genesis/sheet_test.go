package genesis

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gnoverse/gnoscope/pkg/store"
)

// The first lines of the real sheet at SourceCommit.
const (
	lineVesting = "g1pku9u3jwr8k8vjpfypqzd0uhmrwr35zk0f8u7p=332000000000000ugnot;vesting=318720000000000ugnot,1789225200,1852383600"
	linePlain   = "g1j3et7juxr3npgdll3lml3mpv0y6m49rztjnf76=112318842569897ugnot"
	// The one delayed account in the real sheet (line 7067).
	lineDelayed = "g18c0grhdx96lw2u5t9qchl390n5weu9znkwf5vm=3837075547ugnot;vesting=1841860465ugnot,0,1820534400;type=delayed"
)

func TestParseLine(t *testing.T) {
	r, err := ParseLine(lineVesting)
	if err != nil {
		t.Fatal(err)
	}
	if r.Ugnot != 332000000000000 || !r.HasVesting || r.VestUgnot != 318720000000000 || r.VestStart != 1789225200 || r.VestEnd != 1852383600 {
		t.Errorf("vesting line = %+v", r)
	}
	p, err := ParseLine(linePlain)
	if err != nil || p.HasVesting || p.Ugnot != 112318842569897 {
		t.Errorf("plain line = %+v, %v", p, err)
	}
	d, err := ParseLine(lineDelayed)
	if err != nil || !d.VestDelayed || d.VestEnd != 1820534400 || d.VestStart != 0 {
		t.Errorf("delayed line = %+v, %v", d, err)
	}
	if r.VestDelayed {
		t.Error("a continuous schedule read as delayed")
	}
	for _, bad := range []string{linePlain + ";type=delayed", lineVesting + ";type=cliffy", "", "nonsense", "g1pku9u3jwr8k8vjpfypqzd0uhmrwr35zk0f8u7q=1ugnot", "cosmos1abc=1ugnot",
		"g1pku9u3jwr8k8vjpfypqzd0uhmrwr35zk0f8u7p=12foo", linePlain + ";vesting=1ugnot,2"} {
		if _, err := ParseLine(bad); err == nil {
			t.Errorf("ParseLine(%q) accepted a bad line", bad)
		}
	}
}

func gz(t *testing.T, lines ...string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write([]byte(strings.Join(lines, "\n") + "\n"))
	w.Close()
	return b.Bytes()
}

func TestImportThenLookup(t *testing.T) {
	db := store.NewTestDB(t)
	n, err := Import(db, bytes.NewReader(gz(t, lineVesting, linePlain)), "test", "sha")
	if err != nil || n != 2 {
		t.Fatalf("Import = %d, %v", n, err)
	}
	if _, rows, ok, _ := db.GenesisImported(); !ok || rows != 2 {
		t.Fatalf("marker rows=%d ok=%v", rows, ok)
	}
	_, data, _ := Decode("g1pku9u3jwr8k8vjpfypqzd0uhmrwr35zk0f8u7p")
	row, found, err := db.GenesisLookup(data)
	if err != nil || !found || !row.HasVesting || row.VestEnd != 1852383600 {
		t.Fatalf("lookup = %+v found=%v err=%v", row, found, err)
	}
	_, other, _ := Decode("g1manfred47kzduec920z88wfr64ylksmdcedlf5")
	if _, found, _ := db.GenesisLookup(other); found {
		t.Error("an address the sheet does not hold was found")
	}
}

// A sheet that stops parsing midway must not leave a marker: a half-loaded
// table with a marker would answer "not in genesis" for most of the chain.
func TestAFailedImportLeavesNoMarker(t *testing.T) {
	db := store.NewTestDB(t)
	if _, err := Import(db, bytes.NewReader(gz(t, lineVesting, "garbage")), "test", "sha"); err == nil {
		t.Fatal("expected an error on the bad row")
	}
	if _, _, ok, _ := db.GenesisImported(); ok {
		t.Error("a marker was written for a failed import")
	}
}

func TestFetchRefusesAWrongChecksumAndKeepsNothing(t *testing.T) {
	body := gz(t, linePlain)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
	defer srv.Close()
	dir := t.TempDir()

	sum := sha256.Sum256(body)
	good := hex.EncodeToString(sum[:])
	path, err := Fetch(context.Background(), srv.Client(), srv.URL, good, dir)
	if err != nil {
		t.Fatalf("right checksum: %v", err)
	}
	os.Remove(path)

	if _, err := Fetch(context.Background(), srv.Client(), srv.URL, strings.Repeat("0", 64), dir); err == nil {
		t.Fatal("a wrong checksum was accepted")
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("%d file(s) left behind after a rejected download", len(left))
	}
}
