package rendobar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func problemPaths(err error) string {
	var e *Error
	if !errors.As(err, &e) {
		return fmt.Sprintf("not an *Error: %v", err)
	}
	var paths []string
	for _, p := range e.Problems {
		paths = append(paths, p.Path)
	}
	return string(e.Code) + " " + strings.Join(paths, ",")
}

func TestValidateChecksTheSchema(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params JobParams
		want   string
	}{
		{"valid ffmpeg", FfmpegParams{Command: "ffmpeg -i a.mp4 b.mp4", Compute: FfmpegComputeGPU}, ""},
		{"required", FfmpegParams{}, "INVALID_ARGUMENT command"},
		{"too long", FfmpegParams{Command: strings.Repeat("x", 16385)}, "INVALID_ARGUMENT command"},
		{"unknown enum", FfmpegParams{Command: "x", Compute: "quantum"}, "INVALID_ARGUMENT compute"},
		{"exclusive minimum", FfmpegParams{Command: "x", Timeout: Ptr[int64](0)}, "INVALID_ARGUMENT timeout"},
		{"two problems", FfmpegParams{Compute: "quantum"}, "INVALID_ARGUMENT command,compute"},
		{"pattern", CaptionBurnParams{FontColor: "white"}, "INVALID_ARGUMENT fontColor"},
		{"maximum", CaptionBurnParams{FontSize: Ptr(401)}, "INVALID_ARGUMENT fontSize"},
		{"nested", ImageUpscaleParams{Sizing: ImageUpscaleSizingFactor{Factor: 0.5}}, "INVALID_ARGUMENT sizing.factor"},
		{"union branch enum", CompressTargetParams{Target: CompressTargetTargetPreset("tiny")}, "INVALID_ARGUMENT target"},
		{"union branch bounds", CompressTargetParams{Target: CompressTargetTargetInt(101)}, "INVALID_ARGUMENT target"},
		{"valid union", CompressTargetParams{Target: CompressTargetTargetQuality{Quality: 80}}, ""},
		{"discriminated branch", ImageGenerateFlux2DevParams{Prompt: ""}, "INVALID_ARGUMENT prompt"},
		{"custom with a new type", CustomParams{Type: "compose"}, ""},
		{"custom with a typed type", CustomParams{Type: "ffmpeg"}, "INVALID_ARGUMENT type"},
		{"custom with no type", CustomParams{}, "INVALID_ARGUMENT type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.params.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("got %v", err)
				}
				return
			}
			if got := problemPaths(err); got != tc.want {
				t.Fatalf("got %q, want %q (%v)", got, tc.want, err)
			}
		})
	}
}

func TestParamsEncodeAsTheAPIExpects(t *testing.T) {
	for _, tc := range []struct {
		params JobParams
		want   string
	}{
		{FfmpegParams{Command: "x"}, `{"command":"x"}`},
		{FfmpegParams{Command: "x", Timeout: Ptr[int64](60), Compute: FfmpegComputeCPU}, `{"command":"x","compute":"cpu","timeout":60}`},
		{CaptionBurnParams{Bold: Ptr(false)}, `{"bold":false}`},
		{ImageUpscaleParams{Sizing: ImageUpscaleSizingHeight{Height: 2160}}, `{"sizing":{"height":2160,"mode":"height"}}`},
		{ImageGenerateFlux2DevParams{Prompt: "a cat"}, `{"model":"flux-2-dev","prompt":"a cat"}`},
		{CompressTargetParams{Target: CompressTargetTargetPresetBalanced}, `{"target":"balanced"}`},
		{CompressTargetParams{Target: CompressTargetTargetMaxBytes{MaxBytes: 5_000_000}}, `{"target":{"maxBytes":5000000}}`},
		{CustomParams{Type: "compose", Fields: map[string]any{"a": 1}}, `{"a":1}`},
		{CustomParams{Type: "compose"}, `{}`},
	} {
		m, err := paramsMap(tc.params)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := json.Marshal(m) // map keys sort, so the comparison is stable
		if string(got) != tc.want {
			t.Fatalf("%T: got %s, want %s", tc.params, got, tc.want)
		}
	}
}

func TestCreateSendsTheTypeFromTheParams(t *testing.T) {
	var sent struct {
		Type     string            `json:"type"`
		Params   map[string]any    `json:"params"`
		Inputs   map[string]string `json:"inputs"`
		Callback map[string]any    `json:"callback"`
	}
	c, hits := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &sent)
		w.WriteHeader(201)
		fmt.Fprint(w, `{"data":{"id":"job_t","status":"waiting"}}`)
	})
	_, err := c.Jobs.Create(context.Background(), CreateParams{
		Params:   ImageEditStandardParams{Prompt: "make it blue"},
		Inputs:   map[string]string{"image": "https://example.com/a.png"},
		Callback: &Callback{URL: "https://example.com/hook", Verify: true, Events: []CallbackEvent{CallbackEventJobStarted}},
	})
	if err != nil || hits.Load() != 1 {
		t.Fatalf("%v, %d requests", err, hits.Load())
	}
	if sent.Type != "image.edit" || sent.Params["model"] != "standard" || sent.Inputs["image"] != "https://example.com/a.png" ||
		sent.Callback["verify"] != true || fmt.Sprint(sent.Callback["events"]) != "[job.started]" {
		t.Fatalf("sent %+v", sent)
	}
}

func TestCreateRefusesBadParamsBeforeSending(t *testing.T) {
	c, hits := testServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201) })
	for _, p := range []CreateParams{
		{},
		{Params: FfprobeParams{}},
		{Params: FfprobeParams{Command: "x"}, Callback: &Callback{}},
		{Params: FfprobeParams{Command: "x"}, Callback: &Callback{URL: "https://x", Events: []CallbackEvent{"job.exploded"}}},
	} {
		if _, err := c.Jobs.Create(context.Background(), p); codeOf(err) != CodeInvalidArgument {
			t.Fatalf("%+v: got %v", p, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("%d requests sent", hits.Load())
	}
}

func TestSchemaRefusesABadTypeName(t *testing.T) {
	c, hits := testServer(t, func(w http.ResponseWriter, r *http.Request) {})
	for _, name := range []string{"", "../account", "FFMPEG", "a b"} {
		if _, err := c.Jobs.Schema(context.Background(), name); codeOf(err) != CodeInvalidArgument {
			t.Fatalf("%q: got %v", name, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("%d requests sent", hits.Load())
	}
}
