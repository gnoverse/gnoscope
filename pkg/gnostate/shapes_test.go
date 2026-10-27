package gnostate

import (
	"strings"
	"testing"
)

// bubblerumble6.json is mainnet's gno.land/r/g1leu8d2vsplhehcfkjg50mwgdpxdkt8tztu95wr/bubblerumble6,
// captured 2026-10-27 and trimmed to nine of its variables. It is the realm
// where three gaps were measured at once: every map rendered as "len 0" while
// holding 37 KB of entries, 60 pools rendered as positional tuples, and a
// slice of 32-byte hashes rendered as mojibake. Only the objects a bounded
// walk reaches are in it, so most of `pools` is deliberately unresolvable.
func TestDecodeBubbleRumble(t *testing.T) {
	f := load(t, "bubblerumble6.json")
	tree, err := DecodePackage([]byte(f.Package), f, Limits{MaxDepth: 64, MaxNodes: 1 << 20, MaxFetch: 1 << 20})
	if err != nil {
		t.Fatalf("DecodePackage: %v", err)
	}

	t.Run("maps have entries", func(t *testing.T) {
		tests := []struct {
			path, key, value string
			n                int
		}{
			// The wire shape is "List":{"List":[…]}: reading List as the array
			// is what made every one of these "len 0".
			{"teamPaid", "FR", "165572665", 43},
			{"teamSize", "", "", 43},
			{"players", "g1manfred47kzduec920z88wfr64ylksmdcedlf5", "", 100},
		}
		for _, tt := range tests {
			n := find(tree.Nodes, tt.path)
			if n == nil || n.Kind != KindMap {
				t.Fatalf("%s: %v, want a map", tt.path, n)
			}
			if len(n.Children) != tt.n {
				t.Errorf("%s: %d entries, want %d", tt.path, len(n.Children), tt.n)
			}
			if tt.key == "" {
				continue
			}
			e := find(n.Children, tt.key)
			if e == nil {
				t.Errorf("%s: no entry keyed %q", tt.path, tt.key)
				continue
			}
			if tt.value != "" && e.Value != tt.value {
				t.Errorf("%s[%q] = %q, want %q", tt.path, tt.key, e.Value, tt.value)
			}
		}
	})

	t.Run("a map value is a named struct", func(t *testing.T) {
		p := find(tree.Nodes, "players.g1manfred47kzduec920z88wfr64ylksmdcedlf5")
		if p == nil || p.Kind != KindStruct {
			t.Fatalf("players entry = %v, want a struct", p)
		}
		if p.Positional != "" {
			t.Errorf("Player fields are positional (%s)", p.Positional)
		}
		if find(p.Children, "Flag") == nil || find(p.Children, "Withdrawn") == nil {
			t.Errorf("Player fields = %s, want Flag and Withdrawn", names(p.Children))
		}
	})

	t.Run("a pool has field names", func(t *testing.T) {
		pool := find(tree.Nodes, "pools.[0]")
		if pool == nil || pool.Kind != KindStruct {
			t.Fatalf("pools[0] = %v, want a struct", pool)
		}
		if pool.Positional != "" {
			t.Fatalf("pools[0] is positional (%s)", pool.Positional)
		}
		got := names(pool.Children)
		if !strings.HasPrefix(got, "ID Name Creator Window BasePrice") {
			t.Errorf("pools[0] fields = %s", got)
		}
	})

	t.Run("a time.Time reads as an instant", func(t *testing.T) {
		// sec counts from year 1: 63926560946 - 62135596800 = 1790964146.
		at := find(tree.Nodes, "pools.[0].CreatedAt")
		if at == nil || at.Value != "2026-10-02T18:02:26Z" {
			t.Errorf("CreatedAt = %v, want 2026-10-02T18:02:26Z", at)
		}
		if z := find(tree.Nodes, "pools.[0].PoppedAt"); z == nil || z.Value != "zero time" {
			t.Errorf("PoppedAt = %v, want the zero time named as such", z)
		}
	})

	t.Run("a binary string says it lost bytes", func(t *testing.T) {
		c := find(tree.Nodes, "commits.[0]")
		if c == nil || c.Lossy != 15 {
			t.Errorf("commits[0] = %+v, want Lossy 15", c)
		}
		if a := find(tree.Nodes, "admin"); a == nil || a.Lossy != 0 {
			t.Errorf("admin = %+v, want an ordinary string with Lossy 0", a)
		}
	})

	t.Run("an unread pool says why", func(t *testing.T) {
		pools := find(tree.Nodes, "pools")
		var refs int
		for _, c := range pools.Children {
			if c.Kind == KindRef {
				refs++
				if c.Unresolved != MissNotFound || c.ObjectID == "" {
					t.Errorf("%s: unresolved %q object %q, want missing with an object id", c.Name, c.Unresolved, c.ObjectID)
				}
			}
		}
		if refs == 0 {
			t.Error("the fixture resolves every pool; it no longer exercises an unread one")
		}
	})
}

// noTypes answers objects and refuses every type, which is what a read whose
// type fetch failed looks like to the walker.
type noTypes struct{ *fixture }

func (n noTypes) Type(string) ([]byte, error) { return nil, nil }

func TestDecodePositionalSaysWhy(t *testing.T) {
	f := load(t, "bubblerumble6.json")
	tree, err := DecodePackage([]byte(f.Package), noTypes{f}, Limits{MaxDepth: 64, MaxNodes: 1 << 20, MaxFetch: 1 << 20})
	if err != nil {
		t.Fatalf("DecodePackage: %v", err)
	}
	pool := find(tree.Nodes, "pools.[0]")
	if pool == nil {
		t.Fatal("pools[0] missing")
	}
	if pool.Positional != MissNotFound {
		t.Errorf("Positional = %q, want %q", pool.Positional, MissNotFound)
	}
	if pool.Children[0].Name != "[0]" {
		t.Errorf("first field named %q without a type, want [0]", pool.Children[0].Name)
	}
	// Without names, a time.Time is three numbers, not a guessed date.
	if at := find(pool.Children, "[12]"); at == nil || at.Value != "" {
		t.Errorf("an unnamed time.Time rendered %v, want no derived value", at)
	}
}

func TestFieldNamesMismatch(t *testing.T) {
	ti := &typeInfo{id: "x.T", fields: []string{"a", "b"}}
	w := newWalker(&mapResolver{}, Limits{})
	w.types["x.T"] = ti
	if names, miss := w.fieldNames(&typeInfo{id: "x.T"}, 3); names != nil || miss != MissMismatch {
		t.Errorf("fieldNames = %v, %q; want nil, %q", names, miss, MissMismatch)
	}
	if _, miss := w.fieldNames(nil, 3); miss != MissUntyped {
		t.Errorf("no type: miss %q, want %q", miss, MissUntyped)
	}
}

func TestMapEntriesShapes(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		n    int
		ok   bool
	}{
		{"map list image", `{"List":[{"Key":{},"Value":{}},{"Key":{},"Value":{}}]}`, 2, true},
		{"bare array", `[{"Key":{},"Value":{}}]`, 1, true},
		{"absent", ``, 0, true},
		{"null", `null`, 0, true},
		{"neither", `"what"`, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := mapEntries([]byte(tt.raw))
			if ok != tt.ok || len(got) != tt.n {
				t.Errorf("mapEntries = %d entries, ok %v; want %d, %v", len(got), ok, tt.n, tt.ok)
			}
		})
	}
}

// A struct-keyed map has no text to name its entries by, so they are numbered
// and the map says so.
func TestMapStructKeysArePositional(t *testing.T) {
	pkg := `{"names":["m"],"values":[{"T":{"@type":"/gno.MapType"},"V":{"@type":"/gno.MapValue","List":{"List":[
	  {"Key":{"T":{"@type":"/gno.StructType","Fields":[{"Name":"a"}]},"V":{"@type":"/gno.StructValue","Fields":[{"T":{"@type":"/gno.PrimitiveType","value":"16"},"V":{"@type":"/gno.StringValue","value":"x"}}]}},
	   "Value":{"T":{"@type":"/gno.PrimitiveType","value":"16"},"V":{"@type":"/gno.StringValue","value":"v"}}}]}}}]}`
	tree, err := DecodePackage([]byte(pkg), nil, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	m := find(tree.Nodes, "m")
	if m == nil || m.Positional != MissKeys || len(m.Children) != 1 || m.Children[0].Name != "[0]" {
		t.Errorf("struct-keyed map = %+v", m)
	}
}

func TestTimeValue(t *testing.T) {
	prim := func(name, v string) Node { return Node{Name: name, Kind: KindPrimitive, Value: v} }
	tests := []struct {
		name   string
		fields []Node
		want   string
	}{
		{"an instant", []Node{prim("sec", "63926560946"), prim("nsec", "0")}, "2026-10-02T18:02:26Z"},
		{"with nanoseconds", []Node{prim("sec", "63926560946"), prim("nsec", "500000000")}, "2026-10-02T18:02:26.5Z"},
		{"the zero time", []Node{prim("sec", "0"), prim("nsec", "0")}, "zero time"},
		{"unnamed fields", []Node{prim("[0]", "63926560946"), prim("[1]", "0")}, ""},
		{"nsec out of range", []Node{prim("sec", "1"), prim("nsec", "1000000000")}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := timeValue(tt.fields); got != tt.want {
				t.Errorf("timeValue = %q, want %q", got, tt.want)
			}
		})
	}
}

// orderFetcher records the order of its calls, to pin the one ordering that
// decides whether a deadline-cut read is readable: types before objects.
type orderFetcher struct {
	roundFetcher
	calls []string
}

func (o *orderFetcher) Objects(oids []string) (map[string][]byte, error) {
	o.calls = append(o.calls, "objects")
	return o.roundFetcher.Objects(oids)
}

func (o *orderFetcher) Types(tids []string) (map[string][]byte, error) {
	o.calls = append(o.calls, "types:"+strings.Join(tids, ","))
	return o.roundFetcher.Types(tids)
}

// The package block names its types before any value of them is reached, so
// the read asks for every one of them in its very first round. On mainnet the
// Pool type used to be wanted only once a pool was reached, queued behind 337
// objects, and was never asked for before the deadline.
func TestPrefetchAsksForTypesFirst(t *testing.T) {
	f := load(t, "bubblerumble6.json")
	o := &orderFetcher{roundFetcher: roundFetcher{f: f}}
	if _, err := DecodePackageWith([]byte(f.Package), o, Limits{MaxDepth: 64, MaxNodes: 1 << 20, MaxFetch: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	if len(o.calls) == 0 || !strings.HasPrefix(o.calls[0], "types:") || !strings.Contains(o.calls[0], "bubblerumble6.Pool") {
		t.Errorf("first fetch = %v, want the realm's types, Pool among them", o.calls)
	}
}

func TestDecodeObjectWith(t *testing.T) {
	f := load(t, "bubblerumble6.json")
	full, err := DecodePackage([]byte(f.Package), f, Limits{MaxDepth: 64, MaxNodes: 1 << 20, MaxFetch: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	pool := find(full.Nodes, "pools.[0]")

	t.Run("a resolvable object", func(t *testing.T) {
		n, st, err := DecodeObjectWith(pool.ObjectID, pool.TypeID, &roundFetcher{f: f}, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if n.Kind != KindStruct || n.Positional != "" || find(n.Children, "Creator") == nil {
			t.Errorf("object = %s %q %s, want a named Pool", n.Kind, n.Positional, names(n.Children))
		}
		if st.Truncated {
			t.Error("a fully resolvable object reported truncated")
		}
	})

	t.Run("an object the node does not have", func(t *testing.T) {
		n, _, err := DecodeObjectWith("0000000000000000000000000000000000000000:1", "", &roundFetcher{f: f}, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if n.Kind != KindRef || n.Unresolved != MissNotFound {
			t.Errorf("object = %s %q, want a missing ref", n.Kind, n.Unresolved)
		}
	})
}

func names(ns []Node) string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.Name
	}
	return strings.Join(out, " ")
}
