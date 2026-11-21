// Package webhooks verifies Rendobar webhook deliveries.
//
//	body, _ := io.ReadAll(r.Body) // the exact bytes the signature covers
//	if !webhooks.Verify(body, r.Header, secret) {
//		w.WriteHeader(http.StatusUnauthorized)
//		return
//	}
package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultTolerance is how old a delivery may be before Verify rejects it.
const DefaultTolerance = 5 * time.Minute

// Options tune Verify. The zero value uses DefaultTolerance and the current time.
type Options struct {
	Tolerance time.Duration
	Now       time.Time
}

// Verify reports whether body is signed with secret, or with the previous
// secret during a rotation, and was signed within the tolerance.
//
// The signature is "sha256=" followed by the hex HMAC-SHA256 of
// "<X-Rendobar-Timestamp>.<body>". A signature without the sha256= prefix,
// or that is not hex, returns false. An empty secret always returns false:
// anyone can compute an HMAC with an empty key, so it proves nothing.
func Verify(body []byte, header http.Header, secret string) bool {
	return VerifyWith(body, header, secret, Options{})
}

// VerifyWith is Verify with a custom tolerance or clock.
func VerifyWith(body []byte, header http.Header, secret string, o Options) bool {
	if secret == "" {
		return false
	}
	signature := header.Get("X-Rendobar-Signature")
	timestamp := header.Get("X-Rendobar-Timestamp")
	if signature == "" || timestamp == "" {
		return false
	}
	ts, err := strconv.ParseFloat(timestamp, 64)
	if err != nil || math.IsNaN(ts) || math.IsInf(ts, 0) {
		return false
	}
	tolerance := o.Tolerance
	if tolerance <= 0 {
		tolerance = DefaultTolerance
	}
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	if math.Abs(float64(now.Unix())-ts) > tolerance.Seconds() {
		return false
	}
	signed := append([]byte(timestamp+"."), body...)
	if matches(signed, signature, secret) {
		return true
	}
	previous := header.Get("X-Rendobar-Signature-Previous")
	return previous != "" && matches(signed, previous, secret)
}

func matches(signed []byte, signature, secret string) bool {
	digest, ok := strings.CutPrefix(signature, "sha256=")
	if !ok {
		return false
	}
	expected, err := hex.DecodeString(digest)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(signed)
	return hmac.Equal(expected, mac.Sum(nil)) // constant time
}
