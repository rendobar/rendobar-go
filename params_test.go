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
		{"maximum", CaptionBurnParams{FontSize: Ptr[int64](401)}, "INVALID_ARGUMENT fontSize"},
		{"nested", ImageUpscaleParams{Sizing: ImageUpscaleSizingFactor{Factor: Ptr(0.5)}}, "INVALID_ARGUMENT sizing.factor"},
		{"union branch enum", CompressTargetParams{Target: CompressTargetTargetPreset("tiny")}, "INVALID_ARGUMENT target"},
		{"union branch bounds", CompressTargetParams{Target: CompressTargetTargetInt(101)}, "INVALID_ARGUMENT target"},
		{"valid union", CompressTargetParams{Target: CompressTargetTargetQuality{Quality: Ptr[int64](80)}}, ""},
		{"discriminated branch", ImageGenerateFlux2DevParams{Prompt: ""}, "INVALID_ARGUMENT prompt"},
		{"custom with a new type", CustomParams{Type: "compose"}, ""},
		{"custom with a typed type", CustomParams{Type: JobTypeFfmpeg, Fields: map[string]any{"command": "x", "newParam": 1}}, ""},
		{"custom with a bad type name", CustomParams{Type: "Not A Type"}, "INVALID_ARGUMENT type"},
		{"required pointer", ImageUpscaleParams{Sizing: ImageUpscaleSizingHeight{}}, "INVALID_ARGUMENT sizing.height"},
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
		{ImageUpscaleParams{Sizing: ImageUpscaleSizingHeight{Height: Ptr[int64](2160)}}, `{"sizing":{"height":2160,"mode":"height"}}`},
		{ImageGenerateFlux2DevParams{Prompt: "a cat"}, `{"model":"flux-2-dev","prompt":"a cat"}`},
		{CompressTargetParams{Target: CompressTargetTargetPresetBalanced}, `{"target":"balanced"}`},
		{CompressTargetParams{Target: CompressTargetTargetMaxBytes{MaxBytes: Ptr[int64](5_000_000)}}, `{"target":{"maxBytes":5000000}}`},
		{CustomParams{Type: "compose", Fields: map[string]any{"a": 1}}, `{"a":1}`},
		{CustomParams{Type: "compose"}, `{}`},
		// An int64 above 2^53 keeps every digit on its way into the request.
		{ImageGenerateFlux2DevParams{Prompt: "p", Seed: Ptr[int64](9007199254740993)}, `{"model":"flux-2-dev","prompt":"p","seed":9007199254740993}`},
	} {
		m, err := jsonObject("params", tc.params)
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
		Type     string              `json:"type"`
		Params   map[string]any      `json:"params"`
		Inputs   map[string][]string `json:"inputs"`
		Callback map[string]any      `json:"callback"`
	}
	c, hits := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &sent)
		w.WriteHeader(201)
		fmt.Fprint(w, `{"data":{"id":"job_t","status":"waiting"}}`)
	})
	_, err := c.Jobs.Create(context.Background(), CreateParams{
		Params:   ImageEditStandardParams{Prompt: "make it blue"},
		Inputs:   map[string]Input{"images": URLs{"https://example.com/a.png", "https://example.com/b.png"}},
		Callback: &Callback{URL: "https://example.com/hook", Verify: true, Events: []CallbackEvent{CallbackEventJobStarted}},
	})
	if err != nil || hits.Load() != 1 {
		t.Fatalf("%v, %d requests", err, hits.Load())
	}
	if sent.Type != "image.edit" || sent.Params["model"] != "standard" || fmt.Sprint(sent.Inputs["images"]) != "[https://example.com/a.png https://example.com/b.png]" ||
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
	for _, name := range []JobType{"", "../account", "FFMPEG", "a b"} {
		if _, err := c.Jobs.Schema(context.Background(), name); codeOf(err) != CodeInvalidArgument {
			t.Fatalf("%q: got %v", name, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("%d requests sent", hits.Load())
	}
}

func TestInputsEncodeEveryForm(t *testing.T) {
	m, err := jsonObject("inputs", map[string]Input{
		"source":   URL("https://example.com/a.mp4"),
		"images":   URLs{"https://example.com/1.png", "https://example.com/2.png"},
		"list.txt": Content("file 'a.mp4'"),
		"prev.mp4": FromJob("job_abc123"),
		"fonts":    Fonts{{Family: "Display", Src: "https://example.com/d.woff2"}, {Src: "https://example.com/e.ttf"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(m)
	want := `{"fonts":[{"family":"Display","src":"https://example.com/d.woff2"},{"src":"https://example.com/e.ttf"}],` +
		`"images":["https://example.com/1.png","https://example.com/2.png"],"list.txt":{"content":"file 'a.mp4'"},` +
		`"prev.mp4":{"job":"job_abc123"},"source":"https://example.com/a.mp4"}`
	if string(got) != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestInputsAreCheckedBeforeSending(t *testing.T) {
	c, hits := testServer(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201) })
	for name, in := range map[string]Input{
		"../etc/passwd": URL("https://x"),
		"/abs":          URL("https://x"),
		`a\b`:           URL("https://x"),
		"empty":         URL(""),
		"images":        URLs{},
		"job":           FromJob("not-a-job"),
		"big":           Content(strings.Repeat("x", 64<<10+1)),
		"fonts":         Fonts{{Family: "x"}},
		"nil":           nil,
	} {
		_, err := c.Jobs.Create(context.Background(), CreateParams{Params: FfmpegParams{Command: "x"}, Inputs: map[string]Input{name: in}})
		if codeOf(err) != CodeInvalidArgument {
			t.Fatalf("%q: got %v", name, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("%d requests sent", hits.Load())
	}
}

// image.edit takes its pictures as a list under "images". This is the body
// the API's schema asks for: type, params with the model, inputs.images.
func TestImageEditSendsItsImagesAsAList(t *testing.T) {
	var body map[string]any
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(201)
		fmt.Fprint(w, `{"data":{"id":"job_e","status":"waiting"}}`)
	})
	created, err := c.Jobs.Create(context.Background(), CreateParams{
		Params: ImageEditQwenImageEdit2511Params{Prompt: "make the sky purple", Steps: Ptr[int64](30)},
		Inputs: map[string]Input{"images": URLs{"https://example.com/photo.jpg"}},
	})
	if err != nil || created.ID != "job_e" {
		t.Fatalf("%+v %v", created, err)
	}
	params, _ := body["params"].(map[string]any)
	images, _ := body["inputs"].(map[string]any)["images"].([]any)
	if body["type"] != "image.edit" || params["model"] != "qwen-image-edit-2511" || params["steps"] != float64(30) ||
		len(images) != 1 || images[0] != "https://example.com/photo.jpg" {
		t.Fatalf("body %v", body)
	}
}
