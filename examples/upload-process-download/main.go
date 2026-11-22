// Upload-process-download trims a clip to one second, waits for the job and
// streams the output to a local file, checking its size and SHA-256.
//
//	RENDOBAR_API_KEY=rb_... go run ./upload-process-download -mode live
//
// One ffmpeg job. The Go SDK has no uploads or assets API, so the source is
// a public URL and there is no asset to list or delete (see ../README.md).
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

const sampleURL = "https://cdn.rendobar.com/assets/examples/sample.mp4"

func main() {
	mode := flag.String("mode", "mock", "mock (the conformance mock) or live (a real API)")
	flag.Parse()
	if err := run(*mode); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("PASS upload-process-download")
}

func run(mode string) error {
	if mode != "mock" && mode != "live" {
		return fmt.Errorf("-mode must be mock or live, got %q", mode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var opts []rendobar.Option
	if u := os.Getenv("RENDOBAR_API_URL"); u != "" {
		opts = append(opts, rendobar.WithBaseURL(u))
	}
	rb := rendobar.New(opts...)

	fmt.Println("SKIP (sdk: the Go SDK has no uploads API) upload a local clip")

	// The command names its input by URL, so the job needs no Inputs.
	created, err := rb.Jobs.Create(ctx, rendobar.CreateParams{
		Params: rendobar.FfmpegParams{
			Command: "ffmpeg -i " + sampleURL + " -t 1 -vf scale=320:-2 -c:v libx264 -preset veryfast -c:a aac clip.mp4",
			Timeout: rendobar.Ptr[int64](120),
		},
		Label: "examples/upload-process-download",
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

	dir, err := os.MkdirTemp("", "rendobar-example-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	d, err := rb.Jobs.Download(ctx, job.ID())
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer d.Body.Close()
	name := d.Filename // the name the API suggests, safe to join to a directory
	switch {
	case name != "":
	case mode == "mock":
		fmt.Println("SKIP (mock: the download has no Content-Disposition) suggested file name")
		name = "clip.mp4"
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

	if mode == "mock" {
		// The mock serves 1 KiB of placeholder bytes and a placeholder digest.
		fmt.Println("SKIP (mock: the download is placeholder bytes) size and SHA-256 match the job output")
	} else {
		if n != file.Size {
			return fmt.Errorf("downloaded %d bytes, the job says %d", n, file.Size)
		}
		if got := hex.EncodeToString(digest.Sum(nil)); got != file.SHA256 {
			return fmt.Errorf("SHA-256 %s, the job says %s", got, file.SHA256)
		}
		fmt.Println("size and SHA-256 match the job output")
	}

	fmt.Println("SKIP (sdk: the Go SDK has no assets API) list, get and delete the asset")
	return nil
}
