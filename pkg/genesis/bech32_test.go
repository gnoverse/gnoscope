package genesis

import "testing"

// BIP-173's own vectors: the checksum is the part a hand-written port gets wrong.
func TestDecodeAcceptsBIP173Vectors(t *testing.T) {
	for _, v := range []string{"A12UEL5L", "a12uel5l", "abcdef1qpzry9x8gf2tvdw0s3jn54khce6mua7lmqqqxw"} {
		if _, _, err := Decode(v); err != nil {
			t.Errorf("Decode(%q): %v", v, err)
		}
	}
	for _, v := range []string{"A12UEL5m", "a12Uel5l", "x1b4n0q5v", "abc1xyz", "1pzry9x0s0muk"} {
		if _, _, err := Decode(v); err == nil {
			t.Errorf("Decode(%q) accepted an invalid string", v)
		}
	}
}

// A real gno.land address (the NT LLC genesis account) re-spelled under the two
// airdrop prefixes and back is the same address.
func TestToGnoRoundTripsThroughTheAirdropPrefixes(t *testing.T) {
	const g = "g1pku9u3jwr8k8vjpfypqzd0uhmrwr35zk0f8u7p"
	_, data, err := Decode(g)
	if err != nil {
		t.Fatal(err)
	}
	for _, hrp := range []string{"cosmos", "atone"} {
		other, err := Encode(hrp, data)
		if err != nil {
			t.Fatal(err)
		}
		back, err := ToGno(other)
		if err != nil || back != g {
			t.Errorf("%s: ToGno(%q) = %q, %v, want %q", hrp, other, back, err, g)
		}
	}
}

func TestToGnoRejectsWhatIsNotAnAccountAddress(t *testing.T) {
	// A valid bech32 string that carries 32 bytes is not a 20-byte account.
	long, _ := Encode("cosmos", make([]byte, 32))
	for _, s := range []string{"", "hello", "cosmos1qqqqqq", long} {
		if got, err := ToGno(s); err == nil {
			t.Errorf("ToGno(%q) = %q, want an error", s, got)
		}
	}
}
