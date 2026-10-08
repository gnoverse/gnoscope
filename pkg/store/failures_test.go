package store

import (
	"fmt"
	"testing"
)

func TestFailuresRanksByCountAndOnlyTheWindow(t *testing.T) {
	db := NewTestDB(t)
	c := newPulseClock()

	call := func(i int, when, path, fn string, ok bool) {
		t.Helper()
		if err := db.InsertCall("n", fmt.Sprintf("tx-%d", i), 100+i, 0, when,
			fmt.Sprintf("g1caller%d", i%2), path, fn, "", "", ok); err != nil {
			t.Fatalf("InsertCall: %v", err)
		}
	}
	// wugnot: 1 ok, 3 reverted in the window. boards: 5 ok, 1 reverted.
	call(0, c.inWindow(1), "gno.land/r/demo/wugnot", "Deposit", true)
	call(1, c.inWindow(1), "gno.land/r/demo/wugnot", "Withdraw", false)
	call(2, c.inWindow(2), "gno.land/r/demo/wugnot", "Withdraw", false)
	call(3, c.inWindow(3), "gno.land/r/demo/wugnot", "Withdraw", false)
	for i := 4; i < 9; i++ {
		call(i, c.inWindow(2), "gno.land/r/demo/boards", "Post", true)
	}
	call(9, c.inWindow(4), "gno.land/r/demo/boards", "Post", false)
	// One revert in the window before, one long ago: neither is in the window.
	call(10, c.inPrev(), "gno.land/r/demo/boards", "Post", false)
	call(11, c.ancient(), "gno.land/r/demo/boards", "Post", false)

	f, err := db.GetFailures(FailureParams{Network: "n", Since: c.since.Format("2006-01-02T15:04:05Z07:00"),
		PrevSince: c.prevSince.Format("2006-01-02T15:04:05Z07:00"), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if f.Current.Calls != 10 || f.Current.Failed != 4 {
		t.Errorf("current = %+v, want 10 calls and 4 failed", f.Current)
	}
	if !f.HasPrev || f.Prev.Failed != 1 {
		t.Errorf("prev = %+v has=%v, want the one revert in the window before", f.Prev, f.HasPrev)
	}
	if len(f.Realms) != 2 || f.Realms[0].Path != "gno.land/r/demo/wugnot" || f.Realms[0].Failed != 3 {
		t.Errorf("realms = %+v, want wugnot first with 3", f.Realms)
	}
	if len(f.Funcs) == 0 || f.Funcs[0].Func != "Withdraw" || f.Funcs[0].Failed != 3 {
		t.Errorf("funcs = %+v, want Withdraw first with 3", f.Funcs)
	}
	if len(f.Recent) != 4 {
		t.Errorf("recent = %d rows, want the 4 reverts in the window", len(f.Recent))
	}
	for i := 1; i < len(f.Recent); i++ {
		if f.Recent[i].BlockHeight > f.Recent[i-1].BlockHeight {
			t.Errorf("recent is not newest first at %d", i)
		}
	}
}
