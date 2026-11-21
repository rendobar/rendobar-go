package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The shared vectors from rendobar/openapi: every Rendobar SDK agrees with every case.
func TestVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/webhook-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Secret    string `json:"secret"`
		Timestamp int64  `json:"timestamp"`
		Body      string `json:"body"`
		Cases     []struct {
			Name    string            `json:"name"`
			Headers map[string]string `json:"headers"`
			Body    *string           `json:"body"`
			Secret  *string           `json:"secret"`
			Now     *int64            `json:"now"`
			Expect  bool              `json:"expect"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Cases) == 0 {
		t.Fatal("no vectors")
	}
	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range c.Headers {
				h.Set(k, v)
			}
			body, secret, now := file.Body, file.Secret, file.Timestamp
			if c.Body != nil {
				body = *c.Body
			}
			if c.Secret != nil {
				secret = *c.Secret
			}
			if c.Now != nil {
				now = *c.Now
			}
			if got := VerifyWith([]byte(body), h, secret, Options{Now: time.Unix(now, 0)}); got != c.Expect {
				t.Fatalf("got %v, want %v", got, c.Expect)
			}
		})
	}
}

func signedHeader(secret string, ts int64, body string) http.Header {
	stamp := strconv.FormatInt(ts, 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(stamp + "." + body))
	h := http.Header{}
	h.Set("X-Rendobar-Timestamp", stamp)
	h.Set("X-Rendobar-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	return h
}

func TestEmptySecretIsRejected(t *testing.T) {
	h := signedHeader("", 1700000000, "{}")
	if VerifyWith([]byte("{}"), h, "", Options{Now: time.Unix(1700000000, 0)}) {
		t.Fatal("an empty secret verified a delivery")
	}
	if Verify([]byte("{}"), h, "") {
		t.Fatal("Verify accepted an empty secret")
	}
}

func TestNonHexSignatureIsRejected(t *testing.T) {
	for _, sig := range []string{"sha256=éé", "sha256=zz", "not-hex", "sha256="} {
		h := signedHeader("whsec_x", 1700000000, "{}")
		h.Set("X-Rendobar-Signature", sig)
		if VerifyWith([]byte("{}"), h, "whsec_x", Options{Now: time.Unix(1700000000, 0)}) {
			t.Fatalf("signature %q verified", sig)
		}
	}
	// A correct digest without the sha256= prefix is rejected too.
	h := signedHeader("whsec_x", 1700000000, "{}")
	h.Set("X-Rendobar-Signature", strings.TrimPrefix(h.Get("X-Rendobar-Signature"), "sha256="))
	if VerifyWith([]byte("{}"), h, "whsec_x", Options{Now: time.Unix(1700000000, 0)}) {
		t.Fatal("a signature without sha256= verified")
	}
	// And the same header with a good signature does verify, so the test can fail.
	if !VerifyWith([]byte("{}"), signedHeader("whsec_x", 1700000000, "{}"), "whsec_x", Options{Now: time.Unix(1700000000, 0)}) {
		t.Fatal("a good signature did not verify")
	}
}
