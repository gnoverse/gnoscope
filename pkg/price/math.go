package price

import (
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

// q96 is Uniswap V3's fixed-point scale, and GnoSwap's: prices are stored as
// sqrt(price) * 2^96.
var q96 = new(big.Int).Lsh(big.NewInt(1), 96)

// RatioFromSqrtPriceX96 turns a pool's stored sqrtPriceX96 into the exact ratio
// it encodes: **base units of token1 per one base unit of token0**.
//
// The "base units" part is the whole reason this package can price tokens whose
// realms will not say how many decimals they have. The ratio is a property of
// the pool, not of either token's display convention, so it is exact whether or
// not anyone knows what a whole BUBBLE is. Decimals only ever enter at display
// time, and only on the per-token figure.
//
// Exact, in big.Rat, rather than through float64. sqrtPriceX96 on mainnet today
// runs to 30 digits (158761177616844740432909320156 on wugnot/GNS) and squaring
// it in a float64 throws away the bottom half before the division ever happens.
func RatioFromSqrtPriceX96(s string) (*big.Rat, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty sqrtPriceX96")
	}
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return nil, fmt.Errorf("sqrtPriceX96 %q is not an integer", s)
	}
	if n.Sign() <= 0 {
		return nil, fmt.Errorf("sqrtPriceX96 %q is not positive", s)
	}
	// (n / 2^96)^2 == n^2 / 2^192, kept as a ratio so nothing rounds.
	num := new(big.Int).Mul(n, n)
	den := new(big.Int).Mul(q96, q96)
	return new(big.Rat).SetFrac(num, den), nil
}

// TickToRatio is the same quantity from the tick instead of the sqrt price:
// 1.0001^tick.
//
// Not used for pricing, which reads sqrtPriceX96 directly and exactly. It is
// here as the cross-check that caught a sign error once already: the two agree
// to about four significant figures by construction (wugnot/GNS on 2026-09-29
// reports tick 13902 and sqrtPriceX96 158761177616844740432909320156, which are
// 4.0154 and 4.0154), so a disagreement means the pool path or the token order
// is wrong rather than the arithmetic.
func TickToRatio(tick int32) *big.Rat {
	base := big.NewRat(10001, 10000)
	out := new(big.Rat).SetInt64(1)
	n := tick
	neg := n < 0
	if neg {
		n = -n
	}
	// Square-and-multiply: a naive loop at tick 887272 (the V3 maximum) is
	// 887k big.Rat multiplications, each on a growing denominator.
	acc := new(big.Rat).Set(base)
	for n > 0 {
		if n&1 == 1 {
			out.Mul(out, acc)
		}
		acc.Mul(acc, acc)
		n >>= 1
	}
	if neg {
		return new(big.Rat).Inv(out)
	}
	return out
}

// ratFloat is the lossy exit from exact arithmetic, used only where the result
// is about to be compared or displayed rather than propagated.
func ratFloat(r *big.Rat) float64 {
	if r == nil {
		return 0
	}
	f, _ := r.Float64()
	return f
}

// Gno's qeval debug repr, which is what every read in this package comes back
// as. Three shapes matter:
//
//	("158761177616844740432909320156" string)
//	(13902 int32)
//	(true bool)
//
// A multi-return function prints one per line, in order, with an unset error
// printing as "(undefined)". So GetBalances answers three lines and the third
// is noise unless something failed.
var (
	reprString = regexp.MustCompile(`^\("((?:[^"\\]|\\.)*)" string\)$`)
	reprNumber = regexp.MustCompile(`^\((-?\d+) (?:u?int(?:8|16|32|64)?)\)$`)
	reprBool   = regexp.MustCompile(`^\((true|false) bool\)$`)
)

// ReprLines splits a qeval response into its per-return-value lines.
func ReprLines(out string) []string {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// ReprString unwraps a ("…" string) line.
func ReprString(line string) (string, bool) {
	m := reprString.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return "", false
	}
	return m[1], true
}

// ReprInt unwraps any (N <integer type>) line.
func ReprInt(line string) (int64, bool) {
	m := reprNumber.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// ReprBool unwraps a (true bool) line.
func ReprBool(line string) (bool, bool) {
	m := reprBool.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return false, false
	}
	return m[1] == "true", true
}

// ReprErrored reports whether a multi-return qeval response carries a non-nil
// error in its last position.
//
// This is the difference between "the pool holds nothing" and "there is no such
// pool", and both come back with HTTP 200 and a plausible-looking first line.
// GetSlot0SqrtPriceX96 on a path that does not exist answers ("" string)
// followed by an error value, which parsed naively is an empty price rather
// than a missing pool.
func ReprErrored(out string) bool {
	lines := ReprLines(out)
	if len(lines) == 0 {
		return false
	}
	last := lines[len(lines)-1]
	return last != "(undefined)" && strings.Contains(last, "err")
}

// slippagePct is how far a realised quote sits below what spot promised.
//
// Positive means worse for the taker, which is the only direction that happens
// in practice and the only one worth a warning. Clamped at 100 so a router that
// answers zero (liquidity exhausted, or the route was rejected) reports as a
// total loss rather than as a negative number nobody can read.
func slippagePct(got, ideal *big.Rat) float64 {
	if ideal == nil || ideal.Sign() == 0 {
		return 0
	}
	ratio := new(big.Rat).Quo(got, ideal)
	one := new(big.Rat).SetInt64(1)
	p := ratFloat(new(big.Rat).Sub(one, ratio)) * 100
	switch {
	case p < 0:
		return 0
	case p > 100:
		return 100
	}
	return p
}

// usd, pct and itoa format the numbers that get spliced into warning text.
// Kept here rather than inline so every warning reads the same way, which is
// what makes a wall of caveats skimmable instead of exhausting.
func usd(v float64) string {
	switch {
	case v >= 1000:
		return "$" + addThousands(strconv.FormatFloat(v, 'f', 0, 64))
	case v >= 1:
		return "$" + strconv.FormatFloat(v, 'f', 2, 64)
	default:
		return "$" + strconv.FormatFloat(v, 'f', 4, 64)
	}
}

func pct(v float64) string {
	if v >= 10 {
		return strconv.FormatFloat(v, 'f', 0, 64) + "%"
	}
	return strconv.FormatFloat(v, 'f', 2, 64) + "%"
}

func itoa(n int) string { return strconv.Itoa(n) }

func addThousands(s string) string {
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}
