package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gnoverse/gnoscope/pkg/gnoaddr"
	"github.com/gnoverse/gnoscope/pkg/store"
)

// The payloads below are verbatim from rpc.gno.land on 2026-09-28, trimmed of
// nothing that matters. They are the point of this test: the three shapes the
// chain actually returns are the three the panel has to tell apart, and they
// differ in ways no amount of reading the type declaration would reveal.
const (
	// A person who signs: public_key present, and a vesting schedule, which
	// only genesis can set.
	acctSigner = `{
	  "BaseAccount": {
	    "address": "g1manfred47kzduec920z88wfr64ylksmdcedlf5",
	    "coins": "104763646687ugnot",
	    "public_key": {"@type": "/tm.PubKeySecp256k1", "value": "AgBSSj+NLAA6icQ/Rf6vbrjtRbbjo197vms7Sf+eLYPI"},
	    "account_number": "3096238",
	    "sequence": "333",
	    "vesting": {"original_vesting": "106560000000ugnot", "start_time": "1789225200", "end_time": "1852383600"}
	  },
	  "attributes": "0"
	}`
	// A realm's banker: it holds 84,984 GNOT and has never signed, which is
	// exactly what public_key null and sequence 0 mean together.
	acctRealmWithCoins = `{
	  "BaseAccount": {
	    "address": "g1qxp9zvu4w6t3v3e8tschm8avdxjz8q55e4u4s3",
	    "coins": "84984428254ugnot",
	    "public_key": null,
	    "account_number": "3263071",
	    "sequence": "0"
	  },
	  "attributes": "0"
	}`
)

// The verdict has to survive the payloads the chain really sends. In
// particular: a realm holding 15.4M GNS answers "null" here, because GRC20
// balances live in realm state and never in the bank, and reading that as "the
// query failed" is how the page ends up blank for the accounts most worth
// looking at.
func TestDecodeAccount(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantExists    bool
		wantSigned    bool
		wantSequence  int64
		wantCoins     string
		wantVesting   bool
		wantVestEnd   int64
		wantParseFail bool
	}{
		{
			name:         "a signer, with the genesis vesting schedule",
			body:         acctSigner,
			wantExists:   true,
			wantSigned:   true,
			wantSequence: 333,
			wantCoins:    "104763646687ugnot",
			wantVesting:  true,
			wantVestEnd:  1852383600,
		},
		{
			name:       "a realm banker holding ugnot has never signed",
			body:       acctRealmWithCoins,
			wantExists: true,
			wantCoins:  "84984428254ugnot",
		},
		{
			name: "a realm whose money is all GRC20 has no bank account at all",
			body: "null",
		},
		{
			name: "an empty body is the same answer, not an error",
			body: "",
		},
		{
			name:          "a body that is not an account",
			body:          "{[",
			wantParseFail: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeAccount(tt.body)
			if tt.wantParseFail {
				if err == nil {
					t.Fatal("want a parse error, got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeAccount: %v", err)
			}
			if got.Exists != tt.wantExists {
				t.Errorf("exists = %v, want %v", got.Exists, tt.wantExists)
			}
			if got.HasSigned != tt.wantSigned {
				t.Errorf("has_signed = %v, want %v", got.HasSigned, tt.wantSigned)
			}
			if got.Sequence != tt.wantSequence {
				t.Errorf("sequence = %d, want %d", got.Sequence, tt.wantSequence)
			}
			if got.Coins != tt.wantCoins {
				t.Errorf("coins = %q, want %q", got.Coins, tt.wantCoins)
			}
			if (got.Vesting != nil) != tt.wantVesting {
				t.Fatalf("vesting present = %v, want %v", got.Vesting != nil, tt.wantVesting)
			}
			if got.Vesting != nil && got.Vesting.EndTime != tt.wantVestEnd {
				t.Errorf("vesting end = %d, want %d", got.Vesting.EndTime, tt.wantVestEnd)
			}
		})
	}
}

// Through the mux, not the handler: {addr} is a path value, and a handler
// called directly gets a request with none, so every case would silently test
// the empty address instead of the one it names.
func identityOf(t *testing.T, api *API, target string) addressIdentity {
	t.Helper()
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out addressIdentity
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	return out
}

// The endpoint's whole job is the verdict, and the verdict is what the page
// leads with. Every case here is an address shape a reader lands on.
func TestAddressIdentityVerdicts(t *testing.T) {
	api, db := newTestAPI(t)

	const realm = "gno.land/r/gnoswap/pool"
	if err := db.UpsertPackage("alpha", realm, "pool", "g1deployer", "TX1", 10,
		"2026-01-01T00:00:00Z", true, 1); err != nil {
		t.Fatalf("UpsertPackage: %v", err)
	}
	if err := db.UpsertSessionGrant(store.SessionGrant{
		Network: "alpha", SessionAddr: "g1session", Master: "g1master",
		AllowPaths: []string{"vm/exec:" + realm}, GrantedHeight: 20,
	}); err != nil {
		t.Fatalf("UpsertSessionGrant: %v", err)
	}
	if err := db.InsertCall("alpha", "TX2", 30, 0, "2026-01-02T00:00:00Z",
		"g1caller", realm, "Swap", "", "", true); err != nil {
		t.Fatalf("InsertCall: %v", err)
	}

	tests := []struct {
		name     string
		addr     string
		wantKind string
		check    func(t *testing.T, got addressIdentity)
	}{
		{
			name:     "a realm's banker is named, not left as a hash",
			addr:     gnoaddr.Derive(realm),
			wantKind: identityPackage,
			check: func(t *testing.T, got addressIdentity) {
				if got.Package == nil || got.Package.Path != realm {
					t.Fatalf("package = %+v, want %s", got.Package, realm)
				}
				if got.Package.Deposit {
					t.Error("the banker was reported as the storage deposit account")
				}
			},
		},
		{
			name:     "the storage deposit account is a different verdict",
			addr:     gnoaddr.DeriveStorageDeposit(realm),
			wantKind: identityPackageDeposit,
			check: func(t *testing.T, got addressIdentity) {
				if got.Package == nil || !got.Package.Deposit {
					t.Fatalf("package = %+v, want the deposit account", got.Package)
				}
			},
		},
		{
			name:     "a delegated key names its master",
			addr:     "g1session",
			wantKind: identitySessionKey,
			check: func(t *testing.T, got addressIdentity) {
				if got.SessionOf != "g1master" {
					t.Errorf("session_of = %q, want g1master", got.SessionOf)
				}
			},
		},
		{
			name:     "the master counts the keys that sign for it",
			addr:     "g1master",
			wantKind: identityUnknown, // no RPC in this harness, and it has signed nothing here
			check: func(t *testing.T, got addressIdentity) {
				if got.Delegates != 1 {
					t.Errorf("delegates = %d, want 1", got.Delegates)
				}
			},
		},
		{
			name: "an address storage has seen sign is a signer even with no RPC",
			addr: "g1caller",
			// The chain cannot be asked in this harness, and a caller row is
			// itself proof of a signature. Without that fallback the verdict
			// would be "no account" for an address with visible activity.
			wantKind: identitySigner,
			check: func(t *testing.T, got addressIdentity) {
				if got.Transactions == 0 {
					t.Error("transactions = 0 for an address with a call")
				}
			},
		},
		{
			name:     "an address nothing knows about",
			addr:     "g1nobody",
			wantKind: identityUnknown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := identityOf(t, api, "/api/address/"+tt.addr+"/identity?network=alpha")
			if got.Kind != tt.wantKind {
				t.Errorf("kind = %q, want %q", got.Kind, tt.wantKind)
			}
			// No node is configured here, so every case must say so rather than
			// letting a silent absence read as "the chain has no account".
			if got.ChainError == "" {
				t.Error("chain_error is empty with no RPC configured")
			}
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

// The reverse index is only useful if the labels every list reads include it.
// Without this, the rich list and the flows table keep printing hashes while
// the address page knows the name, and the site disagrees with itself.
func TestLabelsIncludePackageAccounts(t *testing.T) {
	api, db := newTestAPI(t)
	const realm = "gno.land/r/gnoswap/pool"
	if err := db.UpsertPackage("alpha", realm, "pool", "g1deployer", "TX1", 10,
		"2026-01-01T00:00:00Z", true, 1); err != nil {
		t.Fatalf("UpsertPackage: %v", err)
	}

	rec := httptest.NewRecorder()
	api.HandleLabels(rec, httptest.NewRequest("GET", "/api/labels?network=alpha", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var labels map[string]store.AddressLabel
	if err := json.Unmarshal(rec.Body.Bytes(), &labels); err != nil {
		t.Fatalf("decode: %v", err)
	}
	banker, ok := labels[gnoaddr.Derive(realm)]
	if !ok {
		t.Fatalf("the realm's banker has no label; got %d labels", len(labels))
	}
	if banker.Label != realm {
		t.Errorf("label = %q, want %q", banker.Label, realm)
	}
	if banker.Why == "" {
		t.Error("a derived label with no stated evidence is not checkable")
	}
}

// The chain's one delayed-vesting account (mainnet, checked 2026-10-08): the
// schedule has no start_time and says so only in "type". Read as a continuous
// one it is a schedule that began in 1970.
const acctDelayed = `{
  "BaseAccount": {
    "address": "g18c0grhdx96lw2u5t9qchl390n5weu9znkwf5vm",
    "coins": "3837075547ugnot",
    "public_key": null,
    "account_number": "2273141",
    "sequence": "0",
    "vesting": {"original_vesting": "1841860465ugnot", "end_time": "1820534400", "type": "delayed"}
  },
  "attributes": "1"
}`

func TestDecodeAccountMarksADelayedSchedule(t *testing.T) {
	got, err := decodeAccount(acctDelayed)
	if err != nil {
		t.Fatal(err)
	}
	if got.Vesting == nil || !got.Vesting.Delayed || got.Vesting.StartTime != 0 || got.Vesting.EndTime != 1820534400 {
		t.Fatalf("vesting = %+v, want a delayed schedule ending 1820534400 with no start", got.Vesting)
	}
	cont, err := decodeAccount(acctSigner)
	if err != nil || cont.Vesting == nil || cont.Vesting.Delayed {
		t.Errorf("a continuous schedule = %+v, %v: must not read as delayed", cont.Vesting, err)
	}
}
