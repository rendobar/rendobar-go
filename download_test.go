package rendobar

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"
)

// A body far larger than anything worth buffering, written slowly enough
// that it outlasts WithTimeout.
func TestDownloadStreamsALargeBodyPastTheTimeout(t *testing.T) {
	const size = 256 << 20 // 256 MiB
	chunk := make([]byte, 1<<20)
	for i := range chunk {
		chunk[i] = byte(i)
	}
	want := sha256.New()
	for i := 0; i < size/len(chunk); i++ {
		want.Write(chunk)
	}
	srv, hits := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/jobs/job_big/download" || r.Header.Get("Authorization") != "Bearer rb_test" {
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(size))
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Disposition", `attachment; filename="out.mp4"`)
		w.WriteHeader(200)
		for i := 0; i < size/len(chunk); i++ {
			if i == 8 {
				time.Sleep(700 * time.Millisecond) // past the 300 ms timeout, mid-body
			}
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})
	c := New(WithAPIKey("rb_test"), WithBaseURL(srv.baseURL), WithTimeout(300*time.Millisecond))
	d, err := c.Jobs.Download(context.Background(), "job_big")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Body.Close()
	got := sha256.New()
	n, err := io.Copy(got, d.Body)
	if err != nil || n != size || string(got.Sum(nil)) != string(want.Sum(nil)) {
		t.Fatalf("read %d bytes, %v", n, err)
	}
	if d.Size != size || d.ContentType != "video/mp4" || d.Filename != "out.mp4" || hits.Load() != 1 {
		t.Fatalf("download %+v, %d requests", d, hits.Load())
	}
}

func TestDownloadTimesOutWaitingForHeadersAndIsNotRetried(t *testing.T) {
	srv, hits := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
		case <-r.Context().Done():
		}
	})
	c := New(WithAPIKey("rb_test"), WithBaseURL(srv.baseURL), WithTimeout(200*time.Millisecond))
	started := time.Now()
	_, err := c.Jobs.Download(context.Background(), "job_t")
	var ue *url.Error
	if !errors.As(err, &ue) || !ue.Timeout() || time.Since(started) > 2*time.Second || hits.Load() != 1 {
		t.Fatalf("got %v after %v and %d requests", err, time.Since(started), hits.Load())
	}
}

func TestDownloadRetriesBeforeTheBodyAndReadsErrors(t *testing.T) {
	n := 0
	srv, hits := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if n++; n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":{"code":"VALIDATION_ERROR","message":"Job has no output"}}`)
	})
	_, err := srv.Jobs.Download(context.Background(), "job_t")
	if codeOf(err) != CodeValidation || statusOf(err) != 400 || hits.Load() != 2 {
		t.Fatalf("got %v after %d requests", err, hits.Load())
	}
}

func TestDownloadBodyStopsWithTheContext(t *testing.T) {
	srv, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	d, err := srv.Jobs.Download(ctx, "job_t")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Body.Close()
	time.AfterFunc(100*time.Millisecond, cancel)
	if _, err := io.ReadAll(d.Body); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}
