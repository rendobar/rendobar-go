package rendobar

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rendobar/rendobar-go/internal/api"
)

const completeJob = `{"data":{"id":"job_c","orgId":"org_1","type":"ffmpeg","status":"complete","source":"api",
"client":"sdk","retryCount":0,"label":"thumb","inputs":{},"params":{"command":"ffmpeg -i in.mp4 out.mp4"},
"cost":{"amount":500000000,"currency":"USD","formatted":"$0.50"},"outputCategory":"video",
"steps":[{"id":"s1","name":"render","status":"complete","startedAt":1763700000000,"completedAt":1763700001500,"durationMs":1500}],
"logsAvailable":true,"resourcesAvailable":false,"timeoutMs":600000,"createdAt":1763700000000,
"completedAt":1763700002000,"webUrl":"https://app.example/jobs/job_c","callback":{"status":"delivered"},
"output":{"data":null,"file":{"url":"https://cdn.example/out.mp4","path":"out.mp4","type":"video","size":2048,
"sha256":"ab","meta":{"width":1280,"height":720,"durationMs":5000}},
"files":[{"url":"https://cdn.example/out.mp4","path":"out.mp4","type":"video","size":2048}],"expiresAt":1763703600000}}}`

func TestJobReadsEveryField(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, completeJob) })
	job, err := c.Jobs.Get(context.Background(), "job_c")
	if err != nil {
		t.Fatal(err)
	}
	created := time.UnixMilli(1763700000000)
	if job.ID() != "job_c" || job.Type() != "ffmpeg" || job.Status() != StatusComplete || !job.CreatedAt().Equal(created) {
		t.Fatalf("accessors: %s %s %s %v", job.ID(), job.Type(), job.Status(), job.CreatedAt())
	}
	if job.Info().Label != "thumb" || job.Complete == nil || job.Waiting != nil {
		t.Fatalf("variant: %+v", job)
	}
	got := job.Complete
	if got.ID != "job_c" || got.Timeout != 10*time.Minute || got.CallbackStatus != "delivered" || !got.DispatchedAt.IsZero() {
		t.Fatalf("info: %+v", got.JobInfo)
	}
	if got.Cost == nil || got.Cost.Nanodollars != 500000000 || got.Cost.Formatted != "$0.50" {
		t.Fatalf("cost: %+v", got.Cost)
	}
	if len(got.Steps) != 1 || got.Steps[0].Duration != 1500*time.Millisecond {
		t.Fatalf("steps: %+v", got.Steps)
	}
	f := got.Output.File
	if f == nil || f.URL != "https://cdn.example/out.mp4" || f.Size != 2048 || f.Width != 1280 || f.Duration != 5*time.Second {
		t.Fatalf("file: %+v", f)
	}
	if len(got.Output.Files) != 1 || !got.Output.ExpiresAt.Equal(time.UnixMilli(1763703600000)) || got.Output.Data != nil {
		t.Fatalf("output: %+v", got.Output)
	}
}

func TestListedJobsAnswerWithoutATypeSwitch(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[
{"id":"job_1","type":"ffprobe","status":"complete","createdAt":1,"output":{"data":{"format":{}},"files":[]}},
{"id":"job_2","type":"ffmpeg","status":"failed","createdAt":2,"error":{"code":"INPUT_UNREADABLE","message":"bad"}},
{"id":"job_3","type":"ffmpeg","status":"dispatched","createdAt":3,"progress":0.5,"eta":4,"bootState":{"phase":"booting","detail":"starting","elapsedMs":250}},
{"id":"job_4","type":"ffmpeg","status":"paused","createdAt":4}],"meta":{"total":4}}`)
	})
	page, err := c.Jobs.List(context.Background(), ListParams{})
	if err != nil {
		t.Fatal(err)
	}
	want := []JobStatus{StatusComplete, StatusFailed, StatusDispatched, "paused"}
	for i, job := range page.Jobs {
		if job.ID() != fmt.Sprintf("job_%d", i+1) || job.Status() != want[i] || job.CreatedAt().UnixMilli() != int64(i+1) {
			t.Fatalf("job %d: %s %s %v", i, job.ID(), job.Status(), job.CreatedAt())
		}
	}
	if page.Jobs[0].Complete.Output.Data == nil || page.Jobs[0].Complete.Output.File != nil {
		t.Fatalf("ffprobe output: %+v", page.Jobs[0].Complete.Output)
	}
	if page.Jobs[1].Failed.Error.Code != "INPUT_UNREADABLE" {
		t.Fatalf("error: %+v", page.Jobs[1].Failed.Error)
	}
	d := page.Jobs[2].Dispatched
	if *d.Progress != 0.5 || d.ETA != 4*time.Second || d.Boot == nil || d.Boot.Elapsed != 250*time.Millisecond {
		t.Fatalf("dispatched: %+v %+v", d, d.Boot)
	}
	future := page.Jobs[3]
	if future.Waiting != nil || future.Dispatched != nil || future.Running != nil || future.Complete != nil || future.Failed != nil || future.Cancelled != nil {
		t.Fatalf("unknown status set a variant: %+v", future)
	}
}

// Against the conformance mock, which answers from the spec's own examples.
func TestSchemaTypesAndCapabilitiesReadTheSpecShapes(t *testing.T) {
	reset(t, "")
	c := client()
	types, err := c.Jobs.Types(context.Background())
	if err != nil || len(types) == 0 || types[0].Type == "" {
		t.Fatalf("types %+v, %v", types, err)
	}
	schema, err := c.Jobs.Schema(context.Background(), types[0].Type)
	if err != nil || schema.Type == "" {
		t.Fatalf("schema %+v, %v", schema, err)
	}
	caps, err := c.Account.Capabilities(context.Background())
	if err != nil || caps.Plan.Slug == "" {
		t.Fatalf("capabilities %+v, %v", caps, err)
	}
	t.Logf("type %s, %d fields, %d inputs, plan %s, %d concurrent", schema.Type, len(schema.Fields), len(schema.Inputs.Fields), caps.Plan.Slug, caps.Limits.ConcurrentJobs)
}

// Every field the generated core knows must be read by the public types, or
// listed here as left out on purpose. A spec change that adds or renames a
// field fails this test, so a person decides whether users get it.
func TestPublicTypesReadEveryGeneratedField(t *testing.T) {
	skipped := map[string]bool{
		"metricsAvailable": true, // deprecated, renamed to resourcesAvailable
		"maxJobInputSize":  true, // deprecated, always equal to maxInputFileSize
		"order":            true, // fields already come in display order
	}
	pairs := []struct {
		generated any
		public    reflect.Type
	}{
		{api.JobWaiting{}, reflect.TypeOf(wireJob{})},
		{api.JobDispatched{}, reflect.TypeOf(wireJob{})},
		{api.JobRunning{}, reflect.TypeOf(wireJob{})},
		{api.JobComplete{}, reflect.TypeOf(wireJob{})},
		{api.JobFailed{}, reflect.TypeOf(wireJob{})},
		{api.JobCancelled{}, reflect.TypeOf(wireJob{})},
		{api.JobCompleteCost{}, reflect.TypeOf(wireCost{})},
		{api.JobCompleteStepsItem{}, reflect.TypeOf(wireStep{})},
		{api.JobCompleteDeliveriesItem{}, reflect.TypeOf(wireDelivery{})},
		{api.JobCompleteCallback{}, reflect.TypeOf(wireJob{}.Callback).Elem()},

		{api.JobDispatchedBootState{}, reflect.TypeOf(wireBootState{})},
		{api.JobCompleteOutput{}, reflect.TypeOf(wireOutput{})},
		{api.JobCompleteOutputFile{}, reflect.TypeOf(wireFile{})},
		{api.JobCompleteOutputFilesItem{}, reflect.TypeOf(wireFile{})},
		{api.JobCompleteOutputFileMeta{}, reflect.TypeOf(wireFile{}.Meta).Elem()},

		{api.JobFailedError{}, reflect.TypeOf(wireJobError{})},
		{api.JobType{}, reflect.TypeOf(JobType{})},
		{api.GetJobTypeSchemaResponseData{}, reflect.TypeOf(JobTypeSchema{})},
		{api.GetJobTypeSchemaResponseDataInputs{}, reflect.TypeOf(SchemaInputs{})},
		{api.ConnectorField{}, reflect.TypeOf(SchemaField{})},
		{api.ConnectorInput{}, reflect.TypeOf(SchemaField{})},
		{api.ConnectorFieldOptionsItem{}, reflect.TypeOf(SchemaOption{})},
		{api.ConnectorFieldShowWhen{}, reflect.TypeOf(ShowWhen{})},
		{api.GetAccountCapabilitiesResponseData{}, reflect.TypeOf(AccountCapabilities{})},
		{api.GetAccountCapabilitiesResponseDataPlan{}, reflect.TypeOf(Plan{})},
		{api.GetAccountCapabilitiesResponseDataLimits{}, reflect.TypeOf(wireLimits{})},
		{api.GetAccountCapabilitiesResponseDataFeatures{}, reflect.TypeOf(Features{})},
	}
	for _, p := range pairs {
		read := jsonNames(p.public)
		for name := range jsonNames(reflect.TypeOf(p.generated)) {
			if !read[name] && !skipped[name] {
				t.Errorf("%T.%s is not read by %v", p.generated, name, p.public)
			}
		}
	}
}

func jsonNames(t reflect.Type) map[string]bool {
	names := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			names[name] = true
		}
	}
	return names
}

// The output of a real ffprobe run on the sample file, trimmed to its summary.
const ffprobeJob = `{"data":{"id":"job_p","type":"ffprobe","status":"complete","createdAt":1,
"output":{"data":{"summary":{"container":"mp4","formatLongName":"QuickTime / MOV","durationSec":5.013,
"sizeBytes":352121,"bitrateBps":561932,"startTimeSec":0,"streamCounts":{"video":1,"audio":1,"subtitle":0,
"data":0,"attachment":0},"tags":{"encoder":"Lavf62.3.100"},"kind":"video","video":{"codec":"h264",
"profile":"High","width":1280,"height":720,"displayAspectRatio":"16:9","pixelFormat":"yuv420p","bitDepth":8,
"fps":24,"isVariableFrameRate":false,"rotation":0,"isHdr":false,"bitrateBps":459337,"language":null},
"audio":{"codec":"aac","profile":"LC","channels":2,"channelLayout":"stereo","sampleRate":48000,
"bitrateBps":95699,"language":null},"brandNewField":true},"format":{"format_name":"mov,mp4"},"streams":[]},
"files":[]}}}`

func TestFfprobeReadsTheSummary(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, ffprobeJob) })
	job, err := c.Jobs.Get(context.Background(), "job_p")
	if err != nil {
		t.Fatal(err)
	}
	probe, err := job.Complete.Ffprobe()
	if err != nil {
		t.Fatal(err)
	}
	s := probe.Summary
	if s == nil || s.Kind != ProbeKindVideo || s.Duration != 5013*time.Millisecond || s.SizeBytes != 352121 ||
		s.StreamCounts.Audio != 1 || s.Tags["encoder"] != "Lavf62.3.100" {
		t.Fatalf("summary %+v", s)
	}
	if v := s.Video; v == nil || v.Width != 1280 || v.FPS != 24 || v.Codec != "h264" || v.Language != "" {
		t.Fatalf("video %+v", v)
	}
	if a := s.Audio; a == nil || a.SampleRate != 48000 || a.Channels != 2 {
		t.Fatalf("audio %+v", a)
	}
	if string(probe.Format) != `{"format_name":"mov,mp4"}` || probe.Chapters != nil {
		t.Fatalf("raw sections %s %s", probe.Format, probe.Chapters)
	}
}

func TestFfprobeRefusesAnotherJobType(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, completeJob) })
	job, err := c.Jobs.Get(context.Background(), "job_c")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := job.Complete.Ffprobe(); err == nil || !strings.Contains(err.Error(), "not ffprobe") {
		t.Fatalf("got %v", err)
	}
}

func TestStatusIsTerminal(t *testing.T) {
	for s, want := range map[JobStatus]bool{StatusComplete: true, StatusFailed: true, StatusCancelled: true,
		StatusRunning: false, StatusWaiting: false, "paused": false} {
		if s.IsTerminal() != want {
			t.Fatalf("%s: %v", s, !want)
		}
	}
}
