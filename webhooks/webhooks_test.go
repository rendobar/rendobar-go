package webhooks

import (
	"encoding/json"
	"net/http"
	"os"
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
