package store

import (
	"testing"
	"time"
)

// Per-function gas, and the multicall exclusion that makes it honest.
//
// A transaction's gas belongs to the whole transaction, not to one message in
// it. Charging a bundled call the full bill would inflate it by however many
// messages rode along, so only single-message transactions count and the
// sample size is reported.

func gasFixture(t *testing.T) *DB {
	t.Helper()
	d := NewTestDB(t)
	d.configured = []string{"alpha"}
	ts := time.Now().UTC()
	const net, pkg = "alpha", "gno.land/r/x/y"
	if err := d.UpsertPackage(net, pkg, "y", "g1dev", "TXDEPLOY", 1, rfc3339(ts), true, 1); err != nil {
		t.Fatal(err)
	}

	// Four clean single-message calls to Cheap, with a spread so the median
	// and the p90 differ and cannot both be an accident.
	for i, gas := range []int{100, 200, 300, 1000} {
		h := "single" + string(rune('A'+i))
		if err := d.InsertCall(net, h, 10+i, 0, rfc3339(ts), "g1caller", pkg, "Cheap", "", "", true); err != nil {
			t.Fatal(err)
		}
		if err := d.UpsertTransaction(net, h, 10+i, rfc3339(ts), gas, gas*2, 1, true); err != nil {
			t.Fatal(err)
		}
	}

	// One transaction bundling three calls, billed 999,999 gas. If any of them
	// is charged the whole amount, the figures above move.
	for i, fn := range []string{"Cheap", "Bundled", "Bundled"} {
		if err := d.InsertCall(net, "multi", 50, i, rfc3339(ts), "g1caller", pkg, fn, "", "", true); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.UpsertTransaction(net, "multi", 50, rfc3339(ts), 999999, 999999, 1, true); err != nil {
		t.Fatal(err)
	}
	return d
}

func funcByName(t *testing.T, u *RealmUsage, name string) RealmFunction {
	t.Helper()
	for _, f := range u.Functions {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("function %q not in %+v", name, u.Functions)
	return RealmFunction{}
}

func TestFunctionGasExcludesMulticalls(t *testing.T) {
	d := gasFixture(t)
	u, err := d.RealmUsage("alpha", "gno.land/r/x/y", RealmUsageFilter{})
	if err != nil {
		t.Fatalf("RealmUsage: %v", err)
	}

	cheap := funcByName(t, u, "Cheap")
	// Five calls happened, but only four are in single-message transactions.
	if cheap.Calls != 5 {
		t.Errorf("calls = %d, want 5 (the bundled one still counts as a call)", cheap.Calls)
	}
	if cheap.GasSamples != 4 {
		t.Errorf("gas samples = %d, want 4: the bundled transaction was not excluded", cheap.GasSamples)
	}
	// Nearest-rank over [100 200 300 1000]: median is the 2nd, p90 the 4th.
	if cheap.GasMedian != 200 {
		t.Errorf("median = %d, want 200", cheap.GasMedian)
	}
	if cheap.GasP90 != 1000 {
		t.Errorf("p90 = %d, want 1000", cheap.GasP90)
	}
	// The 999,999 bill must not have reached any figure.
	if cheap.GasMedian > 999998 || cheap.GasP90 > 999998 {
		t.Errorf("a multicall's gas was attributed to a function: median=%d p90=%d",
			cheap.GasMedian, cheap.GasP90)
	}

	// A function seen only inside a bundle has no attributable cost at all,
	// and must report none rather than a wrong one.
	bundled := funcByName(t, u, "Bundled")
	if bundled.GasSamples != 0 || bundled.GasMedian != 0 {
		t.Errorf("a function only ever called in a bundle reported gas: samples=%d median=%d",
			bundled.GasSamples, bundled.GasMedian)
	}
	if bundled.Calls != 2 {
		t.Errorf("bundled calls = %d, want 2", bundled.Calls)
	}
}

// The failure rate was already computed and returned; this pins it so the
// promotion to the page cannot silently lose it.
func TestFunctionFailureCounts(t *testing.T) {
	d := NewTestDB(t)
	d.configured = []string{"alpha"}
	ts := time.Now().UTC()
	const net, pkg = "alpha", "gno.land/r/x/y"
	if err := d.UpsertPackage(net, pkg, "y", "g1dev", "TXDEPLOY", 1, rfc3339(ts), true, 1); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := d.InsertCall(net, "ok"+string(rune('A'+i)), 10+i, 0, rfc3339(ts), "g1a", pkg, "Flaky", "", "", true); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := d.InsertCall(net, "bad"+string(rune('A'+i)), 20+i, 0, rfc3339(ts), "g1a", pkg, "Flaky", "", "", false); err != nil {
			t.Fatal(err)
		}
	}
	u, err := d.RealmUsage(net, pkg, RealmUsageFilter{})
	if err != nil {
		t.Fatal(err)
	}
	f := funcByName(t, u, "Flaky")
	if f.Calls != 5 || f.OK != 3 || f.Failed != 2 {
		t.Errorf("calls/ok/failed = %d/%d/%d, want 5/3/2", f.Calls, f.OK, f.Failed)
	}
}

func TestPercentileInt(t *testing.T) {
	tests := []struct {
		name   string
		sorted []int
		p      float64
		want   int
	}{
		{"empty", nil, 0.5, 0},
		{"single", []int{42}, 0.5, 42},
		{"single p90", []int{42}, 0.9, 42},
		{"median of four", []int{100, 200, 300, 1000}, 0.5, 200},
		{"p90 of four", []int{100, 200, 300, 1000}, 0.9, 1000},
		{"median of five", []int{1, 2, 3, 4, 5}, 0.5, 3},
		// Nearest-rank never invents a value: every answer is one of the
		// inputs, which is what makes it a gas figure somebody actually paid.
		{"p90 of ten", []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 0.9, 9},
	}
	for _, tt := range tests {
		if got := percentileInt(tt.sorted, tt.p); got != tt.want {
			t.Errorf("%s: percentileInt(%v, %v) = %d, want %d", tt.name, tt.sorted, tt.p, got, tt.want)
		}
	}
}
