package reqmeter

import (
	"fmt"
	"strings"
	"testing"
)

// The query text here is copied from pkg/indexer, not invented, because the
// only thing this function has to be right about is what this repo's queries
// actually look like. They are almost all anonymous with one root field, which
// is why Label reports the root field rather than insisting on an operation
// name nothing here carries.
func TestLabel(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "LatestBlockHeight, the shortest query in the repo",
			query: `{ latestBlockHeight }`,
			want:  "latestBlockHeight",
		},
		{
			name: "the endpoint probe, two root fields, first one wins",
			query: `{
		latestBlockHeight
		getBlocks(where: { height: { eq: 1 } }) { chain_id hash }
	}`,
			want: "latestBlockHeight",
		},
		{
			name:  "GetSupply, the one query with variables",
			query: `query($denom: String!) { getSupply(denom: $denom) { denom height total locked spendable } }`,
			want:  "getSupply",
		},
		{
			name: "GetRecentTransactions",
			query: `{
		getTransactions(
			where: {}
			order: { heightAndIndex: DESC }
		) { index hash success block_height }
	}`,
			want: "getTransactions",
		},
		{
			name: "the sync walk, with a height filter",
			query: `{
		getTransactions(
			where: { block_height: { gt: 4200 } messages: { value: { MsgAddPackage: {} } } }
			order: { heightAndIndex: ASC }
		) { index hash }
	}`,
			want: "getTransactions",
		},
		{
			name: "events by package path: a value is interpolated into the where clause",
			query: `{
		getTransactions(
			where: { response: { events: { GnoEvent: { pkg_path: { eq: "gno.land/r/demo/boards" } } } } }
			order: { heightAndIndex: DESC }
		) { index hash }
	}`,
			want: "getTransactions",
		},
		{
			name:  "an operation name, which nothing here writes but the spec allows",
			query: `query RecentTransactions { getTransactions(where: {}) { hash } }`,
			want:  "RecentTransactions",
		},
		{
			name:  "a mutation, same rule",
			query: `mutation Publish { publish { ok } }`,
			want:  "Publish",
		},
		{
			name:  "a comment before the query",
			query: "# what the sync loop asks for\n{ getBlocks(where: {}) { height } }",
			want:  "getBlocks",
		},
		{
			name:  "empty",
			query: ``,
			want:  UnknownLabel,
		},
		{
			name:  "not a query at all",
			query: `hello`,
			want:  UnknownLabel,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Label(tc.query); got != tc.want {
				t.Errorf("Label() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The group table is a map, and a map keyed by something an outsider writes is
// the unbounded map this package promises not to have. Several queries in this
// repo interpolate a package path or an address straight into a where clause,
// so the scanner has to know that text inside a string literal is a value and
// not query structure. gqlEscape leaves a crafted path inside the quotes; a
// naive scan would read an identifier out of the middle of it.
func TestLabelIgnoresTextInsideValues(t *testing.T) {
	hostile := []string{
		`gno.land/r/x/a", nastyField: { eq: "x`,
		`{ attackerField }`,
		`gno.land/r/x/a\" attackerField`,
		strings.Repeat("a", 500),
	}
	for _, path := range hostile {
		// The same shape pkg/indexer builds, with the value escaped the way
		// gqlEscape escapes it.
		escaped := strings.ReplaceAll(path, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, `"`, `\"`)
		q := fmt.Sprintf(`{
		getTransactions(
			where: { response: { events: { GnoEvent: { pkg_path: { eq: "%s" } } } } }
		) { hash }
	}`, escaped)
		if got := Label(q); got != "getTransactions" {
			t.Errorf("a path of %q produced the label %q, so the group map is keyed by caller input", path, got)
		}
	}
}

// Whatever the label turns out to be, it is bounded. maxGroups is the backstop
// for the map's size; this is the backstop for one key's length.
func TestLabelIsBounded(t *testing.T) {
	long := strings.Repeat("f", 4096)
	got := Label("{ " + long + " }")
	if len(got) > maxLabelLen {
		t.Fatalf("label is %d characters, want at most %d", len(got), maxLabelLen)
	}
	if got != strings.Repeat("f", maxLabelLen) {
		t.Fatalf("label = %q, want the name truncated", got)
	}
}
