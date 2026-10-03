// Package srctok splits gno source into per-line, classified segments for the
// source viewer.
//
// It exists so the browser does not have to guess. The frontend used to colour
// source with a handful of regular expressions per line, which cannot see a
// raw string or a block comment that spans lines, cannot tell a call from a
// conversion, and cannot know which names the package itself declares. The
// server has the whole package and the Go toolchain's own scanner and parser,
// so it answers those questions once per stamp, and the answer is cached
// forever alongside the body it was computed from.
//
// Lexing is go/scanner and the declaration/reference pass is go/parser: gno is
// Go syntax, the standard library ships both, and they add nothing to the
// module graph. A general-purpose highlighter (chroma) was measured as the
// alternative: +5.7 MB of binary for a regex lexer that knows less about Go
// than go/scanner does.
//
// The one guarantee every caller leans on: for every line, the segments'
// texts concatenated are exactly that line of the input, byte for byte. A
// segment is a classification of bytes, never a rewrite of them, so a
// renderer that drops the classes still shows the right source.
package srctok

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"sort"
	"strconv"
	"strings"
)

// Version names the tokenizer's output. It is part of the request (tokens=1),
// so the URL of a response changes whenever its meaning does: responses are
// cached as immutable, and a client holding version 1 must never be handed
// what version 2 would have said under the same URL, nor the reverse.
const Version = "1"

// The closed set of classes. Anything not in it is plain text, which a
// segment encodes as a bare JSON string rather than a pair.
const (
	Keyword = "kw"      // func, return, if, ...
	String  = "str"     // string and rune literals
	Comment = "com"     // line and block comments
	Number  = "num"     // int, float and imaginary literals
	Func    = "fn"      // the name being called, or a method selected and called
	Type    = "type"    // a predeclared type: int, string, address, realm, ...
	Op      = "op"      // operators and delimiters
	Ident   = "ident"   // any other identifier
	Builtin = "builtin" // a predeclared function or constant: len, cross, nil, ...
	Pkg     = "pkg"     // an imported package's name, where it qualifies a selector
	Import  = "imp"     // an import path under gno.land/p/ or gno.land/r/
	Decl    = "decl"    // the name in a top-level declaration of this package
	Ref     = "ref"     // a use of one of this package's top-level declarations
)

// Gno's universe, from gnovm's uverse.go, which is a subset of Go's plus
// address, realm and the cross-realm builtins. A name only gets one of these
// classes when nothing in scope shadows it.
var (
	predeclaredTypes = set("address", "any", "bool", "byte", "error",
		"float32", "float64", "int", "int8", "int16", "int32", "int64",
		"realm", "rune", "string", "uint", "uint8", "uint16", "uint32", "uint64")
	predeclaredFuncs = set("append", "attach", "cap", "copy", "cross", "crossing",
		"delete", "istypednil", "len", "make", "new", "panic", "print", "println",
		"recover", "revive", "true", "false", "nil", "iota")
)

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// Segment is a run of bytes on one line and what they are. Class is empty for
// plain text. Key is set only on a Decl whose name alone does not identify it
// (a method: "Type.Method"); everywhere else the text is the key.
type Segment struct {
	Class string
	Text  string
	Key   string
}

// MarshalJSON writes plain text as a bare string and anything classified as
// [class, text] or [class, text, key]. Most of a file is whitespace and
// punctuation, and this halves what it costs on the wire.
func (s Segment) MarshalJSON() ([]byte, error) {
	switch {
	case s.Class == "":
		return json.Marshal(s.Text)
	case s.Key != "":
		return json.Marshal([3]string{s.Class, s.Text, s.Key})
	default:
		return json.Marshal([2]string{s.Class, s.Text})
	}
}

// Location is where a top-level declaration's name is: file and 1-based line.
// It marshals as [file, line].
type Location struct {
	File string
	Line int
}

// MarshalJSON writes [file, line].
func (l Location) MarshalJSON() ([]byte, error) {
	return json.Marshal([2]any{l.File, l.Line})
}

// File is one file of a package.
type File struct {
	Name string
	Body string
}

// Index is a package read once, answering per-file tokenization from it.
// Building it parses every .gno file, because whether a name refers to a
// package-level declaration depends on the files beside it.
type Index struct {
	files map[string]*fileInfo
	// Decls maps every top-level name of the package to where it is
	// declared: plain names for funcs, types, vars and consts, and
	// "Type.Method" for methods. init and _ are left out, since neither can
	// be referred to and init may be declared any number of times.
	Decls map[string]Location
}

type fileInfo struct {
	body   string
	isGno  bool
	ast    *ast.File
	fset   *token.FileSet
	defs   map[int]string // byte offset of a top-level name -> its key
	marks  map[int]string // byte offset of an identifier -> its class
	impSet map[int]bool   // byte offset of an import path literal under gno.land/p|r
}

// NewIndex parses every .gno file in files. Files that do not parse still get
// lexed; they just contribute what the parser managed to recover.
func NewIndex(files []File) *Index {
	ix := &Index{files: map[string]*fileInfo{}, Decls: map[string]Location{}}
	// Sorted, so which file "wins" a name declared twice (a compile error,
	// but on-chain source is whatever its deployer sent) is deterministic,
	// and so is the response built from it.
	sorted := append([]File(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, f := range sorted {
		fi := &fileInfo{body: f.Body, isGno: strings.HasSuffix(f.Name, ".gno")}
		ix.files[f.Name] = fi
		if !fi.isGno {
			continue
		}
		fi.fset = token.NewFileSet()
		// A partial AST on a syntax error is exactly what is wanted: the
		// declarations before the error are still declarations.
		fi.ast, _ = parser.ParseFile(fi.fset, f.Name, f.Body, 0)
		fi.defs = topLevelDefs(fi)
		for off, key := range fi.defs {
			if _, dup := ix.Decls[key]; dup {
				continue
			}
			ix.Decls[key] = Location{File: f.Name, Line: lineAt(f.Body, off)}
		}
	}
	for _, fi := range ix.files {
		if fi.isGno {
			fi.marks, fi.impSet = ix.classifyIdents(fi)
		}
	}
	return ix
}

// lineAt is the 1-based line of byte offset off.
func lineAt(body string, off int) int {
	if off > len(body) {
		off = len(body)
	}
	return strings.Count(body[:off], "\n") + 1
}

func offsetOf(fi *fileInfo, p token.Pos) int {
	return fi.fset.Position(p).Offset
}

// topLevelDefs finds the name of every top-level declaration in one file.
func topLevelDefs(fi *fileInfo) map[int]string {
	defs := map[int]string{}
	if fi.ast == nil {
		return defs
	}
	add := func(id *ast.Ident, key string) {
		if id == nil || id.Name == "_" || id.Name == "init" || !id.Pos().IsValid() {
			return
		}
		defs[offsetOf(fi, id.Pos())] = key
	}
	for _, d := range fi.ast.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv != nil && len(d.Recv.List) > 0 {
				if recv := recvTypeName(d.Recv.List[0].Type); recv != "" {
					add(d.Name, recv+"."+d.Name.Name)
				}
				continue
			}
			add(d.Name, d.Name.Name)
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					add(s.Name, s.Name.Name)
				case *ast.ValueSpec:
					for _, n := range s.Names {
						add(n, n.Name)
					}
				}
			}
		}
	}
	return defs
}

// recvTypeName is T for a receiver of type T, *T, T[X] or *T[X].
func recvTypeName(e ast.Expr) string {
	for {
		switch t := e.(type) {
		case *ast.StarExpr:
			e = t.X
		case *ast.ParenExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.IndexListExpr:
			e = t.X
		case *ast.Ident:
			return t.Name
		default:
			return ""
		}
	}
}

// classifyIdents decides, for the identifiers that need the parse to tell,
// what each one is. Everything it does not mark falls back to what the
// scanner alone can say.
func (ix *Index) classifyIdents(fi *fileInfo) (map[int]string, map[int]bool) {
	marks, imps := map[int]string{}, map[int]bool{}
	if fi.ast == nil {
		return marks, imps
	}

	// Names this file imports, as they appear before a dot.
	pkgNames := map[string]bool{}
	for _, spec := range fi.ast.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		if strings.HasPrefix(path, "gno.land/p/") || strings.HasPrefix(path, "gno.land/r/") {
			imps[offsetOf(fi, spec.Path.Pos())] = true
		}
		switch {
		case spec.Name != nil && spec.Name.Name != "_" && spec.Name.Name != ".":
			pkgNames[spec.Name.Name] = true
			marks[offsetOf(fi, spec.Name.Pos())] = Pkg
		case spec.Name == nil:
			pkgNames[importName(path)] = true
		}
	}

	// Identifiers that are not free names: a field picked by a selector, a
	// key in a composite literal, a struct field or interface method being
	// declared. None of them can refer to a package-level declaration, and
	// marking one that happens to share a name with one would link it to the
	// wrong thing.
	notFree := map[*ast.Ident]bool{}
	ast.Inspect(fi.ast, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			notFree[n.Sel] = true
			if x, ok := n.X.(*ast.Ident); ok && x.Obj == nil && pkgNames[x.Name] && !ix.isPackageName(x.Name) {
				marks[offsetOf(fi, x.Pos())] = Pkg
			}
		case *ast.CompositeLit:
			for _, e := range n.Elts {
				if kv, ok := e.(*ast.KeyValueExpr); ok {
					if k, ok := kv.Key.(*ast.Ident); ok {
						notFree[k] = true
					}
				}
			}
		case *ast.Field:
			for _, name := range n.Names {
				notFree[name] = true
			}
		case *ast.FuncDecl:
			// A method's name is declared on its type, not in the package
			// scope, so it is never a free name (topLevelDefs still keys it).
			if n.Recv != nil {
				notFree[n.Name] = true
			}
		case *ast.LabeledStmt:
			notFree[n.Label] = true
		case *ast.BranchStmt:
			if n.Label != nil {
				notFree[n.Label] = true
			}
		case *ast.CallExpr:
			switch f := n.Fun.(type) {
			case *ast.Ident:
				marks[offsetOf(fi, f.Pos())] = Func
			case *ast.SelectorExpr:
				marks[offsetOf(fi, f.Sel.Pos())] = Func
			}
		}
		return true
	})

	ast.Inspect(fi.ast, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok || !id.Pos().IsValid() {
			return true
		}
		off := offsetOf(fi, id.Pos())
		if _, isDef := fi.defs[off]; isDef {
			marks[off] = Decl
			return true
		}
		if notFree[id] {
			return true
		}
		if marks[off] == Pkg {
			return true
		}
		if id.Obj != nil {
			// Resolved inside this file. Only a top-level declaration of
			// this file is a reference; anything else is a local.
			if declID := objIdent(id.Obj); declID != nil && declID.Pos().IsValid() {
				if _, top := fi.defs[offsetOf(fi, declID.Pos())]; top {
					marks[off] = Ref
				}
			}
			return true
		}
		// Unresolved: either declared in another file of the package, or a
		// universe name, or an import (handled above).
		switch {
		case ix.isPackageName(id.Name):
			marks[off] = Ref
		case predeclaredTypes[id.Name]:
			marks[off] = Type
		case predeclaredFuncs[id.Name]:
			marks[off] = Builtin
		}
		return true
	})
	return marks, imps
}

// isPackageName reports whether name is a top-level declaration of the
// package (methods excluded: they are never referred to unqualified).
func (ix *Index) isPackageName(name string) bool {
	_, ok := ix.Decls[name]
	return ok && !strings.Contains(name, ".")
}

// objIdent is the identifier that declared obj, where go/parser recorded one.
//
// ast.Object is deprecated in favour of go/types, which would need every
// import type-checked: gno's stdlibs and other on-chain packages, none of
// which this process has. The parser's syntactic resolution is what is
// available, and its known blind spot (a composite literal key) is handled
// by the caller.
func objIdent(obj *ast.Object) *ast.Ident { //nolint:staticcheck // see above
	switch d := obj.Decl.(type) {
	case *ast.ValueSpec:
		for _, n := range d.Names {
			if n.Name == obj.Name {
				return n
			}
		}
	case *ast.TypeSpec:
		return d.Name
	case *ast.FuncDecl:
		return d.Name
	}
	return nil
}

// importName guesses the name an import is used under: its last element,
// or the one before a /vN suffix. Gno packages follow the Go convention, and
// a wrong guess only costs the pkg colour, never a link.
func importName(path string) string {
	parts := strings.Split(path, "/")
	last := parts[len(parts)-1]
	if len(parts) > 1 && len(last) > 1 && last[0] == 'v' && strings.Trim(last[1:], "0123456789") == "" {
		return parts[len(parts)-2]
	}
	return last
}

// Tokenize returns one slice of segments per line of the named file, where a
// line is what strings.Split(body, "\n") would give: the same lines the
// frontend draws, and the same count. ok is false when the package has no
// file by that name. A file that is not .gno comes back unclassified.
func (ix *Index) Tokenize(name string) (lines [][]Segment, ok bool) {
	fi, ok := ix.files[name]
	if !ok {
		return nil, false
	}
	b := &lineBuilder{lines: [][]Segment{{}}}
	if !fi.isGno {
		b.emit("", "", fi.body)
		return b.done(), true
	}
	src := fi.body
	var s scanner.Scanner
	fset := token.NewFileSet()
	file := fset.AddFile(name, -1, len(src))
	// The error handler is set so the scanner keeps going past bad input
	// rather than counting silently: an unterminated string or a stray byte
	// is still drawn, just not coloured.
	s.Init(file, []byte(src), func(token.Position, string) {}, scanner.ScanComments)

	cursor := 0
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		// Automatic semicolons are not in the source.
		if tok == token.SEMICOLON && lit != ";" {
			continue
		}
		start := file.Offset(pos)
		if start < cursor || start >= len(src) {
			continue
		}
		end := start + tokenLen(src, start, tok, lit)
		if end > len(src) {
			end = len(src)
		}
		if start > cursor {
			b.emit("", "", src[cursor:start])
		}
		class, key := classify(fi, start, tok)
		b.emit(class, key, src[start:end])
		cursor = end
	}
	if cursor < len(src) {
		b.emit("", "", src[cursor:])
	}
	return b.done(), true
}

// tokenLen is how many source bytes a token covers. The literal is not always
// that: the scanner drops carriage returns from comments and raw strings, so
// those two are measured on the source instead.
func tokenLen(src string, start int, tok token.Token, lit string) int {
	switch {
	case tok == token.COMMENT && strings.HasPrefix(src[start:], "//"):
		if i := strings.IndexByte(src[start:], '\n'); i >= 0 {
			return i
		}
		return len(src) - start
	case tok == token.COMMENT:
		if i := strings.Index(src[start+2:], "*/"); i >= 0 {
			return i + 4
		}
		return len(src) - start
	case tok == token.STRING && src[start] == '`':
		if i := strings.IndexByte(src[start+1:], '`'); i >= 0 {
			return i + 2
		}
		return len(src) - start
	case lit != "":
		return len(lit)
	default:
		return len(tok.String())
	}
}

func classify(fi *fileInfo, start int, tok token.Token) (class, key string) {
	switch {
	case tok == token.COMMENT:
		return Comment, ""
	case tok == token.STRING && fi.impSet[start]:
		return Import, ""
	case tok == token.STRING || tok == token.CHAR:
		return String, ""
	case tok == token.INT || tok == token.FLOAT || tok == token.IMAG:
		return Number, ""
	case tok == token.IDENT:
		c := fi.marks[start]
		if c == Decl {
			if k := fi.defs[start]; strings.Contains(k, ".") {
				return Decl, k
			}
		}
		if c == "" {
			c = Ident
		}
		return c, ""
	case tok.IsKeyword():
		return Keyword, ""
	case tok.IsOperator():
		return Op, ""
	}
	return "", ""
}

// lineBuilder cuts classified text into lines, merging neighbours that carry
// the same class so a run of punctuation is one segment, not ten.
type lineBuilder struct {
	lines [][]Segment
}

func (b *lineBuilder) emit(class, key, text string) {
	for i, piece := range strings.Split(text, "\n") {
		if i > 0 {
			b.lines = append(b.lines, []Segment{})
		}
		if piece == "" {
			continue
		}
		cur := &b.lines[len(b.lines)-1]
		if n := len(*cur); n > 0 && key == "" && (*cur)[n-1].Class == class && (*cur)[n-1].Key == "" && mergeable(class) {
			(*cur)[n-1].Text += piece
			continue
		}
		*cur = append(*cur, Segment{Class: class, Text: piece, Key: key})
	}
}

// mergeable is false for classes a renderer attaches behaviour to per token:
// two adjacent names are two links, never one.
func mergeable(class string) bool {
	switch class {
	case Decl, Ref, Import, Pkg, Func:
		return false
	}
	return true
}

func (b *lineBuilder) done() [][]Segment { return b.lines }
