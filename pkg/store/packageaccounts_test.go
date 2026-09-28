package store

import (
	"testing"

	"github.com/moul/mygnoscan/pkg/config"
	"github.com/moul/mygnoscan/pkg/gnoaddr"
)

// The three mainnet addresses this file pins are not illustrative. They are the
// answers the derivation has to keep producing, checked against the live chain
// on 2026-09-28, and they are what makes a rewrite of gnoaddr visible here
// rather than in production.
const (
	gnoswapPool     = "gno.land/r/gnoswap/pool"
	gnoswapPoolAddr = "g1dexaf6aqkkyr9yfy9d5up69lsn7ra80af34g5v"
)

func seedPkgAccount(t *testing.T, db *DB, network, path string) {
	t.Helper()
	if err := db.UpsertPackage(network, path, "x", "g1creator", "TX-"+path, 10,
		"2026-01-01T00:00:00Z", true, 1); err != nil {
		t.Fatalf("UpsertPackage(%s): %v", path, err)
	}
}

// UpsertPackage has to write the reverse index as it goes. If it does not, the
// table is only as fresh as the last rollup tick, and a realm deployed a minute
// ago reads as an anonymous hash on the page somebody opened to look at it.
func TestUpsertPackageIndexesItsAccounts(t *testing.T) {
	db := NewTestDB(t)
	seedPkgAccount(t, db, "alpha", gnoswapPool)

	tests := []struct {
		name        string
		address     string
		wantFound   bool
		wantPath    string
		wantDeposit bool
	}{
		{
			name:      "the banker, pinned to the live mainnet address",
			address:   gnoswapPoolAddr,
			wantFound: true,
			wantPath:  gnoswapPool,
		},
		{
			name:        "the storage deposit account is a different row",
			address:     gnoaddr.DeriveStorageDeposit(gnoswapPool),
			wantFound:   true,
			wantPath:    gnoswapPool,
			wantDeposit: true,
		},
		{
			name:    "an address belonging to no package",
			address: "g1manfred47kzduec920z88wfr64ylksmdcedlf5",
		},
		{
			name:    "the empty address is not a lookup",
			address: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := db.LookupPackageAccount("alpha", tt.address)
			if err != nil {
				t.Fatalf("LookupPackageAccount: %v", err)
			}
			if ok != tt.wantFound {
				t.Fatalf("found = %v, want %v (got %+v)", ok, tt.wantFound, got)
			}
			if !ok {
				return
			}
			if got.Path != tt.wantPath {
				t.Errorf("path = %q, want %q", got.Path, tt.wantPath)
			}
			if got.Deposit != tt.wantDeposit {
				t.Errorf("deposit = %v, want %v", got.Deposit, tt.wantDeposit)
			}
		})
	}
}

// The banker and the deposit account must never collapse into one row. They
// hold different money, and attributing a deposit refund to a realm's treasury
// is the specific error the deposit column exists to prevent.
func TestPackageAccountsAreTwoDistinctRows(t *testing.T) {
	db := NewTestDB(t)
	seedPkgAccount(t, db, "alpha", gnoswapPool)

	banker, deposit := gnoaddr.Derive(gnoswapPool), gnoaddr.DeriveStorageDeposit(gnoswapPool)
	if banker == deposit {
		t.Fatalf("derivation collapsed: both accounts are %s", banker)
	}
	n, err := db.PackageAccountCount("alpha")
	if err != nil {
		t.Fatalf("PackageAccountCount: %v", err)
	}
	if n != 2 {
		t.Fatalf("indexed %d accounts for one package, want 2", n)
	}
}

// A run path's "address" is the caller's own, so indexing one puts a realm path
// on a human's account. That is the worst answer this index can give, because
// it is confidently wrong rather than absent.
func TestRunPathIsNotIndexed(t *testing.T) {
	db := NewTestDB(t)
	const caller = "g1manfred47kzduec920z88wfr64ylksmdcedlf5"
	seedPkgAccount(t, db, "alpha", "gno.land/e/"+caller+"/run")

	if _, ok, err := db.LookupPackageAccount("alpha", caller); err != nil {
		t.Fatalf("LookupPackageAccount: %v", err)
	} else if ok {
		t.Fatal("a run path labelled its caller's own address as a package account")
	}
	if n, err := db.PackageAccountCount("alpha"); err != nil {
		t.Fatalf("PackageAccountCount: %v", err)
	} else if n != 0 {
		t.Fatalf("indexed %d accounts for a run path, want 0", n)
	}
}

// The backfill is what fills the table on a database built before it existed,
// so it has to work against rows that were never seen by UpsertPackage.
func TestRefreshPackageAccountsBackfillsExistingRows(t *testing.T) {
	db := NewTestDB(t)
	seedPkgAccount(t, db, "alpha", gnoswapPool)

	// Simulate the pre-existing database: the package is there, the index is
	// not. Deleting is the only honest way to reach that state.
	if _, err := db.db.Exec(`DELETE FROM package_accounts`); err != nil {
		t.Fatalf("clear index: %v", err)
	}
	if n, _ := db.PackageAccountCount("alpha"); n != 0 {
		t.Fatalf("index not cleared: %d rows", n)
	}

	got, err := db.RefreshPackageAccounts()
	if err != nil {
		t.Fatalf("RefreshPackageAccounts: %v", err)
	}
	if got != 1 {
		t.Errorf("processed %d paths, want 1", got)
	}
	if _, ok, err := db.LookupPackageAccount("alpha", gnoswapPoolAddr); err != nil || !ok {
		t.Fatalf("backfill did not restore the banker row (ok=%v err=%v)", ok, err)
	}
}

// Two chains derive the same address for the same path, so the network column
// is the only thing keeping them apart. Without it a lookup on one chain would
// answer with the other's row.
func TestPackageAccountsAreScopedByNetwork(t *testing.T) {
	db := NewTestDB(t)
	db.SetConfiguredNetworks([]config.NetworkConfig{{ID: "alpha"}, {ID: "beta"}})
	seedPkgAccount(t, db, "alpha", gnoswapPool)
	seedPkgAccount(t, db, "beta", "gno.land/r/other/thing")

	if _, ok, err := db.LookupPackageAccount("beta", gnoswapPoolAddr); err != nil {
		t.Fatalf("LookupPackageAccount: %v", err)
	} else if ok {
		t.Error("alpha's package resolved on beta")
	}
	if _, ok, err := db.LookupPackageAccount("alpha", gnoswapPoolAddr); err != nil || !ok {
		t.Fatalf("alpha's own package did not resolve (ok=%v err=%v)", ok, err)
	}
}

// The labels map is what every list endpoint reads, and the two kinds of
// account have to read differently there: "gno.land/r/gnoswap/pool" and
// "gno.land/r/gnoswap/pool (storage deposit)" are two accounts, and a row that
// says the first when it means the second misattributes the money.
func TestPackageAccountLabelsDistinguishTheDeposit(t *testing.T) {
	db := NewTestDB(t)
	seedPkgAccount(t, db, "alpha", gnoswapPool)

	labels, err := db.PackageAccountLabels("alpha")
	if err != nil {
		t.Fatalf("PackageAccountLabels: %v", err)
	}
	banker, ok := labels[gnoswapPoolAddr]
	if !ok {
		t.Fatalf("no label for the banker; got %d labels", len(labels))
	}
	if banker.Label != gnoswapPool {
		t.Errorf("banker label = %q, want %q", banker.Label, gnoswapPool)
	}
	if banker.Kind != "derived" {
		t.Errorf("banker kind = %q, want derived", banker.Kind)
	}
	deposit, ok := labels[gnoaddr.DeriveStorageDeposit(gnoswapPool)]
	if !ok {
		t.Fatal("no label for the storage deposit account")
	}
	if deposit.Label == banker.Label {
		t.Errorf("the deposit account reads as the treasury: both are %q", deposit.Label)
	}
}
