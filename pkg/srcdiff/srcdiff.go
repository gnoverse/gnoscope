// Package srcdiff says what changed between two versions of one package's
// source: which exported declarations were added, removed or changed, and a
// line diff of every file.
//
// Both halves are computed from the files alone, with nothing but the
// standard library: go/parser for the declarations (gno is Go syntax, the
// same reason pkg/srctok uses it) and a Myers diff for the lines.
package srcdiff

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"sort"
	"strings"
)

// Version names what this package computes. An immutable diff response is
// keyed by it, so a change to what counts as an API change or how lines are
// matched gets a new key rather than an old answer.
const Version = "1"

// File is one file of one version.
type File struct {
	Name string
	Body string
}

// Decl is one exported declaration.
//
// Kind is const, var, type, func or method. Recv is the receiver type of a
// method, without the pointer. Signature is the declaration printed with no
// body and no comments, so a changed doc comment or a reordered file is not
// a change, and a changed parameter type is.
type Decl struct {
	Kind      string `json:"kind"`
	Recv      string `json:"recv,omitempty"`
	Name      string `json:"name"`
	Signature string `json:"signature"`
	File      string `json:"file"`
}

// key identifies a declaration across two versions.
func (d Decl) key() string { return d.Kind + "\x00" + d.Recv + "\x00" + d.Name }

// Changed is one declaration present in both versions with a different
// signature.
type Changed struct {
	Kind string `json:"kind"`
	Recv string `json:"recv,omitempty"`
	Name string `json:"name"`
	Old  string `json:"old"`
	New  string `json:"new"`
	File string `json:"file"`
}

// API is the exported surface's difference between two versions.
type API struct {
	Added   []Decl    `json:"added"`
	Removed []Decl    `json:"removed"`
	Changed []Changed `json:"changed"`
	// Same counts the exported declarations whose signature did not move.
	Same int `json:"same"`
	// ExportsChanged is false when no exported signature changed, which the
	// reader is told in so many words rather than left to infer from three
	// empty lists.
	ExportsChanged bool `json:"exports_changed"`
}

// isSource reports whether a file declares API: a .gno file that is not a
// test. Tests declare in their own package scope and are never callable.
func isSource(name string) bool {
	return strings.HasSuffix(name, ".gno") &&
		!strings.HasSuffix(name, "_test.gno") && !strings.HasSuffix(name, "_filetest.gno")
}

// Exports lists the exported declarations of one version: exported
// constants, variables, types and functions, and exported methods on
// exported types. A file that does not parse contributes nothing.
func Exports(files []File) []Decl {
	fset := token.NewFileSet()
	var out []Decl
	for _, f := range files {
		if !isSource(f.Name) {
			continue
		}
		// No ParseComments: a comment is never part of a signature, and
		// without them in the tree the printer cannot print one.
		parsed, err := parser.ParseFile(fset, f.Name, f.Body, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		for _, decl := range parsed.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				out = append(out, genDecls(fset, f.Name, d)...)
			case *ast.FuncDecl:
				if d.Name == nil || !d.Name.IsExported() {
					continue
				}
				cp := *d
				cp.Body = nil
				dec := Decl{Kind: "func", Name: d.Name.Name, File: f.Name}
				if d.Recv != nil && len(d.Recv.List) > 0 {
					recv := recvName(d.Recv.List[0].Type)
					if !ast.IsExported(recv) {
						continue
					}
					dec.Kind, dec.Recv = "method", recv
				}
				dec.Signature = print(fset, &cp)
				out = append(out, dec)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })
	return out
}

func genDecls(fset *token.FileSet, file string, d *ast.GenDecl) []Decl {
	var out []Decl
	switch d.Tok {
	case token.CONST, token.VAR:
		kw := d.Tok.String()
		for _, spec := range d.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if !name.IsExported() {
					continue
				}
				// One name per declaration, so `const A, b = 1, 2` reads as
				// `const A = 1` and a change to b is not a change to A.
				one := &ast.ValueSpec{Names: []*ast.Ident{name}, Type: vs.Type}
				if i < len(vs.Values) && (d.Tok == token.CONST || vs.Type == nil) {
					one.Values = []ast.Expr{vs.Values[i]}
				}
				out = append(out, Decl{Kind: kw, Name: name.Name, File: file, Signature: kw + " " + print(fset, one)})
			}
		}
	case token.TYPE:
		for _, spec := range d.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name == nil || !ts.Name.IsExported() {
				continue
			}
			cp := *ts
			cp.Type = exportedOnly(ts.Type)
			out = append(out, Decl{Kind: "type", Name: ts.Name.Name, File: file, Signature: "type " + print(fset, &cp)})
		}
	}
	return out
}

// exportedOnly drops the unexported fields of a struct and the unexported
// methods of an interface, the way godoc does: a reader of the API cannot
// name them, so renaming one is not an API change.
func exportedOnly(t ast.Expr) ast.Expr {
	switch x := t.(type) {
	case *ast.StructType:
		cp := *x
		cp.Fields = filterFields(x.Fields)
		return &cp
	case *ast.InterfaceType:
		cp := *x
		cp.Methods = filterFields(x.Methods)
		return &cp
	}
	return t
}

func filterFields(fl *ast.FieldList) *ast.FieldList {
	if fl == nil {
		return nil
	}
	out := &ast.FieldList{Opening: fl.Opening, Closing: fl.Closing}
	for _, f := range fl.List {
		if len(f.Names) == 0 {
			// Embedded: kept when the embedded type is exported.
			if ast.IsExported(recvName(f.Type)) {
				out.List = append(out.List, f)
			}
			continue
		}
		var names []*ast.Ident
		for _, n := range f.Names {
			if n.IsExported() {
				names = append(names, n)
			}
		}
		if len(names) > 0 {
			cp := *f
			cp.Names = names
			cp.Tag = nil
			out.List = append(out.List, &cp)
		}
	}
	return out
}

// recvName reads a type name off a receiver or embedded field: T, *T, T[P],
// pkg.T (the last part).
func recvName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return recvName(t.X)
	case *ast.IndexExpr:
		return recvName(t.X)
	case *ast.IndexListExpr:
		return recvName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.ParenExpr:
		return recvName(t.X)
	}
	return ""
}

// print renders a node the way gofmt would, minus comments.
func print(fset *token.FileSet, node any) string {
	var buf bytes.Buffer
	cfg := printer.Config{Mode: printer.UseSpaces | printer.TabIndent, Tabwidth: 8}
	if err := cfg.Fprint(&buf, fset, node); err != nil {
		return ""
	}
	return buf.String()
}

// same compares two signatures with whitespace collapsed, so a declaration
// reflowed across lines, or realigned by gofmt because a neighbouring field
// got longer, is the same declaration.
func same(a, b string) bool {
	return strings.Join(strings.Fields(a), " ") == strings.Join(strings.Fields(b), " ")
}

// DiffAPI compares the exported surface of two versions.
func DiffAPI(from, to []File) API {
	a := API{Added: []Decl{}, Removed: []Decl{}, Changed: []Changed{}}
	old := map[string]Decl{}
	for _, d := range Exports(from) {
		old[d.key()] = d
	}
	seen := map[string]bool{}
	for _, d := range Exports(to) {
		k := d.key()
		if seen[k] {
			continue
		}
		seen[k] = true
		o, ok := old[k]
		switch {
		case !ok:
			a.Added = append(a.Added, d)
		case !same(o.Signature, d.Signature):
			a.Changed = append(a.Changed, Changed{Kind: d.Kind, Recv: d.Recv, Name: d.Name, Old: o.Signature, New: d.Signature, File: d.File})
		default:
			a.Same++
		}
	}
	for _, d := range Exports(from) {
		if !seen[d.key()] {
			seen[d.key()] = true
			a.Removed = append(a.Removed, d)
		}
	}
	a.ExportsChanged = len(a.Added)+len(a.Removed)+len(a.Changed) > 0
	return a
}
