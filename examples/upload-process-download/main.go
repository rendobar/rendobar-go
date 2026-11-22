// Upload-process-download uploads a local clip, trims it to one second with
// the upload as the input, streams the output to a local file and checks its
// size and SHA-256. It then shares the output, and lists, reads and deletes
// the uploaded asset.
//
//	RENDOBAR_API_KEY=rb_... go run ./upload-process-download -mode live
//
// One ffmpeg job. The clip is ../fixtures/clip.mp4, two seconds of test
// pattern and tone.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/rendobar/rendobar-go"
)

const (
	fixture   = "fixtures/clip.mp4"
	sampleURL = "https://cdn.rendobar.com/assets/examples/sample.mp4"
)

func main() {
	mode := flag.String("mode", "mock", "mock (the conformance mock) or live (a real API)")
	flag.Parse()
	if err := run(*mode); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("PASS upload-process-download")
}

func run(mode string) (err error) {
	if mode != "mock" && mode != "live" {
		return fmt.Errorf("-mode must be mock or live, got %q", mode)
	}
	mock := mode == "mock"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var opts []rendobar.Option
	if u := os.Getenv("RENDOBAR_API_URL"); u != "" {
		opts = append(opts, rendobar.WithBaseURL(u))
	}
	rb := rendobar.New(opts...)

	input, assetID, err := upload(ctx, rb, mock)
	if err != nil {
		return err
	}
	if assetID != "" {
		defer func() {
			// Clean up when a later step failed. The last step deletes it, so NOT_FOUND is fine.
			derr := rb.Assets.Delete(context.Background(), assetID)
			var apiErr *rendobar.Error
			if derr != nil && !(errors.As(derr, &apiErr) && apiErr.Code == rendobar.CodeNotFound) && err == nil {
				err = fmt.Errorf("delete asset: %w", derr)
			}
		}()
	}

	// The command reads the input under the name the Inputs map gives it.
	created, err := rb.Jobs.Create(ctx, rendobar.CreateParams{
		Params: rendobar.FfmpegParams{
			Command: "ffmpeg -i clip.mp4 -t 1 -vf scale=160:-2 -c:v libx264 -preset veryfast -c:a aac trimmed.mp4",
			Timeout: rendobar.Ptr[int64](120),
		},
		Inputs: map[string]rendobar.Input{"clip.mp4": input},
		Label:  "examples/upload-process-download",
	})
	if err != nil {
		return fmt.Errorf("submit: %w", err)
	}
	fmt.Println("submitted", created.ID)

	job, err := rb.Jobs.Wait(ctx, created.ID, &rendobar.WaitOptions{
		OnProgress: func(j *rendobar.Job) { fmt.Println("  status", j.Status()) },
	})
	if err != nil {
		return fmt.Errorf("wait: %w", err)
	}
	if job.Status() != rendobar.StatusComplete {
		if job.Failed != nil {
			return fmt.Errorf("job %s failed: %s %s", job.ID(), job.Failed.Error.Code, job.Failed.Error.Message)
		}
		return fmt.Errorf("job %s ended %s, want complete", job.ID(), job.Status())
	}
	file := job.Complete.Output.File
	if file == nil {
		return fmt.Errorf("job %s has no output file", job.ID())
	}
	fmt.Println("output", file.Path, file.Size, "bytes")

	if err := download(ctx, rb, job.ID(), file, mock); err != nil {
		return err
	}
	if err := share(ctx, rb, job.ID()); err != nil {
		return err
	}
	return assets(ctx, rb, assetID, mock)
}

// upload sends the fixture and returns the job input that reads it.
func upload(ctx context.Context, rb *rendobar.Client, mock bool) (rendobar.Input, string, error) {
	if mock {
		fmt.Println("SKIP (mock: POST /assets answers a placeholder deduplicated asset, so nothing uploads) upload the clip, so the input is a public URL")
		return rendobar.URL(sampleURL), "", nil
	}
	f, err := os.Open(fixture)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, "", err
	}
	asset, err := rb.Uploads.Create(ctx, f, info.Size(), rendobar.UploadParams{
		Filename:    "clip.mp4",
		ContentType: "video/mp4",
		OnProgress:  func(p rendobar.UploadProgress) { fmt.Printf("  uploaded %d of %d bytes\n", p.Loaded, p.Total) },
	})
	if err != nil {
		return nil, "", fmt.Errorf("upload: %w", err)
	}
	if asset.Status != rendobar.AssetStatusReady {
		return nil, "", fmt.Errorf("uploaded asset %s is %s, want ready", asset.ID, asset.Status)
	}
	if asset.SizeBytes == nil || *asset.SizeBytes != info.Size() {
		return nil, "", fmt.Errorf("uploaded asset %s has size %v, want %d", asset.ID, asset.SizeBytes, info.Size())
	}
	fmt.Println("uploaded", asset.ID, info.Size(), "bytes")
	// asset:// names an uploaded file. Its URL works as an input too.
	return rendobar.URL("asset://" + asset.ID), asset.ID, nil
}

// download streams the output to a temporary file and checks it.
func download(ctx context.Context, rb *rendobar.Client, jobID string, file *rendobar.OutputFile, mock bool) error {
	dir, err := os.MkdirTemp("", "rendobar-example-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	d, err := rb.Jobs.Download(ctx, jobID)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer d.Body.Close()
	name := d.Filename // the name the API suggests, safe to join to a directory
	switch {
	case name != "":
	case mock:
		fmt.Println("SKIP (mock: the download has no Content-Disposition) suggested file name")
		name = "trimmed.mp4"
	default:
		return errors.New("the download has no file name")
	}
	out, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	defer out.Close()
	digest := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, digest), d.Body)
	if err != nil {
		return fmt.Errorf("download body: %w", err)
	}
	if n <= 0 {
		return errors.New("the downloaded file is empty")
	}
	if d.Size >= 0 && d.Size != n {
		return fmt.Errorf("downloaded %d bytes, the response said %d", n, d.Size)
	}
	fmt.Println("downloaded", n, "bytes to", name)

	if mock {
		// The mock serves 1 KiB of placeholder bytes and a placeholder digest.
		fmt.Println("SKIP (mock: the download is placeholder bytes) size and SHA-256 match the job output")
		return nil
	}
	if n != file.Size {
		return fmt.Errorf("downloaded %d bytes, the job says %d", n, file.Size)
	}
	if got := hex.EncodeToString(digest.Sum(nil)); got != file.SHA256 {
		return fmt.Errorf("SHA-256 %s, the job says %s", got, file.SHA256)
	}
	fmt.Println("size and SHA-256 match the job output")
	return nil
}

// share makes a public link to the output, reads it back and removes it.
func share(ctx context.Context, rb *rendobar.Client, jobID string) error {
	created, err := rb.Jobs.CreateShare(ctx, jobID)
	if err != nil {
		return fmt.Errorf("share: %w", err)
	}
	if created.ShareURL == "" {
		return errors.New("the share has no URL")
	}
	got, err := rb.Jobs.GetShare(ctx, jobID)
	if err != nil {
		return fmt.Errorf("get share: %w", err)
	}
	if got == nil || got.ShareURL == "" {
		return errors.New("the job has no share after sharing it")
	}
	if err := rb.Jobs.DeleteShare(ctx, jobID); err != nil {
		return fmt.Errorf("delete share: %w", err)
	}
	fmt.Println("shared the output at", created.ShareURL, "then removed the link")
	return nil
}

// assets finds the upload in the asset list, reads it, gets a download link
// and deletes it. In mock mode it reads the mock's own asset.
func assets(ctx context.Context, rb *rendobar.Client, assetID string, mock bool) error {
	page, err := rb.Assets.List(ctx, rendobar.ListAssetsParams{Lifecycle: rendobar.AssetLifecycleEphemeral, Limit: 100})
	if err != nil {
		return fmt.Errorf("list assets: %w", err)
	}
	if len(page.Assets) == 0 || page.Meta.Total < 1 {
		return fmt.Errorf("the asset list is empty (total %d)", page.Meta.Total)
	}
	if mock {
		assetID = page.Assets[0].ID
	}
	found := false
	for _, a := range page.Assets {
		found = found || a.ID == assetID
	}
	if !found {
		return fmt.Errorf("asset %s is not in the first page of %d", assetID, page.Meta.Total)
	}
	fmt.Println("listed", len(page.Assets), "of", page.Meta.Total, "ephemeral assets, the upload among them")

	asset, err := rb.Assets.Get(ctx, assetID)
	if err != nil {
		return fmt.Errorf("get asset: %w", err)
	}
	if asset.ID == "" || asset.CreatedAt.Time().IsZero() {
		return fmt.Errorf("asset %+v has no id or creation time", asset)
	}
	link, err := rb.Assets.Download(ctx, assetID)
	if err != nil {
		return fmt.Errorf("asset download link: %w", err)
	}
	if link.URL == "" || !link.ExpiresAt.Time().After(time.Unix(0, 0)) {
		return fmt.Errorf("download link %+v has no URL or expiry", link)
	}
	fmt.Println("asset", asset.ID, asset.Status, "with a download link until", link.ExpiresAt.Time().UTC().Format(time.RFC3339))

	if mock {
		if err := rb.Assets.Delete(ctx, assetID); err != nil {
			return fmt.Errorf("delete asset: %w", err)
		}
		fmt.Println("deleted", assetID)
		return nil
	}
	if err := rb.Assets.Delete(ctx, assetID); err != nil {
		return fmt.Errorf("delete asset: %w", err)
	}
	_, err = rb.Assets.Get(ctx, assetID)
	var apiErr *rendobar.Error
	if !errors.As(err, &apiErr) || apiErr.Code != rendobar.CodeNotFound {
		return fmt.Errorf("get after delete: got %v, want NOT_FOUND", err)
	}
	fmt.Println("deleted", assetID, "and it is gone")
	return nil
}
