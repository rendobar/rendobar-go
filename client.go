// Package rendobar is the Go client for the Rendobar API: media processing and AI generation.
//
//	rb := rendobar.New() // reads RENDOBAR_API_KEY
//	job, err := rb.Jobs.Run(ctx, rendobar.CreateParams{
//		Type:   "ffprobe",
//		Params: map[string]any{"command": "https://cdn.rendobar.com/assets/examples/sample.mp4"},
//	}, nil)
//
// The generated core under internal/api is built from the published OpenAPI
// document. This package adds what every Rendobar SDK promises on top: keyed
// submits that a retry cannot duplicate, waiting for a job, typed errors with
// the request id, deprecation notices, and the environment key.
package rendobar

import (
	"fmt"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	apiclient "github.com/rendobar/rendobar-go/internal/api/client"
	"github.com/rendobar/rendobar-go/internal/api/option"
)

// DefaultBaseURL is the production API.
const DefaultBaseURL = "https://api.rendobar.com"

const requestIDHeader = "X-Request-Id"

var appName = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,32}$`)

// DeprecationNotice reports that an endpoint the client called is deprecated.
// It keeps working until the next major release.
type DeprecationNotice struct {
	Method       string
	Path         string
	DeprecatedAt time.Time
}

// Client is the Rendobar API client. Make one and reuse it: it is safe for concurrent use.
type Client struct {
	Jobs    *Jobs
	Account *Account
	core    *apiclient.Client
}

type config struct {
	apiKey        string
	baseURL       string
	httpClient    *http.Client
	timeout       time.Duration
	maxRetries    int
	app           string
	onDeprecation func(DeprecationNotice)
}

// Option configures a Client.
type Option func(*config)

// WithAPIKey sets the API key. Without it, the client reads RENDOBAR_API_KEY.
func WithAPIKey(key string) Option { return func(c *config) { c.apiKey = key } }

// WithBaseURL points the client at another API host, such as a test server.
func WithBaseURL(url string) Option {
	return func(c *config) { c.baseURL = strings.TrimRight(url, "/") }
}

// WithHTTPClient sets the HTTP client. The client copies it, so the one you
// pass is never changed. Its Timeout bounds each attempt unless WithTimeout is also given.
func WithHTTPClient(hc *http.Client) Option { return func(c *config) { c.httpClient = hc } }

// WithTimeout bounds each request attempt, including reading the whole response
// body, so for Jobs.Download it bounds the whole download. The default is 30
// seconds. It wins over the Timeout of a WithHTTPClient client, in either order,
// and never changes that client.
func WithTimeout(d time.Duration) Option { return func(c *config) { c.timeout = d } }

// WithMaxRetries sets how many times a 429, a 5xx or a connection error is
// retried. The default is 2. 0 (or a negative number) sends each request once.
func WithMaxRetries(n int) Option {
	return func(c *config) {
		if n < 0 {
			n = 0
		}
		c.maxRetries = n
	}
}

// WithApp names the application built on this client, sent as app/<name> in X-Rendobar-Client.
func WithApp(name string) Option { return func(c *config) { c.app = name } }

// WithDeprecationHandler receives each deprecated endpoint once. The default logs to stderr.
func WithDeprecationHandler(fn func(DeprecationNotice)) Option {
	return func(c *config) { c.onDeprecation = fn }
}

// New returns a client. With no WithAPIKey it reads RENDOBAR_API_KEY.
func New(opts ...Option) *Client {
	cfg := config{baseURL: DefaultBaseURL, maxRetries: 2}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.apiKey == "" {
		cfg.apiKey = os.Getenv("RENDOBAR_API_KEY")
	}
	if cfg.app != "" && !appName.MatchString(cfg.app) {
		panic(`rendobar: WithApp name must be 1-32 letters, digits or . _ + -`)
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	if cfg.httpClient != nil {
		copied := *cfg.httpClient // never mutate the caller's client
		hc = &copied
	}
	if cfg.timeout > 0 {
		hc.Timeout = cfg.timeout // applied after every option, so order does not matter
	}
	base := hc.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	hc.Transport = &transport{base: base, deprecations: newDeprecations(cfg.onDeprecation)}

	header := http.Header{}
	header.Set("X-Rendobar-Client", clientHeader(cfg.app))
	coreOpts := []option.RequestOption{
		option.WithBaseURL(cfg.baseURL),
		// This package owns retries (retry.go): the generated core would also
		// retry 408, and never a connection error.
		option.WithHTTPClient(&retrier{client: hc, maxRetries: cfg.maxRetries}),
		option.WithoutRetries(),
		option.WithHTTPHeader(header),
	}
	if cfg.apiKey != "" {
		coreOpts = append(coreOpts, option.WithToken(cfg.apiKey))
	}
	c := &Client{core: apiclient.NewClient(coreOpts...)}
	c.Jobs = &Jobs{client: c}
	c.Account = &Account{client: c}
	return c
}

func clientHeader(app string) string {
	extras := []string{"go/" + strings.TrimPrefix(runtime.Version(), "go")}
	if app != "" {
		extras = append(extras, "app/"+app)
	}
	return fmt.Sprintf("go/%s (%s)", Version, strings.Join(extras, "; "))
}

// transport reads the Deprecation header off every response. It never changes a request.
type transport struct {
	base         http.RoundTripper
	deprecations *deprecations
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := t.base.RoundTrip(req)
	if err == nil {
		t.deprecations.note(res, req.Method, req.URL.Path)
	}
	return res, err
}

var (
	knownID   = regexp.MustCompile(`^(job|batch|org|asset|wh|key|rb|upl)_[A-Za-z0-9_-]+$`)
	genericID = regexp.MustCompile(`^[a-z]+_[A-Za-z0-9]{8,}$`)
)

// The spec has no literal path segment with an underscore, so a lowercase word,
// "_" and 8 or more letters or digits is always an id.
//
// routeOf collapses every id-shaped path segment to :id, adjacent ones included,
// so one endpoint is one route however many ids it is called with.
func routeOf(path string) string {
	segs := strings.Split(path, "/")
	for i, seg := range segs {
		if knownID.MatchString(seg) || genericID.MatchString(seg) {
			segs[i] = ":id"
		}
	}
	return strings.Join(segs, "/")
}

type deprecations struct {
	mu      sync.Mutex
	seen    map[string]bool
	handler func(DeprecationNotice)
}

func newDeprecations(handler func(DeprecationNotice)) *deprecations {
	return &deprecations{seen: map[string]bool{}, handler: handler}
}

// note reports a deprecated route once: ids collapse to :id, so one endpoint is one notice.
func (d *deprecations) note(res *http.Response, method, path string) {
	at, ok := parseDeprecation(res.Header.Get("Deprecation"))
	if !ok {
		return
	}
	key := method + " " + routeOf(path)
	d.mu.Lock()
	first := !d.seen[key]
	d.seen[key] = true
	d.mu.Unlock()
	if !first {
		return
	}
	notice := DeprecationNotice{Method: method, Path: path, DeprecatedAt: at}
	defer func() { _ = recover() }() // a handler that panics must not break the call
	if d.handler != nil {
		d.handler(notice)
		return
	}
	fmt.Fprintf(os.Stderr, "[rendobar] %s %s is deprecated since %s. It keeps working until the next major release.\n", method, path, at.Format("2006-01-02"))
}

// parseDeprecation reads RFC 9745's "@<unix seconds>".
func parseDeprecation(v string) (time.Time, bool) {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "@") {
		return time.Time{}, false
	}
	secs, err := strconv.ParseInt(v[1:], 10, 64)
	if err != nil || secs < -62135596800 || secs > 253402300799 { // years 1 to 9999
		return time.Time{}, false
	}
	return time.Unix(secs, 0).UTC(), true
}
