// Quickstart probes a public video and prints its duration, resolution and codecs.
//
//	RENDOBAR_API_KEY=rb_... go run ./quickstart -mode live
//
// One ffprobe job. See ../README.md for the mock mode.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
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
	fmt.Println("PASS quickstart")
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
	rb := rendobar.New(opts...) // reads RENDOBAR_API_KEY

	job, err := rb.Jobs.Run(ctx, rendobar.CreateParams{
		Params: rendobar.FfprobeParams{Command: sampleURL},
		Label:  "examples/quickstart",
	}, nil)
	if err != nil {
		return fmt.Errorf("probe: %w", err)
	}
	if job.Status() != rendobar.StatusComplete || job.Complete == nil {
		return fmt.Errorf("job %s ended %s, want complete", job.ID(), job.Status())
	}
	fmt.Println("job", job.ID(), "complete")

	if mode == "mock" {
		// The mock synthesizes every job from the spec: its type is "type" and its data is {}.
		fmt.Println("SKIP (mock: a synthesized job has no ffprobe type or data) typed ffprobe result")
		return nil
	}

	probe, err := job.Complete.Ffprobe()
	if err != nil {
		return err
	}
	if probe.Summary == nil || probe.Summary.Video == nil {
		return errors.New("the probe has no video summary")
	}
	v := probe.Summary.Video
	if v.DurationSec == nil || *v.DurationSec <= 0 {
		return fmt.Errorf("duration %v, want more than 0", v.DurationSec)
	}
	if v.Video == nil || v.Video.Width == nil || v.Video.Height == nil || *v.Video.Width <= 0 || *v.Video.Height <= 0 {
		return errors.New("the probe has no video resolution")
	}
	if v.Video.Codec == "" {
		return errors.New("the probe has no video codec")
	}
	audio := "none"
	if v.Audio != nil {
		audio = v.Audio.Codec
	}
	fmt.Printf("duration %.2fs, %dx%d, video %s, audio %s\n", *v.DurationSec, *v.Video.Width, *v.Video.Height, v.Video.Codec, audio)
	return nil
}
