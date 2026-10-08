// Package genesis answers "what did the gnoland-1 genesis give this address":
// the allocation, and the vesting schedule if it has one.
//
// The public RPC cannot serve the genesis file (over 250MB, past the node's
// write deadline), and the chain keeps no record of which accounts it began with
// once they have moved. So the one exact source is the balances sheet that the
// genesis was built from, published by gnolang/independence-day. This package
// reads that sheet, pinned to a commit and checked against the repository's own
// SHA256SUMS, and never trusts a byte of it before the checksum matches.
package genesis

import (
	"errors"
	"strings"
)

// bech32, per BIP-173. Hand-written because the whole need is "re-spell these 20
// bytes under another prefix", and a module for that would be the larger risk.

const charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

var generator = [5]uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}

func polymod(values []byte) uint32 {
	chk := uint32(1)
	for _, v := range values {
		top := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		for i := 0; i < 5; i++ {
			if (top>>uint(i))&1 == 1 {
				chk ^= generator[i]
			}
		}
	}
	return chk
}

func hrpExpand(hrp string) []byte {
	out := make([]byte, 0, len(hrp)*2+1)
	for i := 0; i < len(hrp); i++ {
		out = append(out, hrp[i]>>5)
	}
	out = append(out, 0)
	for i := 0; i < len(hrp); i++ {
		out = append(out, hrp[i]&31)
	}
	return out
}

// convertBits regroups a byte string between 5-bit and 8-bit words.
func convertBits(data []byte, from, to uint, pad bool) ([]byte, error) {
	var acc, bits uint
	maxv := uint(1)<<to - 1
	var out []byte
	for _, v := range data {
		if uint(v)>>from != 0 {
			return nil, errors.New("value out of range")
		}
		acc = acc<<from | uint(v)
		bits += from
		for bits >= to {
			bits -= to
			out = append(out, byte(acc>>bits&maxv))
		}
	}
	if pad {
		if bits > 0 {
			out = append(out, byte(acc<<(to-bits)&maxv))
		}
	} else if bits >= from || acc<<(to-bits)&maxv != 0 {
		return nil, errors.New("invalid padding")
	}
	return out, nil
}

// Decode reads a bech32 string and returns its prefix and the bytes it carries.
func Decode(s string) (hrp string, data []byte, err error) {
	if strings.ToLower(s) != s && strings.ToUpper(s) != s {
		return "", nil, errors.New("mixed case")
	}
	s = strings.ToLower(s)
	pos := strings.LastIndexByte(s, '1')
	if pos < 1 || pos+7 > len(s) {
		return "", nil, errors.New("not a bech32 address")
	}
	hrp = s[:pos]
	words := make([]byte, 0, len(s)-pos-1)
	for i := pos + 1; i < len(s); i++ {
		c := strings.IndexByte(charset, s[i])
		if c < 0 {
			return "", nil, errors.New("invalid character")
		}
		words = append(words, byte(c))
	}
	if polymod(append(hrpExpand(hrp), words...)) != 1 {
		return "", nil, errors.New("bad checksum")
	}
	data, err = convertBits(words[:len(words)-6], 5, 8, false)
	return hrp, data, err
}

// Encode spells data under the given prefix.
func Encode(hrp string, data []byte) (string, error) {
	words, err := convertBits(data, 8, 5, true)
	if err != nil {
		return "", err
	}
	values := append(hrpExpand(hrp), words...)
	mod := polymod(append(values, 0, 0, 0, 0, 0, 0)) ^ 1
	var b strings.Builder
	b.WriteString(hrp)
	b.WriteByte('1')
	for _, w := range words {
		b.WriteByte(charset[w])
	}
	for i := 0; i < 6; i++ {
		b.WriteByte(charset[(mod>>uint(5*(5-i)))&31])
	}
	return b.String(), nil
}

// ToGno re-spells a 20-byte bech32 address (cosmos1…, atone1…, g1…) as a g1…
// address. The airdrops kept each recipient's key bytes and changed only the
// prefix, so this is the whole conversion.
func ToGno(addr string) (string, error) {
	_, data, err := Decode(strings.TrimSpace(addr))
	if err != nil {
		return "", err
	}
	if len(data) != 20 {
		return "", errors.New("not a 20-byte account address")
	}
	return Encode("g", data)
}
