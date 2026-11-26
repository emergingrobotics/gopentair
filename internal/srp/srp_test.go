package srp

import (
	"encoding/base64"
	"encoding/json"
	"math/big"
	"os"
	"testing"
	"time"
)

type vectors struct {
	SRP struct {
		A           string            `json:"A"`
		APriv       string            `json:"a"`
		Salt        string            `json:"salt"`
		B           string            `json:"B"`
		BPriv       string            `json:"b"`
		UserID      string            `json:"user_id"`
		Password    string            `json:"password"`
		Pool        string            `json:"pool"`
		SecretBlock string            `json:"secret_block"`
		Response    map[string]string `json:"response"`
	} `json:"srp"`
}

func loadVectors(t *testing.T) vectors {
	t.Helper()
	b, err := os.ReadFile("../../testdata/reference_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestPycognitoVector checks the client against a vector produced by
// pycognito's AWSSRP with a fixed private value and timestamp.
func TestPycognitoVector(t *testing.T) {
	v := loadVectors(t).SRP
	a, _ := new(big.Int).SetString(v.APriv, 16)
	c, err := NewClientWithPrivate(v.Pool, v.Password, a)
	if err != nil {
		t.Fatal(err)
	}
	if c.AHex() != v.A {
		t.Fatalf("A mismatch")
	}
	now, _ := time.Parse(TimestampLayout, v.Response["TIMESTAMP"])
	resp, err := c.Response(Challenge{
		UserIDForSRP: v.UserID, Username: v.UserID, SaltHex: v.Salt, BHex: v.B, SecretBlock: v.SecretBlock,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range v.Response {
		if resp[k] != want {
			t.Errorf("%s = %q, want %q", k, resp[k], want)
		}
	}
}

// TestServerAgreement checks that the server-side math in this package
// derives the same key as the client.
func TestServerAgreement(t *testing.T) {
	const pool, user, pw, salt = "us-west-2_TestPool", "uid-1", "hunter2", "0badc0ffee"
	c, err := NewClient(pool, pw)
	if err != nil {
		t.Fatal(err)
	}
	ver := Verifier(pool, user, pw, salt)
	b := big.NewInt(0).SetBytes([]byte("server-private-value-for-testing"))
	B := ServerB(ver, b)
	block := base64.StdEncoding.EncodeToString([]byte("blk"))
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	resp, err := c.Response(Challenge{UserIDForSRP: user, SaltHex: salt, BHex: B.Text(16), SecretBlock: block}, now)
	if err != nil {
		t.Fatal(err)
	}
	if resp["TIMESTAMP"] != "Fri Jan 2 03:04:05 UTC 2026" {
		t.Errorf("timestamp format: %q", resp["TIMESTAMP"])
	}
	key, err := ServerKey(c.AHex(), ver, b, B)
	if err != nil {
		t.Fatal(err)
	}
	want := Signature(key, pool, user, []byte("blk"), resp["TIMESTAMP"])
	if resp["PASSWORD_CLAIM_SIGNATURE"] != want {
		t.Fatal("client and server signatures differ")
	}
	// Wrong password must not agree.
	bad, _ := NewClient(pool, "wrong")
	r2, _ := bad.Response(Challenge{UserIDForSRP: user, SaltHex: salt, BHex: B.Text(16), SecretBlock: block}, now)
	key2, _ := ServerKey(bad.AHex(), ver, b, B)
	if r2["PASSWORD_CLAIM_SIGNATURE"] == Signature(key2, pool, user, []byte("blk"), r2["TIMESTAMP"]) {
		t.Fatal("wrong password produced a valid signature")
	}
}

func TestPadHex(t *testing.T) {
	cases := map[string]string{"abc": "0abc", "8f": "008f", "7f": "7f", "": ""}
	for in, want := range cases {
		if got := PadHex(in); got != want {
			t.Errorf("PadHex(%q)=%q want %q", in, got, want)
		}
	}
}
