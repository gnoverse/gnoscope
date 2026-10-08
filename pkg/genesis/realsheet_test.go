package genesis

import (
	"os"
	"testing"

	"github.com/gnoverse/gnoscope/pkg/store"
)

// TestRealSheet imports the actual sheet when GNOSCOPE_REAL_SHEET names a local
// copy of balances.txt.gz, and checks it against the figures the sheet's own
// README publishes. Skipped otherwise: the file is 57MB.
func TestRealSheet(t *testing.T) {
	path := os.Getenv("GNOSCOPE_REAL_SHEET")
	if path == "" {
		t.Skip("set GNOSCOPE_REAL_SHEET to a local balances.txt.gz")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	db := store.NewTestDB(t)
	n, err := Import(db, f, "local", SourceSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if n != SourceRows {
		t.Errorf("rows = %d, want %d", n, SourceRows)
	}
	rows, total, err := db.GenesisTotals()
	if err != nil {
		t.Fatal(err)
	}
	if rows != SourceRows {
		t.Errorf("stored rows = %d, want %d", rows, SourceRows)
	}
	if total != 1332999998328067 {
		t.Errorf("total = %d ugnot, want 1332999998328067 (the sheet's README)", total)
	}

	// The one delayed account (line 7067 of the sheet) must come back delayed.
	_, data, _ := Decode("g18c0grhdx96lw2u5t9qchl390n5weu9znkwf5vm")
	row, found, err := db.GenesisLookup(data)
	if err != nil || !found || !row.VestDelayed || row.VestEnd != 1820534400 {
		t.Errorf("delayed account = %+v found=%v err=%v", row, found, err)
	}
}
