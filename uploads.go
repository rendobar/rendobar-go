package rendobar

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Uploads sends local files to Rendobar as assets that jobs can read.
type Uploads struct{ client *Client }

// UploadParams describes a file for Uploads.Create. Filename is required.
type UploadParams struct {
	// Filename is the display name, also used when the file is downloaded.
	Filename string
	// ContentType is the MIME type, such as "video/mp4", or "" to let the API detect it.
	ContentType string
	// Checksum is the file's SHA-256 as hex, or "". With it, the API skips the
	// upload when the workspace already has the same file.
	Checksum string
	// Persist keeps the asset until you delete it. By default it is ephemeral
	// and expires after 24 hours.
	Persist bool
	// Concurrency is how many parts of a large file upload at once. 0 means 5.
	Concurrency int
	// OnProgress is called as bytes finish uploading, one call at a time, or nil.
	OnProgress func(UploadProgress)
}

// UploadProgress is how much of an upload has finished.
type UploadProgress struct {
	Loaded int64 // bytes uploaded so far
	Total  int64 // the file's size
}

type uploadedPart struct {
	PartNumber int64  `json:"partNumber"`
	ETag       string `json:"etag"`
}

// Create uploads a file and returns the ready asset. size is the file's
// length in bytes. Pass the asset to a job as rendobar.URL("asset://" + asset.ID)
// or as its URL.
//
// A small file goes up in one request, a large one in parts, Concurrency at a
// time, read from file with ReadAt. An *os.File works, and so does a
// *bytes.Reader. Each part is retried up to 3 times. ctx bounds the whole upload.
func (u *Uploads) Create(ctx context.Context, file io.ReaderAt, size int64, p UploadParams) (*Asset, error) {
	c := u.client
	if err := c.needKey(); err != nil {
		return nil, err
	}
	switch {
	case file == nil:
		return nil, local(CodeInvalidArgument, "Uploads.Create needs a file to read")
	case size <= 0:
		return nil, local(CodeInvalidArgument, "Uploads.Create size must be more than 0, got %d", size)
	case p.Filename == "":
		return nil, local(CodeInvalidArgument, "UploadParams.Filename is required")
	case p.Concurrency < 0:
		return nil, local(CodeInvalidArgument, "UploadParams.Concurrency must not be negative, got %d", p.Concurrency)
	}
	lifecycle := AssetLifecycleEphemeral
	if p.Persist {
		lifecycle = AssetLifecyclePersisted
	}
	body := struct {
		Filename    string         `json:"filename"`
		Size        int64          `json:"size"`
		ContentType string         `json:"contentType,omitempty"`
		Checksum    string         `json:"checksum,omitempty"`
		Lifecycle   AssetLifecycle `json:"lifecycle"`
	}{p.Filename, size, p.ContentType, p.Checksum, lifecycle}
	// uploadStart (spec_gen.go) is the response of POST /assets: top-level, not in a data envelope.
	var start uploadStart
	if err := c.doJSON(ctx, http.MethodPost, "/assets", nil, body, &start); err != nil {
		return nil, err
	}
	progress := newProgress(size, p.OnProgress)
	var (
		asset *Asset
		parts []uploadedPart
	)
	switch start.Status {
	case uploadStartStatusDeduplicated:
		// The workspace already has this file: nothing to send.
		if err := hasAsset(start.Deduplicated.Data); err != nil {
			return nil, err
		}
		progress.add(size)
		return start.Deduplicated.Data, nil
	case uploadStartStatusPresigned:
		// One PUT of the whole file.
		asset = start.Presigned.Data
		if err := hasAsset(asset); err != nil {
			return nil, err
		}
		up := start.Presigned.Upload
		switch {
		case up == nil || up.URL == "":
			return nil, fmt.Errorf("rendobar: the upload start has no URL")
		case up.Method != uploadStartPresignedUploadMethodPUT:
			return nil, fmt.Errorf("rendobar: the upload start asks for a %q, which this SDK cannot send: upgrade it", up.Method)
		}
		if _, err := c.putPart(ctx, up.URL, file, 0, size); err != nil {
			return nil, err
		}
		progress.add(size)
	case uploadStartStatusMultipart:
		// One PUT per part, then complete with the parts.
		asset = start.Multipart.Data
		if err := hasAsset(asset); err != nil {
			return nil, err
		}
		var err error
		if parts, err = c.putParts(ctx, file, size, start.Multipart.Upload, p.Concurrency, progress); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("rendobar: the upload start is %q, which this SDK cannot follow: upgrade it", start.Status)
	}
	seg, err := segment("asset id", asset.ID)
	if err != nil {
		return nil, err
	}
	complete := struct {
		Parts []uploadedPart `json:"parts,omitempty"`
	}{parts}
	return call[Asset](ctx, c, http.MethodPost, "/assets/"+seg+"/complete", nil, complete)
}

// hasAsset checks that an upload start names the asset it creates.
func hasAsset(a *Asset) error {
	if a == nil || a.ID == "" {
		return fmt.Errorf("rendobar: the upload start has no asset")
	}
	return nil
}

// putParts uploads every part of a multipart upload, at most concurrency at
// a time. The first part that fails stops the rest.
func (c *Client) putParts(ctx context.Context, file io.ReaderAt, size int64, up *uploadStartMultipartUpload, concurrency int, progress *progress) ([]uploadedPart, error) {
	if up == nil || up.PartSize <= 0 || len(up.Parts) == 0 {
		return nil, fmt.Errorf("rendobar: the multipart upload start has no parts")
	}
	if concurrency == 0 {
		concurrency = 5
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var (
		mu   sync.Mutex
		out  []uploadedPart
		wg   sync.WaitGroup
		jobs = make(chan int)
	)
	for range min(concurrency, len(up.Parts)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				part := up.Parts[i]
				offset := (part.PartNumber - 1) * up.PartSize
				if part.PartNumber < 1 || offset >= size {
					cancel(fmt.Errorf("rendobar: upload part %d is outside the file", part.PartNumber))
					continue
				}
				n := min(up.PartSize, size-offset)
				etag, err := c.putPart(ctx, part.URL, file, offset, n)
				if err == nil && etag == "" {
					// The storage took the part but did not expose its ETag, which completing needs.
					err = &Error{Code: CodeUploadFailed, StatusCode: http.StatusBadGateway,
						Message: "a multipart upload needs the ETag response header, and the storage did not send it"}
				}
				if err != nil {
					cancel(err)
					continue
				}
				progress.add(n)
				mu.Lock()
				out = append(out, uploadedPart{PartNumber: part.PartNumber, ETag: etag})
				mu.Unlock()
			}
		}()
	}
	for i := range up.Parts {
		if ctx.Err() != nil {
			break
		}
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	sort.Slice(out, func(a, b int) bool { return out[a].PartNumber < out[b].PartNumber })
	return out, nil
}

// putAttempts is how often one PUT is tried. Variables, so the tests can shorten them.
var (
	putAttempts = 3
	putBackoff  = 200 * time.Millisecond
)

// putPart sends one PUT of the n bytes at offset to a presigned URL and returns its ETag.
// Each attempt gets a time budget that grows with the size and with each try,
// so a slow link still finishes and a stalled connection is cut and tried again.
func (c *Client) putPart(ctx context.Context, url string, file io.ReaderAt, offset, n int64) (string, error) {
	var last error
	for attempt := 0; attempt < putAttempts; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(putBackoff << (attempt - 1))
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", context.Cause(ctx)
			case <-timer.C:
			}
		}
		etag, err := c.putOnce(ctx, url, io.NewSectionReader(file, offset, n), n, attemptTimeout(n, attempt))
		if err == nil {
			return etag, nil
		}
		if ctx.Err() != nil {
			return "", context.Cause(ctx)
		}
		last = err
	}
	return "", last
}

func (c *Client) putOnce(ctx context.Context, url string, body io.Reader, n int64, budget time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, body)
	if err != nil {
		return "", err
	}
	req.ContentLength = n
	res, err := c.upload.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return "", &Error{Code: CodeUploadFailed, StatusCode: res.StatusCode, Message: fmt.Sprintf("the upload PUT failed with status %d", res.StatusCode)}
	}
	return res.Header.Get("ETag"), nil
}

// attemptTimeout is one PUT attempt's budget: a grace period plus the time
// the size takes at an assumed rate, more patient on each try (500, 100,
// then 50 KiB/s).
func attemptTimeout(size int64, attempt int) time.Duration {
	grace := []time.Duration{10 * time.Second, 30 * time.Second, 60 * time.Second}
	rate := []int64{500 << 10, 100 << 10, 50 << 10}
	i := min(attempt, len(grace)-1)
	return grace[i] + time.Duration(size*int64(time.Second)/rate[i])
}

// progress reports bytes uploaded, one OnProgress call at a time.
type progress struct {
	mu     sync.Mutex
	loaded int64
	total  int64
	fn     func(UploadProgress)
}

func newProgress(total int64, fn func(UploadProgress)) *progress {
	return &progress{total: total, fn: fn}
}

func (p *progress) add(n int64) {
	if p.fn == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.loaded += n
	p.fn(UploadProgress{Loaded: p.loaded, Total: p.total})
}
