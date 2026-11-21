package rendobar

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"time"
)

// Download is a job's output file, streamed. Read Body and close it. Size is
// the length in bytes, or -1 when the API did not say.
type Download struct {
	Body        io.ReadCloser
	Size        int64
	ContentType string
	// Filename is the name the API suggests for the file, or "".
	Filename string
}

// Download streams the output file of a completed job. The caller must close
// Body. WithTimeout bounds only the wait for the response headers. ctx bounds
// the body, so a large file can take as long as it needs.
//
// The request is retried like any other until the body starts, never after.
// A job with no output, such as an ffprobe job, gives a 400 *Error. Its data
// is in JobComplete.Output.Data instead.
func (j *Jobs) Download(ctx context.Context, id string) (*Download, error) {
	if err := j.client.needKey(); err != nil {
		return nil, err
	}
	if err := checkID(id); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, j.client.baseURL+"/jobs/"+url.PathEscape(id)+"/download", nil)
	if err != nil {
		return nil, err
	}
	req.Header = j.client.header.Clone()
	res, err := j.client.stream.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		defer res.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		return nil, apiError(res.StatusCode, res.Header, body)
	}
	d := &Download{Body: res.Body, Size: res.ContentLength, ContentType: res.Header.Get("Content-Type")}
	if _, params, err := mime.ParseMediaType(res.Header.Get("Content-Disposition")); err == nil {
		d.Filename = params["filename"]
	}
	return d, nil
}

// headerTimeout bounds each attempt's wait for response headers, and leaves
// the body to the request's context.
type headerTimeout struct {
	base    http.RoundTripper
	timeout time.Duration
}

// errHeaderTimeout reads as a timeout, so the retrier does not retry it.
var errHeaderTimeout error = timeoutError{}

type timeoutError struct{}

func (timeoutError) Error() string   { return "timed out waiting for response headers" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return false }

func (h *headerTimeout) RoundTrip(req *http.Request) (*http.Response, error) {
	if h.timeout <= 0 {
		return h.base.RoundTrip(req)
	}
	ctx, cancel := context.WithCancel(req.Context())
	timer := time.AfterFunc(h.timeout, cancel)
	res, err := h.base.RoundTrip(req.WithContext(ctx))
	if !timer.Stop() {
		// The timer fired: the headers came too late, or never.
		if err == nil {
			res.Body.Close()
		}
		cancel()
		if req.Context().Err() == nil {
			return nil, errHeaderTimeout
		}
		return nil, req.Context().Err()
	}
	if err != nil {
		cancel()
		if errors.Is(err, context.Canceled) && req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		return nil, err
	}
	res.Body = &cancelOnClose{ReadCloser: res.Body, cancel: cancel}
	return res, nil
}

// cancelOnClose releases the attempt's context when the caller closes the body.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}
