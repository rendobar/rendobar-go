// Webhook-receiver is a net/http server that verifies Rendobar webhook
// deliveries and switches on their typed events.
//
// Serve deliveries with your endpoint's signing secret:
//
//	RENDOBAR_WEBHOOK_SECRET=whsec_... go run ./webhook-receiver -serve :8080
//
// Without -serve it tests itself: it starts the server on a local port and
// sends it signed deliveries, a forged one, a stale one and an event newer
// than this SDK. In mock mode it also replays the shared webhook vectors
// (RENDOBAR_WEBHOOK_VECTORS, by default ../webhooks/testdata/webhook-vectors.json).
// The Go SDK has no API for webhook endpoints, so no endpoint is created (see ../README.md).
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/rendobar/rendobar-go/webhooks"
)

func main() {
	mode := flag.String("mode", "mock", "mock (also replay the shared vectors) or live")
	serve := flag.String("serve", "", "serve deliveries on this address, such as :8080, instead of testing")
	flag.Parse()
	if *serve != "" {
		secret := os.Getenv("RENDOBAR_WEBHOOK_SECRET")
		if secret == "" {
			log.Fatal("set RENDOBAR_WEBHOOK_SECRET to your endpoint's signing secret")
		}
		log.Println("listening on", *serve)
		srv := &http.Server{Addr: *serve, Handler: &receiver{secret: secret}, ReadHeaderTimeout: 10 * time.Second}
		log.Fatal(srv.ListenAndServe())
	}
	if err := selfTest(*mode); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("PASS webhook-receiver")
}

// receiver handles POSTs of webhook deliveries.
type receiver struct {
	secret string
	opts   webhooks.Options // the zero value in production: 5 minutes tolerance, the real clock

	mu      sync.Mutex
	seen    map[string]bool // delivery ids already handled; a retry keeps its id
	handled []string        // what each delivery did, newest last
}

func (rc *receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20)) // the exact bytes the signature covers
	if err != nil {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	event, err := webhooks.ParseWith(body, r.Header, rc.secret, rc.opts)
	switch {
	case errors.Is(err, webhooks.ErrPayload):
		rc.record("bad payload")
		w.WriteHeader(http.StatusBadRequest)
		return
	case err != nil: // ErrSignature, ErrTimestamp or ErrNoSecret
		rc.record("refused: " + err.Error())
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if !rc.firstTime(event.ID) {
		rc.record("repeat " + event.ID)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	payload, err := event.Payload()
	if err != nil {
		rc.record("bad data: " + err.Error())
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	switch data := payload.(type) {
	case *webhooks.JobCompletedData:
		file := "no file" // a data job, such as ffprobe, writes no file
		if data.Output != nil && data.Output.File != nil {
			file = data.Output.File.URL
		}
		rc.record(fmt.Sprintf("completed %s %s", data.JobID, file))
	case *webhooks.JobFailedData:
		code := ""
		if data.Error != nil {
			code = data.Error.Code
		}
		rc.record(fmt.Sprintf("failed %s %s", data.JobID, code))
	case *webhooks.TestEventData:
		rc.record("test " + data.EndpointID)
	case *webhooks.UnknownPayload:
		rc.record("ignored " + string(data.Type)) // an event newer than this SDK
	default:
		rc.record("ignored " + string(event.Type)) // a known event this handler does not use
	}
	w.WriteHeader(http.StatusNoContent)
}

func (rc *receiver) firstTime(id string) bool {
	if id == "" {
		return true
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.seen == nil {
		rc.seen = map[string]bool{}
	}
	if rc.seen[id] {
		return false
	}
	rc.seen[id] = true
	return true
}

func (rc *receiver) record(s string) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.handled = append(rc.handled, s)
}

func (rc *receiver) last() string {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if len(rc.handled) == 0 {
		return ""
	}
	return rc.handled[len(rc.handled)-1]
}

func selfTest(mode string) error {
	if mode != "mock" && mode != "live" {
		return fmt.Errorf("-mode must be mock or live, got %q", mode)
	}
	fmt.Println("SKIP (sdk: the Go SDK has no webhook endpoints API) create, list, get, update, rotate, test, deliveries, retry and delete an endpoint")

	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return err
	}
	secret := "whsec_" + hex.EncodeToString(key[:])
	rc := &receiver{secret: secret}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: rc, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	url := "http://" + ln.Addr().String() + "/rendobar-webhook"

	now := time.Now()
	completed := envelope("whd_ex_1", "job.completed",
		`{"jobId":"job_ex_1","jobType":"ffmpeg","output":{"file":{"url":"https://example.com/out.mp4","path":"out.mp4","type":"video","size":1024}}}`)
	failed := envelope("whd_ex_2", "job.failed",
		`{"jobId":"job_ex_2","jobType":"ffmpeg","error":{"code":"PROCESSING_FAILED","message":"ffmpeg exited 1"}}`)
	test := envelope("whd_ex_3", "test", `{"message":"hello","endpointId":"whe_ex"}`)
	future := envelope("whd_ex_4", "job.teleported", `{"jobId":"job_ex_4"}`)
	notEvent := []byte(`{"hello":1}`)

	cases := []struct {
		name    string
		body    []byte
		header  http.Header
		status  int
		handled string
	}{
		{"job.completed", completed, signed(completed, secret, now), 204, "completed job_ex_1 https://example.com/out.mp4"},
		{"the same delivery again", completed, signed(completed, secret, now), 204, "repeat whd_ex_1"},
		{"job.failed", failed, signed(failed, secret, now), 204, "failed job_ex_2 PROCESSING_FAILED"},
		{"test", test, signed(test, secret, now), 204, "test whe_ex"},
		{"an event newer than this SDK", future, signed(future, secret, now), 204, "ignored job.teleported"},
		{"forged signature", test, signed(test, "whsec_wrong", now), 401, "refused: " + webhooks.ErrSignature.Error()},
		{"stale timestamp", test, signed(test, secret, now.Add(-10*time.Minute)), 401, "refused: " + webhooks.ErrTimestamp.Error()},
		{"signed body that is not an event", notEvent, signed(notEvent, secret, now), 400, "bad payload"},
	}
	for _, tc := range cases {
		status, err := post(url, tc.body, tc.header)
		if err != nil {
			return fmt.Errorf("%s: %w", tc.name, err)
		}
		if status != tc.status || rc.last() != tc.handled {
			return fmt.Errorf("%s: %d %q, want %d %q", tc.name, status, rc.last(), tc.status, tc.handled)
		}
		fmt.Printf("%-34s %d %s\n", tc.name, status, tc.handled)
	}

	if mode == "live" {
		fmt.Println("SKIP (live: the shared vectors are checked in mock mode) webhook vectors")
		return nil
	}
	return replayVectors()
}

// replayVectors runs the shared webhook vectors through the same handler, at
// the vectors' own clock.
func replayVectors() error {
	path := os.Getenv("RENDOBAR_WEBHOOK_VECTORS")
	if path == "" {
		path = "../webhooks/testdata/webhook-vectors.json"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("webhook vectors: %w", err)
	}
	var v struct {
		Secret    string `json:"secret"`
		Timestamp int64  `json:"timestamp"`
		Body      string `json:"body"`
		Cases     []struct {
			Name    string            `json:"name"`
			Secret  *string           `json:"secret"`
			Body    *string           `json:"body"`
			Now     *int64            `json:"now"`
			Headers map[string]string `json:"headers"`
			Expect  bool              `json:"expect"`
			NotFor  []string          `json:"notFor"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("webhook vectors: %w", err)
	}
	if len(v.Cases) == 0 {
		return errors.New("webhook vectors: no cases")
	}
	checked := 0
	for _, c := range v.Cases {
		if slices.Contains(c.NotFor, "go") {
			continue
		}
		checked++
		secret, body, now := v.Secret, v.Body, v.Timestamp
		if c.Secret != nil {
			secret = *c.Secret
		}
		if c.Body != nil {
			body = *c.Body
		}
		if c.Now != nil {
			now = *c.Now
		}
		rc := &receiver{secret: secret, opts: webhooks.Options{Now: time.Unix(now, 0)}}
		req := httptest.NewRequest(http.MethodPost, "/rendobar-webhook", bytes.NewBufferString(body))
		for k, val := range c.Headers {
			req.Header.Set(k, val)
		}
		rec := httptest.NewRecorder()
		rc.ServeHTTP(rec, req)
		want := http.StatusUnauthorized
		if c.Expect {
			want = http.StatusNoContent
		}
		if rec.Code != want {
			return fmt.Errorf("vector %q: %d (%s), want %d", c.Name, rec.Code, rc.last(), want)
		}
	}
	fmt.Println("webhook vectors:", checked, "cases agree")
	return nil
}

// envelope wraps event data the way Rendobar sends it.
func envelope(id, event, data string) []byte {
	b, err := json.Marshal(struct {
		Version    string          `json:"version"`
		Event      string          `json:"event"`
		DeliveryID string          `json:"deliveryId"`
		Timestamp  int64           `json:"timestamp"`
		OrgID      string          `json:"orgId"`
		Data       json.RawMessage `json:"data"`
	}{"1", event, id, time.Now().UnixMilli(), "org_ex", json.RawMessage(data)})
	if err != nil {
		panic(err) // the data literals above are valid JSON
	}
	return b
}

// signed returns the headers Rendobar sends with a delivery of body.
func signed(body []byte, secret string, at time.Time) http.Header {
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(body)
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("X-Rendobar-Timestamp", ts)
	h.Set("X-Rendobar-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	return h
}

func post(url string, body []byte, header http.Header) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header = header
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode, nil
}
