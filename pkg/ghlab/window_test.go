package ghlab

import "testing"

func TestParseWindow(t *testing.T) {
	for _, tc := range []struct {
		in, label string
		ok        bool
	}{
		{"", "all", true},
		{"all", "all", true},
		{"2025", "2025", true},
		{"25", "", false},
		{"1999", "", false},
		{"20251", "", false},
		{"last", "", false},
	} {
		w, err := ParseWindow(tc.in)
		if (err == nil) != tc.ok || (tc.ok && w.Label != tc.label) {
			t.Errorf("ParseWindow(%q) = %+v, %v; want label %q ok=%v", tc.in, w, err, tc.label, tc.ok)
		}
	}
	if !AllTime().IsAll() || Year(2025).IsAll() || LastDays(30).IsAll() {
		t.Error("only AllTime is unbounded")
	}
}

// TestYearWindowIsBoundedOnBothSides is the bug a year adds that a trailing
// window could not have: a trailing window ends now, so a lower bound was the
// whole condition. A year also ends, and a query that forgets the upper bound
// counts 2025's work as 2024's and the year pills all show the same table.
func TestYearWindowIsBoundedOnBothSides(t *testing.T) {
	s := openTracked(t)
	addRepo(t, s, "gnolang/gno", "seed", true)
	pr := func(n int, author, created, merged string) PR {
		return PR{FullName: "gnolang/gno", Number: n, Author: author, State: "closed",
			CreatedAt: created, UpdatedAt: merged, MergedAt: merged}
	}
	if err := s.UpsertPRs([]PR{
		pr(1, "old", "2024-03-01T10:00:00Z", "2024-03-01T12:00:00Z"),
		pr(2, "old", "2024-12-31T23:00:00Z", "2024-12-31T23:30:00Z"),
		// Opened on the last evening of 2024, merged in 2025: opened is a
		// 2024 fact, merged a 2025 one.
		pr(3, "late", "2024-12-31T22:00:00Z", "2025-01-01T02:00:00Z"),
		pr(4, "late", "2025-06-01T00:00:00Z", "2025-06-02T00:00:00Z"),
		pr(5, "late", "2025-07-01T00:00:00Z", "2025-07-02T00:00:00Z"),
	}); err != nil {
		t.Fatal(err)
	}

	years, err := s.Years()
	if err != nil {
		t.Fatal(err)
	}
	if len(years) != 2 || years[0] != 2025 || years[1] != 2024 {
		t.Fatalf("years = %v, want [2025 2024]", years)
	}

	w24, err := s.windowStats(Year(2024))
	if err != nil {
		t.Fatal(err)
	}
	if w24.PRsOpened != 3 || w24.PRsMerged != 2 || w24.Authors != 2 {
		t.Errorf("2024 opened/merged/authors = %d/%d/%d, want 3/2/2", w24.PRsOpened, w24.PRsMerged, w24.Authors)
	}
	if w24.NewContributors != 1 {
		t.Errorf("2024 new = %d, want 1 (late's first merge is in 2025)", w24.NewContributors)
	}

	top24, err := s.TopContributors(Year(2024), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(top24) != 1 || top24[0].Login != "old" || top24[0].WindowScore != 2 {
		t.Fatalf("2024 top = %+v, want only old, with 2", top24)
	}

	top25, err := s.TopContributors(Year(2025), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(top25) != 1 || top25[0].Login != "late" || top25[0].RecentMerged != 3 {
		t.Fatalf("2025 top = %+v, want only late, with 3 merges", top25)
	}

	all, err := s.TopContributors(AllTime(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Login != "late" || all[1].Login != "old" {
		t.Fatalf("all-time top = %+v, want late then old", all)
	}

	recent, err := s.RecentPRs(Year(2024), 10, "merged")
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 {
		t.Errorf("2024 merged = %d rows, want 2", len(recent))
	}
}
