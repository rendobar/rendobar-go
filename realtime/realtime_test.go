package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/rendobar/rendobar-go"
)

// hubPath is the path of the last handshake.
var hubPath atomic.Value

// hub plays the API's event socket. Each connection calls serve with the
// lastEventId the client's init carried.
func hub(t *testing.T, serve func(ctx context.Context, conn *websocket.Conn, n int32, lastEventID int64)) (*Client, *atomic.Int32) {
	t.Helper()
	var conns atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer rb_test" || r.URL.Query().Get("token") != "" {
			t.Errorf("handshake auth %q, query %q", r.Header.Get("Authorization"), r.URL.RawQuery)
		}
		hubPath.Store(r.URL.Path)
		n := conns.Add(1)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.CloseNow()
		_, raw, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		var init struct {
			Type        string `json:"type"`
			LastEventID int64  `json:"lastEventId"`
		}
		if json.Unmarshal(raw, &init) != nil || init.Type != "init" {
			t.Errorf("first message %s, want init", raw)
		}
		serve(r.Context(), conn, n, init.LastEventID)
	}))
	t.Cleanup(srv.Close)
	return New(WithAPIKey("rb_test"), WithBaseURL(srv.URL)), &conns
}

func send(ctx context.Context, conn *websocket.Conn, frames ...string) {
	for _, f := range frames {
		if conn.Write(ctx, websocket.MessageText, []byte(f)) != nil {
			return
		}
	}
}

func fast(t *testing.T) {
	min0, max0 := minDelay, maxDelay
	minDelay, maxDelay = 10*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { minDelay, maxDelay = min0, max0 })
}

func TestEventsReadsEveryKindAndToleratesTheRest(t *testing.T) {
	c, _ := hub(t, func(ctx context.Context, conn *websocket.Conn, _ int32, last int64) {
		if last != 7 {
			t.Errorf("init lastEventId %d, want 7", last)
		}
		send(ctx, conn,
			`{"type":"job.created","jobId":"job_1","jobType":"ffmpeg","status":"waiting","eventId":8}`,
			`{"type":"balance.updated","balance":9000000000000,"balanceFormatted":"$9000.00","timestamp":1763700000000,"eventId":9}`,
			`{"type":"replay.done"}`,
			`{"type":"job.progress","jobId":"job_1","timestamp":1763700000000,"progress":null,"phase":"processing","step":"ffmpeg","metrics":{"fps":29.5,"speed":"1.2x"}}`,
			`{"type":"job.metrics","jobId":"job_1","timestamp":1,"cpuCores":2,"memUsedMB":100,"gpuPct":null}`,
			`{"type":"notification","id":"ntf_1","severity":"warning","title":"t","message":"m","id":10}`,
			`{"type":"member.updated","userId":"u_1","role":null,"reason":"left","timestamp":1,"eventId":11}`,
			`not json`,
			`{"type":"job.teleported","jobId":"job_1","eventId":12}`,
			`{"type":"job.log","jobId":"job_1","level":3,"message":"m"}`,
			`{"type":"resync","reason":"buffer_overflow"}`,
		)
		<-ctx.Done()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got []Event
	for ev, err := range c.Events(ctx, EventsParams{LastEventID: 7}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, ev)
		if len(got) == 11 {
			break
		}
	}
	if _, ok := got[0].(*Connected); !ok {
		t.Fatalf("first %T, want *Connected", got[0])
	}
	if e, ok := got[1].(*JobCreated); !ok || e.JobID != "job_1" || e.JobType != rendobar.JobTypeFfmpeg || e.EventID() != 8 {
		t.Errorf("job.created %+v", got[1])
	}
	if e, ok := got[2].(*BalanceUpdated); !ok || e.Balance != 9000000000000 || e.Timestamp.Time().IsZero() {
		t.Errorf("balance %+v", got[2])
	}
	if _, ok := got[3].(*ReplayDone); !ok {
		t.Errorf("%T, want *ReplayDone", got[3])
	}
	if e, ok := got[4].(*JobProgress); !ok || e.Progress != nil || e.Phase != PhaseProcessing || *e.Metrics["fps"].Number != 29.5 || e.Metrics["speed"].Text != "1.2x" || e.EventID() != 0 {
		t.Errorf("progress %+v", got[4])
	}
	if e, ok := got[5].(*JobResources); !ok || e.CPUCores != 2 || e.GPUPct != nil || e.Type() != "job.resources" {
		t.Errorf("resources %+v", got[5])
	}
	// An older hub's numeric id is the cursor. The notification's own id is a string.
	if e, ok := got[6].(*Notification); !ok || e.EventID() != 10 || e.Severity != rendobar.NotificationSeverityWarning {
		t.Errorf("notification %+v", got[6])
	}
	if e, ok := got[7].(*MemberUpdated); !ok || e.Role != nil || e.Reason != LeaveSelf {
		t.Errorf("member %+v", got[7])
	}
	if e, ok := got[8].(*Unknown); !ok || e.Type() != "job.teleported" || e.EventID() != 12 {
		t.Errorf("newer event %+v", got[8])
	}
	if e, ok := got[9].(*Unknown); !ok || e.Type() != "job.log" {
		t.Errorf("a log of another shape %+v, want *Unknown", got[9])
	}
	if e, ok := got[10].(*Resync); !ok || e.Reason != ResyncBufferOverflow {
		t.Errorf("resync %+v", got[10])
	}
}

func TestEventsReconnectsAndResumesAfterTheLastEvent(t *testing.T) {
	fast(t)
	c, conns := hub(t, func(ctx context.Context, conn *websocket.Conn, n int32, last int64) {
		switch n {
		case 1:
			send(ctx, conn, `{"type":"job.result","jobId":"job_1","eventId":1}`, `{"type":"job.result","jobId":"job_2","eventId":2}`)
			conn.CloseNow() // the connection drops
		default:
			if last != 2 {
				t.Errorf("reconnect resumed after %d, want 2", last)
			}
			send(ctx, conn, `{"type":"job.result","jobId":"job_2","eventId":2}`, `{"type":"job.result","jobId":"job_3","eventId":3}`)
			<-ctx.Done()
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var ids []string
	connected := 0
	for ev, err := range c.Events(ctx, EventsParams{}) {
		if err != nil {
			t.Fatal(err)
		}
		switch e := ev.(type) {
		case *Connected:
			connected++
		case *JobResult:
			ids = append(ids, e.JobID)
		}
		if len(ids) == 3 {
			break
		}
	}
	if len(ids) != 3 || ids[2] != "job_3" || connected != 2 || conns.Load() != 2 {
		t.Fatalf("results %v after %d connections (%d yielded)", ids, conns.Load(), connected)
	}
}

func TestJobEndsOnItsFinalStatus(t *testing.T) {
	c, _ := hub(t, func(ctx context.Context, conn *websocket.Conn, _ int32, _ int64) {
		send(ctx, conn,
			`{"type":"balance.updated","balance":5,"eventId":1}`,
			`{"type":"job.status","jobId":"job_2","status":"complete","eventId":2}`,
			`{"type":"replay.done"}`,
			`{"type":"job.status","jobId":"job_1","status":"running"}`,
			`{"type":"job.log","jobId":"job_1","level":"info","message":"go"}`,
			`{"type":"job.status","jobId":"job_1","status":"complete","price":1500000}`,
			`{"type":"job.log","jobId":"job_1","level":"info","message":"after the end"}`,
		)
		<-ctx.Done()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var types []string
	for ev, err := range c.Job(ctx, "job_1") {
		if err != nil {
			t.Fatal(err)
		}
		types = append(types, ev.Type())
		if s, ok := ev.(*JobStatusChanged); ok && s.Final() && (s.Price == nil || *s.Price != 1500000) {
			t.Errorf("final status %+v", s)
		}
	}
	if p := hubPath.Load(); p != "/events/ws/job/job_1" {
		t.Errorf("path %v", p)
	}
	// The workspace's other events, another job's end among them, are left out.
	if want := []string{"connected", "job.status", "job.log", "job.status"}; len(types) != len(want) || types[1] != "job.status" || types[3] != "job.status" {
		t.Fatalf("events %v, want %v", types, want)
	}
}

func TestARefusalEndsTheStreamWithItsError(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("X-Request-Id", "req_1")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"INSUFFICIENT_SCOPE","message":"needs jobs:read"}}`))
	}))
	defer srv.Close()
	c := New(WithAPIKey("rb_test"), WithBaseURL(srv.URL))
	var errs []error
	for _, err := range c.Events(context.Background(), EventsParams{}) {
		errs = append(errs, err)
	}
	var apiErr *rendobar.Error
	if len(errs) != 1 || !errors.As(errs[0], &apiErr) || apiErr.Code != rendobar.CodeInsufficientScope || apiErr.StatusCode != 403 || apiErr.RequestID != "req_1" {
		t.Fatalf("errors %v", errs)
	}
	if hits.Load() != 1 {
		t.Fatalf("%d handshakes, want 1", hits.Load())
	}
}

func TestAServerErrorIsRetried(t *testing.T) {
	fast(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		_, _, _ = conn.Read(r.Context())
		<-r.Context().Done()
	}))
	defer srv.Close()
	c := New(WithAPIKey("rb_test"), WithBaseURL(srv.URL))
	for ev, err := range c.Events(context.Background(), EventsParams{}) {
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := ev.(*Connected); ok {
			break
		}
	}
	if hits.Load() != 3 {
		t.Fatalf("%d handshakes, want 3", hits.Load())
	}
}

func TestAStreamEndsWithTheContext(t *testing.T) {
	c, _ := hub(t, func(ctx context.Context, _ *websocket.Conn, _ int32, _ int64) { <-ctx.Done() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var last error
	for ev, err := range c.Events(ctx, EventsParams{}) {
		if _, ok := ev.(*Connected); ok {
			cancel()
		}
		last = err
	}
	if !errors.Is(last, context.Canceled) {
		t.Fatalf("ended with %v, want context.Canceled", last)
	}
}

func TestBadKeysAndIDsFailWithoutConnecting(t *testing.T) {
	t.Setenv("RENDOBAR_API_KEY", "")
	for name, tc := range map[string]struct {
		seq  func() (Event, error)
		code rendobar.ErrorCode
	}{
		"no key":     {func() (Event, error) { return first(New().Events(context.Background(), EventsParams{})) }, rendobar.CodeMissingAPIKey},
		"not rb_":    {func() (Event, error) { return first(New(WithAPIKey("sk_x")).Job(context.Background(), "job_1")) }, rendobar.CodeInvalidAPIKey},
		"bad job id": {func() (Event, error) { return first(New(WithAPIKey("rb_x")).Job(context.Background(), "../x")) }, rendobar.CodeInvalidArgument},
	} {
		ev, err := tc.seq()
		var apiErr *rendobar.Error
		if ev != nil || !errors.As(err, &apiErr) || apiErr.Code != tc.code || apiErr.StatusCode != 0 {
			t.Errorf("%s: %v %v, want %s", name, ev, err, tc.code)
		}
	}
}

func first(seq func(func(Event, error) bool)) (Event, error) {
	for ev, err := range seq {
		return ev, err
	}
	return nil, nil
}
