package rendobar

// Every operation in the published spec is reachable from the public client,
// or specSkips says why it is left out.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
)

// specSkips are the operations the client leaves out on purpose, by method and path.
var specSkips = map[string]string{
	"GET /jobs/{id}/resources":                               "utilization telemetry for the dashboard's charts, not in @rendobar/sdk's surface",
	"GET /jobs/{id}/metrics":                                 "deprecated alias of GET /jobs/{id}/resources",
	"GET /jobs/{id}/throughput":                              "encode telemetry for the dashboard's charts, not in @rendobar/sdk's surface",
	"POST /jobs/{id}/dispute":                                "a charge dispute goes to manual review from the dashboard, not in @rendobar/sdk's surface",
	"GET /models":                                            "model catalogue for the dashboard, not in @rendobar/sdk's surface. Jobs.Schema describes each type's params",
	"POST /billing/checkout/pro":                             "deprecated, Billing.CheckoutPlan replaces it",
	"GET /billing/history/{id}/pdf":                          "a redirect to a PDF for the browser, not in @rendobar/sdk's surface",
	"GET /billing/history/{id}/receipt":                      "a redirect to a PDF for the browser, not in @rendobar/sdk's surface",
	"POST /orgs/current/exit-feedback":                       "the dashboard's form before a workspace is deleted",
	"GET /orgs/current/callback-secret":                      "not in @rendobar/sdk's surface, which this SDK matches",
	"POST /orgs/current/callback-secret/rotate":              "not in @rendobar/sdk's surface, which this SDK matches",
	"GET /orgs/current/audit-log":                            "not in @rendobar/sdk's surface, which this SDK matches",
	"POST /orgs/current/invitations":                         "bulk invite for the dashboard. Orgs.Members.Invite is the one way to invite",
	"POST /orgs/current/invitations/{invitationId}/resend":   "not in @rendobar/sdk's surface, which this SDK matches",
	"GET /invitations/{id}/preview":                          "public page read before sign-in, with no API key",
	"GET /assets/{id}/content":                               "a redirect for the browser. Assets.Download returns the short-lived URL",
	"GET /assets/{id}/preview":                               "thumbnail PNG for the dashboard",
	"POST /team/invite":                                      "deprecated, Orgs.Members.Invite replaces it",
	"PATCH /team/role":                                       "deprecated, Orgs.Members.ChangeRole replaces it",
	"POST /team/remove":                                      "deprecated, Orgs.Members.Remove replaces it",
	"POST /team/revoke-invitation":                           "deprecated, Orgs.Invitations.Revoke replaces it",
	"GET /upload-sessions/{token}":                           "upload page for MCP tools, authed by its token, not an API key",
	"POST /upload-sessions/{token}/files":                    "upload page for MCP tools, authed by its token, not an API key",
	"POST /upload-sessions/{token}/files/{assetId}/complete": "upload page for MCP tools, authed by its token, not an API key",
	"POST /upload-sessions/{token}/done":                     "upload page for MCP tools, authed by its token, not an API key",
}

// specOp is one operation of fern/openapi.json.
type specOp struct {
	key      string // "GET /jobs/{id}"
	id       string // operationId
	method   string
	match    *regexp.Regexp
	literals int // path segments that are not a parameter
}

var pathParam = regexp.MustCompile(`\{[^/}]+\}`)

func specOps(t *testing.T) []specOp {
	t.Helper()
	raw, err := os.ReadFile("fern/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	var ops []specOp
	for path, item := range spec.Paths {
		// Spec paths hold only letters, digits, '-', '/' and {params}, so no other character needs quoting.
		pattern := "^" + pathParam.ReplaceAllString(path, `[^/]+`) + "$"
		literals := 0
		for _, seg := range strings.Split(strings.Trim(path, "/"), "/") {
			if !pathParam.MatchString(seg) {
				literals++
			}
		}
		for method, body := range item {
			switch method {
			case "get", "post", "put", "patch", "delete", "head":
			default:
				continue // parameters, summary and the like
			}
			var op struct {
				OperationID string `json:"operationId"`
			}
			if err := json.Unmarshal(body, &op); err != nil {
				t.Fatalf("%s %s: %v", method, path, err)
			}
			m := strings.ToUpper(method)
			ops = append(ops, specOp{m + " " + path, op.OperationID, m, regexp.MustCompile(pattern), literals})
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].key < ops[j].key })
	return ops
}

// opRecorder records the method and path of every request it carries.
type opRecorder struct {
	mu   sync.Mutex
	seen []string
}

func (r *opRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.seen = append(r.seen, req.Method+" "+req.URL.EscapedPath())
	r.mu.Unlock()
	return http.DefaultTransport.RoundTrip(req)
}

func TestEverySpecOperationIsReachable(t *testing.T) {
	var requests []string
	for _, tc := range resourceCalls(t) {
		c, got, _ := recordingServer(t, tc.status, tc.resp)
		_ = tc.run(c) // TestEveryResourceMethodSendsAndReads checks the result
		if got.Method != "" {
			requests = append(requests, got.Method+" "+got.Path)
		}
	}
	// An upload sends more than one request, so it runs against the upload server.
	u := newUploadServer(t, `{"status":"presigned","data":{"id":"asset_1","status":"pending"},"upload":{"method":"PUT","url":"%s/put/whole","expiresAt":1}}`)
	rec := &opRecorder{}
	c := New(WithAPIKey("rb_test"), WithBaseURL(u.srv.URL), WithHTTPClient(&http.Client{Transport: rec}))
	if _, err := c.Uploads.Create(context.Background(), bytes.NewReader([]byte("0123456789")), 10, UploadParams{Filename: "clip.mp4"}); err != nil {
		t.Fatalf("upload: %v", err)
	}
	requests = append(requests, rec.seen...)

	ops := specOps(t)
	reached := map[string]bool{}
	for _, r := range requests {
		method, path, _ := strings.Cut(r, " ")
		// The most specific template wins, so GET /jobs/stats never counts as GET /jobs/{id}.
		var best *specOp
		for i := range ops {
			if op := &ops[i]; op.method == method && op.match.MatchString(path) && (best == nil || op.literals > best.literals) {
				best = op
			}
		}
		if best != nil {
			reached[best.key] = true
		}
	}

	inSpec := map[string]bool{}
	for _, op := range ops {
		inSpec[op.key] = true
		_, skipped := specSkips[op.key]
		switch {
		case reached[op.key] && skipped:
			t.Errorf("%s (%s) is reachable from the client: remove it from specSkips", op.key, op.id)
		case !reached[op.key] && !skipped:
			t.Errorf("%s (%s) is not reachable from the client: wrap it and add a call to resourceCalls, or add it to specSkips with a reason", op.key, op.id)
		}
	}
	for key := range specSkips {
		if !inSpec[key] {
			t.Errorf("specSkips lists %s, which the spec no longer has: remove it", key)
		}
	}
}
