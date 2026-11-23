package reqmeter

// UnknownLabel is the group for a query this could not name. It is a real
// answer and it is reported as one: a bucket called "unknown" that is 40% of
// the traffic is a finding, and silently folding those requests into whichever
// group happened to parse would hide it.
const UnknownLabel = "unknown"

// maxLabelLen bounds one group's name. GraphQL names in this repo are a dozen
// characters; anything beyond this is not a name.
const maxLabelLen = 48

// Label names the operation a GraphQL query performs.
//
// Preference order, which follows what this codebase's queries actually carry
// rather than what the spec allows:
//
//	query Name(...) { ... }  -> "Name"
//	query($v: T!) { f(...) } -> "f"
//	{ f(...) { ... } }       -> "f"
//
// Almost every query here is the third form, anonymous with one root field, so
// in practice this reports the root field: getTransactions, latestBlockHeight,
// getBlocks, getSupply. One query is the second form (GetSupply, the only one
// with variables) and taking its root field rather than the literal word
// "query" is why the operation keyword is skipped rather than returned.
//
// The scanner skips string literals, and that is the load-bearing part rather
// than a nicety. Several queries interpolate a package path or an address into
// a `where` clause, those values are attacker-controlled, and a naive scan over
// the raw text would happily read an identifier out of the middle of one. The
// group map would then be keyed by something an outsider writes, which is the
// unbounded map this package promises not to have. maxGroups is the backstop;
// this is the fix.
func Label(query string) string {
	toks := tokenize(query)
	for i, t := range toks {
		switch t.kind {
		case tokIdent:
			if i == 0 && isOperationKeyword(t.text) {
				// `query Foo { ... }`: the name is the next token, when the
				// next token is a name at all. `query(...)` and `query {` both
				// fall through to the root field.
				if i+1 < len(toks) && toks[i+1].kind == tokIdent {
					return clampLabel(toks[i+1].text)
				}
				continue
			}
			// An identifier before any brace, and not an operation keyword, is
			// not something this knows how to read.
			return UnknownLabel
		case tokBrace:
			// The first selection set at paren depth zero. Its first
			// identifier is the root field.
			for _, n := range toks[i+1:] {
				if n.kind == tokIdent {
					return clampLabel(n.text)
				}
			}
			return UnknownLabel
		}
	}
	return UnknownLabel
}

func isOperationKeyword(s string) bool {
	switch s {
	case "query", "mutation", "subscription":
		return true
	}
	return false
}

func clampLabel(s string) string {
	if s == "" {
		return UnknownLabel
	}
	if len(s) > maxLabelLen {
		s = s[:maxLabelLen]
	}
	return s
}

type tokKind int

const (
	tokIdent tokKind = iota
	tokBrace
)

type token struct {
	kind tokKind
	text string
}

// tokenize reduces a query to the two things Label cares about: identifiers,
// and the opening brace of the top-level selection set.
//
// Everything inside parentheses is skipped wholesale, so the argument names of
// a variable definition (`query($denom: String!)`) and of a root field never
// look like a root field themselves. String literals and `#` comments are
// skipped too, and in the literals' case a backslash escape is honoured so a
// value containing an escaped quote cannot end the literal early and let the
// rest of itself be read as query text.
//
// It stops at the first brace found outside parentheses, plus the identifiers
// that follow it, because nothing after the root field changes the answer.
func tokenize(q string) []token {
	var out []token
	parens := 0
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch {
		case c == '"':
			i = skipString(q, i)
		case c == '#':
			for i < len(q) && q[i] != '\n' {
				i++
			}
		case c == '(':
			parens++
		case c == ')':
			if parens > 0 {
				parens--
			}
		case parens > 0:
			// Inside arguments. Nothing here is a label.
		case c == '{':
			out = append(out, token{kind: tokBrace})
			// Collect only what is needed to name the root field.
			for j := i + 1; j < len(q); j++ {
				if q[j] == '"' {
					j = skipString(q, j)
					continue
				}
				if isIdentStart(q[j]) {
					end := j
					for end < len(q) && isIdentPart(q[end]) {
						end++
					}
					out = append(out, token{kind: tokIdent, text: q[j:end]})
					return out
				}
			}
			return out
		case isIdentStart(c):
			end := i
			for end < len(q) && isIdentPart(q[end]) {
				end++
			}
			out = append(out, token{kind: tokIdent, text: q[i:end]})
			i = end - 1
		}
	}
	return out
}

// skipString returns the index of the literal's closing quote, or the last
// index of the input when it is unterminated. i must point at the opening
// quote.
func skipString(q string, i int) int {
	for j := i + 1; j < len(q); j++ {
		if q[j] == '\\' {
			j++
			continue
		}
		if q[j] == '"' {
			return j
		}
	}
	return len(q) - 1
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}
