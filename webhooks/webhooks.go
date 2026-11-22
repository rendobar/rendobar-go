// Package webhooks verifies and reads Rendobar webhook deliveries and per-job
// callbacks.
//
//	body, _ := io.ReadAll(r.Body) // the exact bytes the signature covers
//	event, err := webhooks.Parse(body, r.Header, secret)
//	if err != nil {
//		w.WriteHeader(http.StatusUnauthorized)
//		return
//	}
//	payload, err := event.Payload()
//	switch data := payload.(type) {
//	case *webhooks.JobCompletedData:
//		fmt.Println(data.JobID, data.Output.File.URL)
//	default:
//		// an event this code does not handle, or one newer than this SDK
//	}
package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rendobar/rendobar-go"
)

// DefaultTolerance is how far a delivery's timestamp may be from now, either
// way, before it is refused.
const DefaultTolerance = 5 * time.Minute

// Options tune Verify and Parse. The zero value uses DefaultTolerance and the current time.
type Options struct {
	Tolerance time.Duration
	Now       time.Time
}

// Why a delivery is refused. Parse returns one of these, wrapped, so check
// with errors.Is.
var (
	// ErrNoSecret: the secret is empty or whitespace. An empty key proves nothing.
	ErrNoSecret = errors.New("webhooks: no signing secret")
	// ErrSignature: the signature is missing, malformed or does not match.
	ErrSignature = errors.New("webhooks: signature does not match")
	// ErrTimestamp: the timestamp is missing, malformed or outside the tolerance.
	ErrTimestamp = errors.New("webhooks: timestamp missing or outside the tolerance")
	// ErrPayload: the body is signed but is not a Rendobar event envelope.
	ErrPayload = errors.New("webhooks: body is not a Rendobar event")
)

// Verify reports whether body is signed with secret, or with the previous
// secret during a rotation, within the tolerance. Use it when you only need
// the check. Parse also reads the event.
//
// The signature is "sha256=" followed by the hex HMAC-SHA256 of
// "<X-Rendobar-Timestamp>.<body>". A signature without the sha256= prefix,
// or that is not hex, fails. So does an empty or whitespace-only secret.
func Verify(body []byte, header http.Header, secret string) bool {
	return VerifyWith(body, header, secret, Options{})
}

// VerifyWith is Verify with a custom tolerance or clock.
func VerifyWith(body []byte, header http.Header, secret string, o Options) bool {
	return check(body, header, secret, o) == nil
}

func check(body []byte, header http.Header, secret string, o Options) error {
	if strings.TrimSpace(secret) == "" {
		return ErrNoSecret
	}
	timestamp := header.Get("X-Rendobar-Timestamp")
	ts, err := strconv.ParseFloat(timestamp, 64)
	if err != nil || math.IsNaN(ts) || math.IsInf(ts, 0) {
		return ErrTimestamp
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
		return ErrTimestamp
	}
	signed := append([]byte(timestamp+"."), body...)
	if matches(signed, header.Get("X-Rendobar-Signature"), secret) {
		return nil
	}
	if previous := header.Get("X-Rendobar-Signature-Previous"); previous != "" && matches(signed, previous, secret) {
		return nil
	}
	return ErrSignature
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

// Payload is the data of one event: a *<Event>Data type such as
// *JobCompletedData, or *UnknownPayload for an event newer than this SDK.
// Read it with a type switch that keeps a default branch.
type Payload interface{ isPayload() }

// UnknownPayload is the data of an event this SDK does not know yet.
type UnknownPayload struct {
	Type rendobar.WebhookEvent
	Data json.RawMessage
}

func (*UnknownPayload) isPayload() {}

// Event is one delivery: the envelope every event shares. Data is the
// event's payload as JSON. Payload reads it as the type for Type.
type Event struct {
	// Version is the envelope version, "1" today. A newer version still
	// parses as far as the fields below go.
	Version string
	Type    rendobar.WebhookEvent
	// ID is the delivery id. A retried delivery keeps it, so use it to ignore
	// a repeat.
	ID     string
	OrgID  string    // the workspace the event belongs to
	SentAt time.Time // when the API sent the event
	Data   json.RawMessage
}

// Parse verifies a delivery, as Verify does, and reads its envelope. A
// refusal wraps ErrNoSecret, ErrTimestamp, ErrSignature or ErrPayload.
func Parse(body []byte, header http.Header, secret string) (*Event, error) {
	return ParseWith(body, header, secret, Options{})
}

// ParseWith is Parse with a custom tolerance or clock.
func ParseWith(body []byte, header http.Header, secret string, o Options) (*Event, error) {
	if err := check(body, header, secret, o); err != nil {
		return nil, err
	}
	var w struct {
		Version    string          `json:"version"`
		Event      string          `json:"event"`
		DeliveryID string          `json:"deliveryId"`
		Timestamp  *float64        `json:"timestamp"`
		OrgID      string          `json:"orgId"`
		Data       json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &w); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPayload, err)
	}
	if w.Event == "" {
		return nil, fmt.Errorf("%w: no event name", ErrPayload)
	}
	e := &Event{Version: w.Version, Type: rendobar.WebhookEvent(w.Event), ID: w.DeliveryID, OrgID: w.OrgID, Data: w.Data}
	if w.Timestamp != nil {
		e.SentAt = time.UnixMilli(int64(*w.Timestamp))
	}
	return e, nil
}
