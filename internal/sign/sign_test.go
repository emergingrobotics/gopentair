package sign

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"
)

type sigVector struct {
	Method        string            `json:"method"`
	URL           string            `json:"url"`
	Body          *string           `json:"body"`
	Headers       map[string]string `json:"headers"`
	Authorization string            `json:"authorization"`
	AmzDate       string            `json:"amzdate"`
}

// TestBotocoreVectors checks the Standard profile against signatures
// produced by botocore's SigV4Auth for Pentair-shaped requests.
func TestBotocoreVectors(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/reference_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		SigV4 []sigVector `json:"sigv4"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	creds := Credentials{"AKIDEXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "SESSIONTOKENEXAMPLE"}
	for _, c := range v.SigV4 {
		var body []byte
		if c.Body != nil {
			body = []byte(*c.Body)
		}
		r, _ := http.NewRequest(c.Method, c.URL, bytes.NewReader(body))
		for k, val := range c.Headers {
			r.Header.Set(k, val)
		}
		now, _ := time.Parse(AmzDateLayout, c.AmzDate)
		if err := Sign(r, body, creds, "us-west-2", "execute-api", Standard, now); err != nil {
			t.Fatal(err)
		}
		if got := r.Header.Get("Authorization"); got != c.Authorization {
			t.Errorf("%s %s:\n got  %s\n want %s", c.Method, c.URL, got, c.Authorization)
		}
	}
}

// TestAWSGetVanilla is the "get-vanilla" case from the AWS SigV4 test suite.
func TestAWSGetVanilla(t *testing.T) {
	r, _ := http.NewRequest("GET", "https://example.amazonaws.com/", nil)
	now, _ := time.Parse(AmzDateLayout, "20150830T123600Z")
	err := Sign(r, nil, Credentials{AccessKeyID: "AKIDEXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"},
		"us-east-1", "service", Standard, now)
	if err != nil {
		t.Fatal(err)
	}
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if got := r.Header.Get("Authorization"); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestAppProfileAndVerify(t *testing.T) {
	body := []byte(`{"payload":{"d13":"1"}}`)
	r, _ := http.NewRequest("PUT", "https://api.pentair.cloud/device/device-service/user/device/abc", bytes.NewReader(body))
	r.Header.Set("x-amz-id-token", "ID")
	r.Header.Set("iseuropeanuser", "false")
	r.Header.Set("x-pha-apptype", "")
	r.Header.Set("Content-Type", "application/json; charset=UTF-8")
	creds := Credentials{"AK", "SECRET", "ST"}
	if err := Sign(r, body, creds, "us-west-2", "execute-api", App, time.Now()); err != nil {
		t.Fatal(err)
	}
	auth := r.Header.Get("Authorization")
	if !bytes.Contains([]byte(auth), []byte("SignedHeaders=host;iseuropeanuser;x-amz-date;x-amz-id-token;x-amz-security-token;x-pha-apptype,")) {
		t.Fatalf("unexpected signed headers: %s", auth)
	}
	if err := Verify(r, body, "SECRET", "us-west-2", "execute-api"); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := Verify(r, []byte("tampered"), "SECRET", "us-west-2", "execute-api"); err == nil {
		t.Fatal("tampered body verified")
	}
	if err := Verify(r, body, "WRONG", "us-west-2", "execute-api"); err == nil {
		t.Fatal("wrong secret verified")
	}
}
