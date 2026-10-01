package ghlab

import (
	"database/sql"
	"fmt"
	"strconv"
	"time"
)

// Window is the span every windowed figure is counted over: [Since, Until).
//
// Three shapes. All time is the default since 2026-10-02: the walk reaches
// back to a repository's first pull request, so "who built gno" is a question
// the data answers whole, and the page opens on it. A calendar year is the
// one a reader asks next ("who was building in 2024"), and a trailing number
// of days survives for ?days=, which links and scripts already carry.
//
// Bounds are RFC3339 strings because that is how every timestamp is stored,
// and two RFC3339 UTC strings compare in the same order as the instants.
type Window struct {
	Label string `json:"label"`
	Since string `json:"since"`
	Until string `json:"until"`
	// Days is set only for a trailing window.
	Days int `json:"days,omitempty"`
}

const (
	beginning = "0001-01-01T00:00:00Z"
	forever   = "9999-12-31T23:59:59Z"
)

// AllTime is every row the store holds.
func AllTime() Window { return Window{Label: "all", Since: beginning, Until: forever} }

// Year is one calendar year, in UTC.
func Year(y int) Window {
	return Window{
		Label: strconv.Itoa(y),
		Since: fmt.Sprintf("%04d-01-01T00:00:00Z", y),
		Until: fmt.Sprintf("%04d-01-01T00:00:00Z", y+1),
	}
}

// LastDays is the trailing n days up to now. n <= 0 means 30.
func LastDays(n int) Window {
	if n <= 0 {
		n = 30
	}
	return Window{
		Label: strconv.Itoa(n) + "d",
		Since: time.Now().UTC().AddDate(0, 0, -n).Format(time.RFC3339),
		Until: forever,
		Days:  n,
	}
}

// ParseWindow reads the ?window= value: "" or "all", or a four-digit year.
func ParseWindow(s string) (Window, error) {
	if s == "" || s == "all" {
		return AllTime(), nil
	}
	y, err := strconv.Atoi(s)
	if err != nil || len(s) != 4 || y < 2000 {
		return Window{}, fmt.Errorf("window must be all or a year such as 2025, got %q", s)
	}
	return Year(y), nil
}

// IsAll says the window has no bounds, so a window figure is the all-time one.
func (w Window) IsAll() bool { return w.Since <= beginning && w.Until >= forever }

// in is the SQL condition "col falls inside the window" and its two arguments.
func (w Window) in(col string) (string, []any) {
	return col + " >= ? AND " + col + " < ?", []any{w.Since, w.Until}
}

// Years lists the calendar years that hold at least one tracked pull request,
// newest first. It is what the page draws its year pills from, so a pill
// never opens on a year with nothing in it.
func (s *Store) Years() ([]int, error) {
	out := []int{}
	err := s.each(`SELECT DISTINCT substr(created_at, 1, 4) FROM gh_tracked_prs
		WHERE created_at <> '' ORDER BY 1 DESC`, nil, func(r *sql.Rows) error {
		var y string
		if err := r.Scan(&y); err != nil {
			return err
		}
		if n, err := strconv.Atoi(y); err == nil {
			out = append(out, n)
		}
		return nil
	})
	return out, err
}
