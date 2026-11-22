// Package realtime streams a Rendobar workspace's events as they happen: job
// progress, logs and status, balance and notification changes.
//
// It is a separate package so its WebSocket dependency is built only into
// programs that import it. Follow one job to its end:
//
//	rt := realtime.New() // reads RENDOBAR_API_KEY
//	for ev, err := range rt.Job(ctx, jobID) {
//		if err != nil {
//			return err
//		}
//		switch e := ev.(type) {
//		case *realtime.JobProgress:
//			fmt.Println(e.Phase, e.Progress)
//		case *realtime.JobStatusChanged:
//			fmt.Println("status", e.Status)
//		}
//	}
//
// The stream reconnects on its own and resumes where it left off, so a
// dropped connection costs no events the workspace keeps for replay.
package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/rendobar/rendobar-go"
)

// Client opens event streams. Make one with New and reuse it: it is safe for concurrent use.
type Client struct {
	wsURL   string
	header  http.Header
	http    *http.Client
	confErr error // a missing or malformed key, returned by every stream
}

type config struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

// Option configures a Client.
type Option func(*config)

// WithAPIKey sets the API key. Without it, the client reads RENDOBAR_API_KEY.
// The key needs the jobs:read scope.
func WithAPIKey(key string) Option { return func(c *config) { c.apiKey = key } }

// WithBaseURL points the client at another API host, as rendobar.WithBaseURL
// does. The default is rendobar.DefaultBaseURL. http becomes ws and https wss.
func WithBaseURL(u string) Option {
	return func(c *config) { c.baseURL = strings.TrimRight(u, "/") }
}

// WithHTTPClient sets the HTTP client the WebSocket handshake goes through.
func WithHTTPClient(hc *http.Client) Option { return func(c *config) { c.httpClient = hc } }

// New returns a client. With no WithAPIKey it reads RENDOBAR_API_KEY. A client
// with no key, or one that does not start with rb_, yields a *rendobar.Error
// with code MISSING_API_KEY or INVALID_API_KEY from every stream, without connecting.
func New(opts ...Option) *Client {
	cfg := config{baseURL: rendobar.DefaultBaseURL}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.apiKey == "" {
		cfg.apiKey = os.Getenv("RENDOBAR_API_KEY")
	}
	c := &Client{http: cfg.httpClient, header: http.Header{}}
	switch {
	case cfg.apiKey == "":
		c.confErr = &rendobar.Error{Code: rendobar.CodeMissingAPIKey, Message: "no API key: set RENDOBAR_API_KEY or pass realtime.WithAPIKey"}
	case !strings.HasPrefix(cfg.apiKey, "rb_"):
		c.confErr = &rendobar.Error{Code: rendobar.CodeInvalidAPIKey, Message: "the API key does not start with rb_: check RENDOBAR_API_KEY or the key passed to realtime.WithAPIKey"}
	}
	// A header, not the ?token= query a browser has to use, so the key stays out of URLs and logs.
	c.header.Set("Authorization", "Bearer "+cfg.apiKey)
	c.header.Set("X-Rendobar-Client", fmt.Sprintf("go/%s (go/%s; realtime)", rendobar.Version, strings.TrimPrefix(runtime.Version(), "go")))
	switch {
	case strings.HasPrefix(cfg.baseURL, "https://"):
		c.wsURL = "wss://" + strings.TrimPrefix(cfg.baseURL, "https://")
	case strings.HasPrefix(cfg.baseURL, "http://"):
		c.wsURL = "ws://" + strings.TrimPrefix(cfg.baseURL, "http://")
	default:
		c.wsURL = cfg.baseURL
	}
	return c
}

// EventsParams starts Client.Events.
type EventsParams struct {
	// LastEventID resumes after the event with this EventID: the workspace
	// replays what it kept since then. 0 replays everything it keeps.
	LastEventID int64
}

// Events yields every event of the workspace until ctx ends, then the
// context's error. It yields *Connected on each connection, the replayed
// events, *ReplayDone, then live events. A dropped connection reconnects with
// backoff and resumes after the last event yielded, and an event seen before
// is never yielded twice. Breaking out of the loop closes the connection.
//
// A refusal that no retry fixes, such as a revoked key (401), a key without
// the jobs:read scope (403) or a malformed key, is yielded as a
// *rendobar.Error and ends the stream.
func (c *Client) Events(ctx context.Context, p EventsParams) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		c.stream(ctx, "/events/ws", p.LastEventID, nil, nil, yield)
	}
}

var jobID = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Job yields one job's events: *Connected, then its progress, steps, logs,
// resources, previews, status and result, replayed ones first. It leaves out
// the workspace's other events. It returns after the job reaches a final
// status, the *JobStatusChanged for it included, so a job that already ended
// returns after its replayed status. It ends early, with the context's error,
// when ctx ends. A job the workspace does not have is a NOT_FOUND *rendobar.Error.
func (c *Client) Job(ctx context.Context, id string) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		if !jobID.MatchString(id) || id == "." || id == ".." {
			yield(nil, &rendobar.Error{Code: rendobar.CodeInvalidArgument, Message: fmt.Sprintf("job id %q is not valid: use letters, digits, '.', '_' and '-'", id)})
			return
		}
		keep := func(ev Event) bool { return aboutJob(ev, id) }
		final := func(ev Event) bool {
			s, ok := ev.(*JobStatusChanged)
			return ok && s.JobID == id && s.Final()
		}
		c.stream(ctx, "/events/ws/job/"+url.PathEscape(id), 0, keep, final, yield)
	}
}

// Backoff between reconnects. Variables, so the tests can shorten them.
var (
	minDelay = time.Second
	maxDelay = 30 * time.Second
)

// readLimit bounds one message. A preview frame is up to 64 KB of base64.
const readLimit = 1 << 20

// stream runs one stream until yield stops it, last(ev) is true, ctx ends or
// the API refuses the handshake for good. keep, when set, picks the events to yield.
func (c *Client) stream(ctx context.Context, path string, lastID int64, keep, last func(Event) bool, yield func(Event, error) bool) {
	if c.confErr != nil {
		yield(nil, c.confErr)
		return
	}
	delay := minDelay
	for {
		connected, err := c.connect(ctx, path, &lastID, keep, last, yield)
		if connected {
			delay = minDelay // a working connection starts the backoff over
		}
		var done stopped
		switch {
		case errors.As(err, &done):
			return
		case ctx.Err() != nil:
			yield(nil, ctx.Err())
			return
		case permanent(err):
			yield(nil, err)
			return
		}
		timer := time.NewTimer(time.Duration(float64(delay) * (0.9 + 0.2*rand.Float64())))
		select {
		case <-ctx.Done():
			timer.Stop()
			yield(nil, ctx.Err())
			return
		case <-timer.C:
		}
		delay = min(delay*3/2, maxDelay)
	}
}

// stopped ends a stream on purpose: the caller broke out, or the job ended.
type stopped struct{}

func (stopped) Error() string { return "realtime: stream stopped" }

// connect runs one connection. connected reports that the handshake
// succeeded. err is stopped when the stream should end, else why the
// connection ended.
func (c *Client) connect(ctx context.Context, path string, lastID *int64, keep, last func(Event) bool, yield func(Event, error) bool) (connected bool, err error) {
	conn, res, err := websocket.Dial(ctx, c.wsURL+path, &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: c.header})
	if err != nil {
		if res != nil && res.StatusCode != http.StatusSwitchingProtocols {
			return false, handshakeError(res)
		}
		return false, err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(readLimit)
	init, err := json.Marshal(struct {
		Type        string `json:"type"`
		LastEventID int64  `json:"lastEventId"`
	}{"init", *lastID})
	if err != nil {
		return true, err
	}
	if err := conn.Write(ctx, websocket.MessageText, init); err != nil {
		return true, err
	}
	if !yield(&Connected{}, nil) {
		return true, stopped{}
	}
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			return true, err
		}
		if kind != websocket.MessageText {
			continue
		}
		ev, id, ok := decode(data)
		if !ok {
			continue // not an event at all
		}
		if id != 0 {
			if id <= *lastID {
				continue // replayed again after a reconnect: already yielded
			}
			*lastID = id
		}
		if keep != nil && !keep(ev) {
			continue
		}
		if !yield(ev, nil) || (last != nil && last(ev)) {
			return true, stopped{}
		}
	}
}

// handshakeError reads the API's error envelope from a refused handshake.
func handshakeError(res *http.Response) *rendobar.Error {
	out := &rendobar.Error{
		Code:       rendobar.CodeUnknown,
		Message:    fmt.Sprintf("the event stream was refused with status %d", res.StatusCode),
		StatusCode: res.StatusCode,
		RequestID:  res.Header.Get("X-Request-Id"),
	}
	if res.Body == nil {
		return out
	}
	body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	var env struct {
		Error *struct {
			Code    rendobar.ErrorCode `json:"code"`
			Message string             `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &env) == nil && env.Error != nil {
		if env.Error.Code != "" {
			out.Code = env.Error.Code
		}
		if env.Error.Message != "" {
			out.Message = env.Error.Message
		}
	}
	return out
}

// permanent reports a refusal that reconnecting cannot fix: a 4xx other than
// 408 and 429, or an error the SDK raised itself.
func permanent(err error) bool {
	var apiErr *rendobar.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	s := apiErr.StatusCode
	return s == 0 || (s >= 400 && s < 500 && s != http.StatusRequestTimeout && s != http.StatusTooManyRequests)
}
