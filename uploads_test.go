package rendobar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// uploadServer plays the API and the storage of an upload. start is the POST
// /assets response, with %s standing for the server's own URL. put answers
// each PUT, by path.
type uploadServer struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	puts     map[string][]byte // path -> bytes received
	putHits  atomic.Int32
	start    string
	put      func(w http.ResponseWriter, r *http.Request, n int32)
	started  map[string]any
	complete map[string]any
}

func newUploadServer(t *testing.T, start string) *uploadServer {
	u := &uploadServer{t: t, puts: map[string][]byte{}, start: start}
	u.put = func(w http.ResponseWriter, r *http.Request, _ int32) {
		w.Header().Set("ETag", `"etag-`+strings.TrimPrefix(r.URL.Path, "/put/")+`"`)
	}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/assets":
			_ = json.Unmarshal(body, &u.started)
			if strings.Contains(u.start, "%") {
				fmt.Fprintf(w, u.start, u.srv.URL)
			} else {
				fmt.Fprint(w, u.start)
			}
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/put/"):
			if r.Header.Get("Authorization") != "" || r.Header.Get("X-Rendobar-Client") != "" {
				t.Errorf("a presigned PUT carried Rendobar headers")
			}
			n := u.putHits.Add(1)
			u.mu.Lock()
			u.puts[r.URL.Path] = body
			u.mu.Unlock()
			u.put(w, r, n)
		case r.Method == http.MethodPost && r.URL.Path == "/assets/asset_1/complete":
			_ = json.Unmarshal(body, &u.complete)
			fmt.Fprint(w, `{"data":{"id":"asset_1","status":"ready","sizeBytes":10}}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *uploadServer) client() *Client {
	return New(WithAPIKey("rb_test"), WithBaseURL(u.srv.URL))
}

func TestUploadInOnePut(t *testing.T) {
	u := newUploadServer(t, `{"status":"presigned","data":{"id":"asset_1","status":"pending"},"upload":{"method":"PUT","url":"%s/put/whole","expiresAt":1}}`)
	var seen []UploadProgress
	file := []byte("0123456789")
	asset, err := u.client().Uploads.Create(context.Background(), bytes.NewReader(file), int64(len(file)), UploadParams{
		Filename: "clip.mp4", ContentType: "video/mp4", Persist: true,
		OnProgress: func(p UploadProgress) { seen = append(seen, p) },
	})
	if err != nil || asset.ID != "asset_1" || asset.Status != AssetStatusReady {
		t.Fatalf("%+v %v", asset, err)
	}
	if !bytes.Equal(u.puts["/put/whole"], file) {
		t.Fatalf("PUT %q", u.puts["/put/whole"])
	}
	if u.started["filename"] != "clip.mp4" || u.started["size"] != float64(10) || u.started["lifecycle"] != "persisted" || u.started["contentType"] != "video/mp4" {
		t.Fatalf("start %v", u.started)
	}
	if _, ok := u.complete["parts"]; ok {
		t.Fatalf("a single PUT completed with parts: %v", u.complete)
	}
	if len(seen) != 1 || seen[0] != (UploadProgress{Loaded: 10, Total: 10}) {
		t.Fatalf("progress %v", seen)
	}
}

func TestUploadInParts(t *testing.T) {
	u := newUploadServer(t, `{"status":"multipart","data":{"id":"asset_1"},"upload":{"uploadId":"u","partSize":4,"parts":[`+
		`{"partNumber":3,"url":"%[1]s/put/3"},{"partNumber":1,"url":"%[1]s/put/1"},{"partNumber":2,"url":"%[1]s/put/2"}],"expiresAt":1}}`)
	var mu sync.Mutex
	var last UploadProgress
	asset, err := u.client().Uploads.Create(context.Background(), strings.NewReader("0123456789"), 10, UploadParams{
		Filename: "clip.mp4", Concurrency: 2,
		OnProgress: func(p UploadProgress) { mu.Lock(); last = p; mu.Unlock() },
	})
	if err != nil || asset.ID != "asset_1" {
		t.Fatalf("%+v %v", asset, err)
	}
	for path, want := range map[string]string{"/put/1": "0123", "/put/2": "4567", "/put/3": "89"} {
		if string(u.puts[path]) != want {
			t.Errorf("%s got %q, want %q", path, u.puts[path], want)
		}
	}
	parts, _ := json.Marshal(u.complete["parts"])
	if string(parts) != `[{"etag":"\"etag-1\"","partNumber":1},{"etag":"\"etag-2\"","partNumber":2},{"etag":"\"etag-3\"","partNumber":3}]` {
		t.Fatalf("complete parts %s", parts)
	}
	if last != (UploadProgress{Loaded: 10, Total: 10}) {
		t.Fatalf("progress %v", last)
	}
	if u.started["lifecycle"] != "ephemeral" {
		t.Fatalf("lifecycle %v", u.started["lifecycle"])
	}
}

func TestUploadRetriesAFailedPut(t *testing.T) {
	putBackoff = time.Millisecond
	t.Cleanup(func() { putBackoff = 200 * time.Millisecond })
	u := newUploadServer(t, `{"status":"presigned","data":{"id":"asset_1"},"upload":{"method":"PUT","url":"%s/put/whole","expiresAt":1}}`)
	u.put = func(w http.ResponseWriter, _ *http.Request, n int32) {
		if n < 3 {
			w.WriteHeader(503)
		}
	}
	if _, err := u.client().Uploads.Create(context.Background(), strings.NewReader("abc"), 3, UploadParams{Filename: "a"}); err != nil {
		t.Fatal(err)
	}
	if u.putHits.Load() != 3 {
		t.Fatalf("%d PUTs, want 3", u.putHits.Load())
	}
}

func TestUploadGivesUpAfterThreePuts(t *testing.T) {
	putBackoff = time.Millisecond
	t.Cleanup(func() { putBackoff = 200 * time.Millisecond })
	u := newUploadServer(t, `{"status":"presigned","data":{"id":"asset_1"},"upload":{"method":"PUT","url":"%s/put/whole","expiresAt":1}}`)
	u.put = func(w http.ResponseWriter, _ *http.Request, _ int32) { w.WriteHeader(403) }
	_, err := u.client().Uploads.Create(context.Background(), strings.NewReader("abc"), 3, UploadParams{Filename: "a"})
	if codeOf(err) != CodeUploadFailed || statusOf(err) != 403 || u.putHits.Load() != 3 {
		t.Fatalf("%v after %d PUTs", err, u.putHits.Load())
	}
}

func TestUploadPartWithoutAnETagFails(t *testing.T) {
	u := newUploadServer(t, `{"status":"multipart","data":{"id":"asset_1"},"upload":{"partSize":4,"parts":[{"partNumber":1,"url":"%s/put/1"}]}}`)
	u.put = func(http.ResponseWriter, *http.Request, int32) {} // no ETag header
	_, err := u.client().Uploads.Create(context.Background(), strings.NewReader("abc"), 3, UploadParams{Filename: "a"})
	if codeOf(err) != CodeUploadFailed || u.complete != nil {
		t.Fatalf("got %v, completed %v", err, u.complete)
	}
}

func TestUploadOfAFileTheWorkspaceHasSendsNothing(t *testing.T) {
	u := newUploadServer(t, `{"status":"deduplicated","data":{"id":"asset_9","status":"ready"}}`)
	asset, err := u.client().Uploads.Create(context.Background(), strings.NewReader("abc"), 3, UploadParams{Filename: "a", Checksum: "abc123"})
	if err != nil || asset.ID != "asset_9" || u.putHits.Load() != 0 || u.complete != nil {
		t.Fatalf("%+v %v, %d PUTs", asset, err, u.putHits.Load())
	}
	if u.started["checksum"] != "abc123" {
		t.Fatalf("start %v", u.started)
	}
}

func TestUploadRefusesAStartItCannotFollow(t *testing.T) {
	u := newUploadServer(t, `{"status":"resumable","data":{"id":"asset_1"},"upload":{}}`)
	_, err := u.client().Uploads.Create(context.Background(), strings.NewReader("abc"), 3, UploadParams{Filename: "a"})
	if err == nil || !strings.Contains(err.Error(), `"resumable"`) {
		t.Fatalf("got %v", err)
	}
}

func TestUploadRefusesAMethodItCannotSend(t *testing.T) {
	u := newUploadServer(t, `{"status":"presigned","data":{"id":"asset_1"},"upload":{"method":"POST","url":"%s/put/whole","expiresAt":1}}`)
	_, err := u.client().Uploads.Create(context.Background(), strings.NewReader("abc"), 3, UploadParams{Filename: "a"})
	if err == nil || !strings.Contains(err.Error(), `"POST"`) || u.putHits.Load() != 0 {
		t.Fatalf("got %v, %d PUTs", err, u.putHits.Load())
	}
}

func TestUploadRefusesBadInputBeforeSending(t *testing.T) {
	c, _, hits := recordingServer(t, 200, `{}`)
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"nil file": func() error { _, err := c.Uploads.Create(ctx, nil, 1, UploadParams{Filename: "a"}); return err },
		"zero size": func() error {
			_, err := c.Uploads.Create(ctx, strings.NewReader(""), 0, UploadParams{Filename: "a"})
			return err
		},
		"no filename": func() error { _, err := c.Uploads.Create(ctx, strings.NewReader("a"), 1, UploadParams{}); return err },
		"negative concurrency": func() error {
			_, err := c.Uploads.Create(ctx, strings.NewReader("a"), 1, UploadParams{Filename: "a", Concurrency: -1})
			return err
		},
	} {
		if codeOf(call()) != CodeInvalidArgument {
			t.Errorf("%s: want INVALID_ARGUMENT", name)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("%d requests", hits.Load())
	}
}

func TestUploadStopsWhenTheContextEnds(t *testing.T) {
	u := newUploadServer(t, `{"status":"multipart","data":{"id":"asset_1"},"upload":{"partSize":1,"parts":[`+
		`{"partNumber":1,"url":"%[1]s/put/1"},{"partNumber":2,"url":"%[1]s/put/2"},{"partNumber":3,"url":"%[1]s/put/3"}]}}`)
	ctx, cancel := context.WithCancel(context.Background())
	u.put = func(w http.ResponseWriter, r *http.Request, _ int32) {
		cancel()
		<-r.Context().Done()
	}
	_, err := u.client().Uploads.Create(ctx, strings.NewReader("abc"), 3, UploadParams{Filename: "a", Concurrency: 1})
	if err != context.Canceled || u.complete != nil {
		t.Fatalf("got %v, completed %v", err, u.complete)
	}
}

func TestAttemptTimeoutGrowsWithSizeAndTries(t *testing.T) {
	if got := attemptTimeout(0, 0); got != 10*time.Second {
		t.Errorf("empty first try %v", got)
	}
	if got := attemptTimeout(500<<10, 0); got != 11*time.Second {
		t.Errorf("500 KiB first try %v", got)
	}
	if got := attemptTimeout(100<<10, 5); got != 62*time.Second {
		t.Errorf("100 KiB late try %v", got)
	}
}
