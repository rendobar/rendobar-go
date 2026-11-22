package rendobar

// The resource methods: each sends the method, path, query and body the spec
// names, and reads the response the spec describes.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// sent is one request as the server saw it.
type sent struct {
	Method, Path, Query, Body string
}

// recordingServer answers every request with status and body, and records the last request.
func recordingServer(t *testing.T, status int, body string) (*Client, *sent, *atomic.Int32) {
	t.Helper()
	got := &sent{}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		*got = sent{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, string(b)}
		if r.Header.Get("Authorization") != "Bearer rb_test" {
			t.Errorf("%s %s without the API key", r.Method, r.URL.Path)
		}
		if len(b) > 0 && r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("%s %s sent a body as %q", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return New(WithAPIKey("rb_test"), WithBaseURL(srv.URL), WithMaxRetries(0)), got, &hits
}

func sameJSON(t *testing.T, got, want string) bool {
	t.Helper()
	if got == "" || want == "" {
		return got == want
	}
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("body %q is not JSON: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want %q is not JSON: %v", want, err)
	}
	return reflect.DeepEqual(g, w)
}

func sortedQuery(raw string) string {
	q, _ := url.ParseQuery(raw)
	return q.Encode()
}

// resourceCall is one call of a public method: the response that answers it,
// the request it must send, and the call itself, which checks what it read on t.
type resourceCall struct {
	name   string
	status int
	resp   string
	want   sent
	run    func(c *Client) error
}

// resourceCalls calls every public method that sends one request.
func resourceCalls(t *testing.T) []resourceCall {
	ctx := context.Background()
	return []resourceCall{
		// Jobs
		{"Jobs.Create", 201, `{"data":{"id":"job_1","status":"waiting"}}`,
			sent{"POST", "/jobs", "", `{"type":"ffprobe","params":{"command":"https://x.test/a.mp4"},"idempotencyKey":"key_1"}`}, func(c *Client) error {
				cr, err := c.Jobs.Create(ctx, CreateParams{Params: FfprobeParams{Command: "https://x.test/a.mp4"}, IdempotencyKey: "key_1"})
				if err == nil && (cr.ID != "job_1" || cr.Status != StatusWaiting || cr.Replayed) {
					t.Errorf("created %+v", cr)
				}
				return err
			}},
		{"Jobs.Get", 200, `{"data":{"id":"job_1","type":"ffprobe","status":"running"}}`,
			sent{"GET", "/jobs/job_1", "", ""}, func(c *Client) error {
				j, err := c.Jobs.Get(ctx, "job_1")
				if err == nil && (j.ID() != "job_1" || j.Running == nil) {
					t.Errorf("job %+v", j)
				}
				return err
			}},
		{"Jobs.List", 200, `{"data":[{"id":"job_1","type":"ffprobe","status":"failed"}],"meta":{"total":1,"limit":10,"offset":0}}`,
			sent{"GET", "/jobs", "limit=10&status=failed", ""}, func(c *Client) error {
				p, err := c.Jobs.List(ctx, ListParams{Status: StatusFailed, Limit: 10})
				if err == nil && (len(p.Jobs) != 1 || p.Jobs[0].Status() != StatusFailed || p.Total != 1) {
					t.Errorf("page %+v", p)
				}
				return err
			}},
		{"Jobs.Cancel", 200, `{"data":{"id":"job_1","status":"cancelled"}}`,
			sent{"POST", "/jobs/job_1/cancel", "", ""}, func(c *Client) error { return c.Jobs.Cancel(ctx, "job_1") }},
		{"Jobs.Download", 200, `abc`,
			sent{"GET", "/jobs/job_1/download", "", ""}, func(c *Client) error {
				d, err := c.Jobs.Download(ctx, "job_1")
				if err != nil {
					return err
				}
				defer d.Body.Close()
				b, err := io.ReadAll(d.Body)
				if err == nil && string(b) != "abc" {
					t.Errorf("body %q", b)
				}
				return err
			}},
		{"Jobs.Types", 200, `{"data":[{"type":"ffprobe","tag":"media","summary":"Probe a file"}]}`,
			sent{"GET", "/jobs/types", "", ""}, func(c *Client) error {
				ts, err := c.Jobs.Types(ctx)
				if err == nil && (len(ts) != 1 || ts[0].Type != JobTypeFfprobe) {
					t.Errorf("types %+v", ts)
				}
				return err
			}},
		{"Jobs.Schema", 200, `{"data":{"type":"ffprobe","fields":[],"inputs":{"fields":[]},"jsonSchema":{}}}`,
			sent{"GET", "/jobs/types/ffprobe/schema", "", ""}, func(c *Client) error {
				s, err := c.Jobs.Schema(ctx, JobTypeFfprobe)
				if err == nil && s.Type != JobTypeFfprobe {
					t.Errorf("schema %+v", s)
				}
				return err
			}},
		{"Jobs.Logs", 200, `{"data":[{"timestamp":1763700000000,"level":"info","event":"step.done","message":"ok","durationMs":3000000000,"meta":{"k":1}}]}`,
			sent{"GET", "/jobs/job_1/logs", "", ""}, func(c *Client) error {
				logs, err := c.Jobs.Logs(ctx, "job_1")
				if err == nil && (len(logs) != 1 || logs[0].Timestamp.Time().UnixMilli() != 1763700000000 || logs[0].DurationMs != 3000000000 || string(logs[0].Meta) != `{"k":1}`) {
					t.Errorf("logs %+v", logs[0])
				}
				return err
			}},
		{"Jobs.Timings", 200, `{"data":{"jobId":"job_1","apiTotalMs":12,"execTotalMs":null}}`,
			sent{"GET", "/jobs/job_1/timings", "", ""}, func(c *Client) error {
				tm, err := c.Jobs.Timings(ctx, "job_1")
				if err == nil && (tm.JobID != "job_1" || tm.APITotalMs == nil || *tm.APITotalMs != 12 || tm.ExecTotalMs != nil) {
					t.Errorf("timings %+v", tm)
				}
				return err
			}},
		{"Jobs.Stats", 200, `{"data":{"window":{"from":1,"to":2},"totals":{"jobs":3,"succeeded":2,"failed":1,"cancelled":0,"running":0},"successRate":0.66,"avgDurationMs":null,"spend":{"amount":5000000000,"formatted":"$5.00"},"activity":[{"bucketStart":1763700000000,"succeeded":2,"failed":1,"other":0}]}}`,
			sent{"GET", "/jobs/stats", "window=7d", ""}, func(c *Client) error {
				st, err := c.Jobs.Stats(ctx, StatsParams{Window: StatsWindowWeek})
				if err == nil && (st.Totals.Jobs != 3 || *st.SuccessRate != 0.66 || st.AvgDurationMs != nil || st.Spend.Amount != 5000000000 || st.Activity[0].BucketStart.Time().IsZero()) {
					t.Errorf("stats %+v", st)
				}
				return err
			}},
		{"Jobs.RetryDeliveries", 200, `{"data":{"retried":2}}`,
			sent{"POST", "/jobs/job_1/deliveries/retry", "", ""}, func(c *Client) error {
				r, err := c.Jobs.RetryDeliveries(ctx, "job_1")
				if err == nil && r.Retried != 2 {
					t.Errorf("retried %d", r.Retried)
				}
				return err
			}},
		{"Jobs.CreateShare", 201, `{"data":{"shareId":"shr_1","shareUrl":"https://rendobar.com/s/shr_1"}}`,
			sent{"POST", "/jobs/job_1/share", "", ""}, func(c *Client) error {
				s, err := c.Jobs.CreateShare(ctx, "job_1")
				if err == nil && s.ShareURL != "https://rendobar.com/s/shr_1" {
					t.Errorf("share %+v", s)
				}
				return err
			}},
		{"Jobs.GetShare", 200, `{"data":{"shareId":"shr_1","shareUrl":"u","createdAt":1763700000000}}`,
			sent{"GET", "/jobs/job_1/share", "", ""}, func(c *Client) error {
				s, err := c.Jobs.GetShare(ctx, "job_1")
				if err == nil && (s == nil || s.CreatedAt.Time().IsZero()) {
					t.Errorf("share %+v", s)
				}
				return err
			}},
		{"Jobs.GetShare none", 200, `{"data":null}`,
			sent{"GET", "/jobs/job_1/share", "", ""}, func(c *Client) error {
				s, err := c.Jobs.GetShare(ctx, "job_1")
				if err == nil && s != nil {
					t.Errorf("share %+v, want nil", s)
				}
				return err
			}},
		{"Jobs.DeleteShare", 200, `{"data":{"deleted":true}}`,
			sent{"DELETE", "/jobs/job_1/share", "", ""}, func(c *Client) error { return c.Jobs.DeleteShare(ctx, "job_1") }},

		// Billing
		{"Billing.State", 200, `{"data":{"balance":{"amount":12.5},"plan":{"slug":"pro","name":"Pro","price":19,"limits":{"concurrentJobs":5,"apiRequestsPerMinute":600,"maxJobTimeout":3600,"maxInputFileSize":5368709120,"teamMembers":5}},"subscription":null,"usage":{"currentPeriodSpend":1,"jobCount":2,"slotWaitMs":0,"slotWaitJobs":0,"slotWaitSavedMs":0,"slotWaitSavedJobs":0},"isPro":true,"creditBonusRate":0.1,"creditsAdded":{"purchased":1,"bonus":0,"included":0,"granted":0},"creditSchedule":null,"upgradePlan":{"id":"studio","name":"Studio","price":29,"limits":{"concurrentJobs":10,"apiRequestsPerMinute":600,"maxJobTimeout":7200,"maxInputFileSize":1,"teamMembers":10}}}}`,
			sent{"GET", "/billing/state", "", ""}, func(c *Client) error {
				s, err := c.Billing.State(ctx)
				if err == nil && (s.Balance.Amount != 12.5 || s.Plan.Slug != PlanPro || s.Plan.Limits.MaxInputFileSize != 5368709120 || s.Subscription != nil || s.UpgradePlan.Limits.ConcurrentJobs != 10) {
					t.Errorf("state %+v", s)
				}
				return err
			}},
		{"Billing.Usage", 200, `{"data":{"totalJobs":1,"totalAmountCharged":7000000000,"totalAmountFormatted":"$7.00","totalComputeSeconds":9,"byJobType":{"ffmpeg":{"jobCount":1,"amountCharged":7000000000,"amountFormatted":"$7.00","computeSeconds":9}},"daily":[{"date":"2026-11-01","jobType":"ffmpeg","jobCount":1,"amountCharged":7,"computeSeconds":9}]}}`,
			sent{"GET", "/billing/usage", "end=2026-11-30&start=2026-11-01", ""}, func(c *Client) error {
				u, err := c.Billing.Usage(ctx, UsageParams{Start: time.Date(2026, 11, 1, 23, 0, 0, 0, time.UTC), End: time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)})
				if err == nil && (u.TotalAmountCharged != 7000000000 || u.ByJobType["ffmpeg"].ComputeSeconds != 9 || u.Daily[0].JobType != JobTypeFfmpeg) {
					t.Errorf("usage %+v", u)
				}
				return err
			}},
		{"Billing.UsageByClient", 200, `{"data":[{"client":"sdk","jobCount":4,"amountCharged":1,"amountFormatted":"$0.00"}]}`,
			sent{"GET", "/billing/usage-by-client", "days=7", ""}, func(c *Client) error {
				u, err := c.Billing.UsageByClient(ctx, UsageByClientParams{Days: 7})
				if err == nil && (len(u) != 1 || u[0].Client != "sdk" || u[0].JobCount != 4) {
					t.Errorf("usage %+v", u)
				}
				return err
			}},
		{"Billing.Transactions", 200, `{"data":[{"id":"tx_1","type":"debit","amount":-0.5,"paidAmount":0,"bonusAmount":0,"jobId":"job_1","jobType":"ffmpeg","balanceAfter":9.5,"createdAt":1763700000000}],"meta":{"total":31,"page":2,"limit":10}}`,
			sent{"GET", "/billing/transactions", "limit=10&page=2", ""}, func(c *Client) error {
				p, err := c.Billing.Transactions(ctx, PageParams{Page: 2, Limit: 10})
				if err == nil && (len(p.Transactions) != 1 || p.Transactions[0].Amount != -0.5 || p.Meta.Total != 31 || p.Meta.Page != 2) {
					t.Errorf("page %+v", p)
				}
				return err
			}},
		{"Billing.History", 200, `{"data":[{"id":"inv_1","amount":19,"currency":"usd","status":"paid","productName":"Pro","createdAt":"2026-11-01T00:00:00Z"}],"meta":{"total":1,"page":1,"limit":20}}`,
			sent{"GET", "/billing/history", "", ""}, func(c *Client) error {
				p, err := c.Billing.History(ctx, PageParams{})
				if err == nil && (len(p.Invoices) != 1 || p.Invoices[0].ProductName != "Pro" || p.Meta.Total != 1) {
					t.Errorf("page %+v", p)
				}
				return err
			}},
		{"Billing.CheckoutCredits", 200, `{"data":{"checkoutUrl":"https://pay/x","checkoutId":"co_1"}}`,
			sent{"POST", "/billing/checkout/credits", "", `{"amount":25,"interval":"month"}`}, func(c *Client) error {
				co, err := c.Billing.CheckoutCredits(ctx, CheckoutCreditsParams{Amount: Ptr(25.0), Interval: CheckoutCreditsIntervalMonth})
				if err == nil && co.CheckoutID != "co_1" {
					t.Errorf("checkout %+v", co)
				}
				return err
			}},
		{"Billing.CheckoutPlan checkout", 200, `{"data":{"kind":"checkout","checkoutUrl":"https://pay/y","checkoutId":"co_2"}}`,
			sent{"POST", "/billing/checkout/plan", "", `{"plan":"studio"}`}, func(c *Client) error {
				pc, err := c.Billing.CheckoutPlan(ctx, CheckoutPlanParams{Plan: PlanStudio})
				if err == nil && (pc.Kind != PlanCheckoutKindCheckout || pc.Checkout == nil || pc.Checkout.CheckoutURL != "https://pay/y" || pc.Changed != nil) {
					t.Errorf("plan checkout %+v", pc)
				}
				return err
			}},
		{"Billing.CheckoutPlan changed", 200, `{"data":{"kind":"changed","plan":"studio"}}`,
			sent{"POST", "/billing/checkout/plan", "", `{"plan":"studio"}`}, func(c *Client) error {
				pc, err := c.Billing.CheckoutPlan(ctx, CheckoutPlanParams{Plan: PlanStudio})
				if err == nil && (pc.Changed == nil || pc.Changed.Plan != PlanStudio || pc.Checkout != nil) {
					t.Errorf("plan checkout %+v", pc)
				}
				return err
			}},
		{"Billing.CheckoutPlan newer kind", 200, `{"data":{"kind":"scheduled","at":1}}`,
			sent{"POST", "/billing/checkout/plan", "", `{"plan":"pro"}`}, func(c *Client) error {
				pc, err := c.Billing.CheckoutPlan(ctx, CheckoutPlanParams{Plan: PlanPro})
				if err == nil && (pc.Kind != "scheduled" || pc.Checkout != nil || pc.Changed != nil) {
					t.Errorf("plan checkout %+v", pc)
				}
				return err
			}},
		{"Billing.CancelSubscription", 200, `{"data":{"status":"active","cancelAtPeriodEnd":true}}`,
			sent{"PATCH", "/billing/subscription", "", `{"cancelAtPeriodEnd":true}`}, func(c *Client) error { return c.Billing.CancelSubscription(ctx) }},
		{"Billing.ReactivateSubscription", 200, `{"data":{"status":"active","cancelAtPeriodEnd":false}}`,
			sent{"PATCH", "/billing/subscription", "", `{"cancelAtPeriodEnd":false}`}, func(c *Client) error { return c.Billing.ReactivateSubscription(ctx) }},
		{"Billing.CancelCreditSchedule", 204, ``,
			sent{"DELETE", "/billing/credit-schedule", "", ""}, func(c *Client) error { return c.Billing.CancelCreditSchedule(ctx) }},
		{"Billing.PaymentMethods", 200, `{"data":[{"id":"pm_1","type":"card","brand":"visa","last4":"4242","expMonth":12,"expYear":2030,"wallet":null,"isDefault":true}]}`,
			sent{"GET", "/billing/payment-methods", "", ""}, func(c *Client) error {
				pms, err := c.Billing.PaymentMethods(ctx)
				if err == nil && (len(pms) != 1 || pms[0].Last4 != "4242" || *pms[0].ExpYear != 2030 || !pms[0].IsDefault) {
					t.Errorf("methods %+v", pms)
				}
				return err
			}},
		{"Billing.DeletePaymentMethod", 200, `{"data":{"success":true}}`,
			sent{"DELETE", "/billing/payment-methods/pm_1", "", ""}, func(c *Client) error { return c.Billing.DeletePaymentMethod(ctx, "pm_1") }},
		{"Billing.SetDefaultPaymentMethod", 200, `{"data":{"id":"pm_1"}}`,
			sent{"PATCH", "/billing/payment-methods/pm_1/default", "", ""}, func(c *Client) error { return c.Billing.SetDefaultPaymentMethod(ctx, "pm_1") }},
		{"Billing.PortalURL", 200, `{"data":{"url":"https://portal/x"}}`,
			sent{"GET", "/billing/payment-methods/portal-url", "", ""}, func(c *Client) error {
				u, err := c.Billing.PortalURL(ctx)
				if err == nil && u != "https://portal/x" {
					t.Errorf("url %q", u)
				}
				return err
			}},

		// Webhooks
		{"Webhooks.Create", 201, `{"data":{"id":"whe_1","name":"n","url":"https://x.test/h","signingSecret":"whsec_full","active":true,"disabledReason":null,"consecutiveFailures":0,"subscribedEvents":["job.completed"],"rotationActive":false,"createdAt":1,"updatedAt":2}}`,
			sent{"POST", "/webhooks/endpoints", "", `{"name":"n","url":"https://x.test/h","subscribedEvents":["job.completed","job.failed"]}`}, func(c *Client) error {
				e, err := c.Webhooks.Create(ctx, CreateWebhookParams{Name: "n", URL: "https://x.test/h", SubscribedEvents: []WebhookEvent{"job.completed", "job.failed"}})
				if err == nil && (e.ID != "whe_1" || e.SigningSecret != "whsec_full" || e.SubscribedEvents[0] != "job.completed") {
					t.Errorf("endpoint %+v", e)
				}
				return err
			}},
		{"Webhooks.List", 200, `{"data":[{"id":"whe_1"},{"id":"whe_2"}],"meta":{"total":2}}`,
			sent{"GET", "/webhooks/endpoints", "", ""}, func(c *Client) error {
				l, err := c.Webhooks.List(ctx)
				if err == nil && (len(l.Endpoints) != 2 || l.Total != 2) {
					t.Errorf("list %+v", l)
				}
				return err
			}},
		{"Webhooks.Get", 200, `{"data":{"id":"whe_1","consecutiveFailures":3}}`,
			sent{"GET", "/webhooks/endpoints/whe_1", "", ""}, func(c *Client) error {
				e, err := c.Webhooks.Get(ctx, "whe_1")
				if err == nil && e.ConsecutiveFailures != 3 {
					t.Errorf("endpoint %+v", e)
				}
				return err
			}},
		{"Webhooks.Update", 200, `{"data":{"id":"whe_1","active":false}}`,
			sent{"PATCH", "/webhooks/endpoints/whe_1", "", `{"name":"m","active":false}`}, func(c *Client) error {
				e, err := c.Webhooks.Update(ctx, "whe_1", UpdateWebhookParams{Name: "m", Active: Ptr(false)})
				if err == nil && e.Active {
					t.Errorf("endpoint %+v", e)
				}
				return err
			}},
		{"Webhooks.Delete", 204, ``,
			sent{"DELETE", "/webhooks/endpoints/whe_1", "", ""}, func(c *Client) error { return c.Webhooks.Delete(ctx, "whe_1") }},
		{"Webhooks.RotateSecret", 200, `{"data":{"id":"whe_1","signingSecret":"whsec_new","rotationActive":true,"secretExpiresAt":1763700000000}}`,
			sent{"POST", "/webhooks/endpoints/whe_1/secret", "", ""}, func(c *Client) error {
				e, err := c.Webhooks.RotateSecret(ctx, "whe_1")
				if err == nil && (e.SigningSecret != "whsec_new" || !e.RotationActive || e.SecretExpiresAt.Time().IsZero()) {
					t.Errorf("endpoint %+v", e)
				}
				return err
			}},
		{"Webhooks.Test", 200, `{"data":{"delivered":true,"statusCode":200,"responseTimeMs":41}}`,
			sent{"POST", "/webhooks/endpoints/whe_1/test", "", ""}, func(c *Client) error {
				r, err := c.Webhooks.Test(ctx, "whe_1")
				if err == nil && (!r.Delivered || r.StatusCode == nil || *r.StatusCode != 200 || r.ResponseTimeMs != 41) {
					t.Errorf("test %+v", r)
				}
				return err
			}},
		{"Webhooks.ListDeliveries", 200, `{"data":[{"id":"whd_1","endpointId":"whe_1","endpointName":null,"jobId":null,"event":"test","payload":"{}","status":"queued_later","statusCode":null,"responseBody":null,"attempts":1,"lastAttemptAt":null,"createdAt":1}],"meta":{"total":1,"limit":5,"offset":0}}`,
			sent{"GET", "/webhooks/deliveries", "endpointId=whe_1&event=job.completed&jobId=job_1&limit=5&offset=10&status=failed", ""}, func(c *Client) error {
				p, err := c.Webhooks.ListDeliveries(ctx, ListDeliveriesParams{EndpointID: "whe_1", Event: "job.completed", Status: WebhookDeliveryStatusFailed, JobID: "job_1", Limit: 5, Offset: 10})
				// A status newer than this SDK reads as its own value.
				if err == nil && (len(p.Deliveries) != 1 || p.Deliveries[0].Status != "queued_later" || p.Deliveries[0].StatusCode != nil || p.Meta.Limit != 5) {
					t.Errorf("page %+v", p)
				}
				return err
			}},
		{"Webhooks.RetryDelivery", 200, `{"data":{"id":"whd_1","status":"pending","event":"job.completed","endpointId":"whe_1"}}`,
			sent{"POST", "/webhooks/deliveries/whd_1/retry", "", ""}, func(c *Client) error {
				r, err := c.Webhooks.RetryDelivery(ctx, "whd_1")
				if err == nil && (r.Status != WebhookDeliveryStatusPending || r.EndpointID != "whe_1") {
					t.Errorf("retry %+v", r)
				}
				return err
			}},

		// Storage
		{"Storage.Create", 201, `{"data":{"id":"prod-media","provider":"s3","bucket":"b","endpoint":"https://s3","region":"us-east-1","pathStyle":false,"createdAt":1,"updatedAt":1,"checks":[{"name":"write","status":"passed"}]}}`,
			sent{"POST", "/storage", "", `{"provider":"s3","id":"prod-media","bucket":"b","region":"us-east-1","credentials":{"type":"aws-role","roleArn":"arn:aws:iam::123456789012:role/rendobar"}}`}, func(c *Client) error {
				s, err := c.Storage.Create(ctx, CreateStorageS3Params{ID: "prod-media", Bucket: "b", Region: "us-east-1", Credentials: StorageAWSRole{RoleARN: "arn:aws:iam::123456789012:role/rendobar"}})
				if err == nil && (s.Provider != StorageProviderS3 || s.Checks[0].Status != StorageCheckStatusPassed) {
					t.Errorf("storage %+v", s)
				}
				return err
			}},
		{"Storage.List", 200, `{"data":[{"id":"a"}],"meta":{"total":1}}`,
			sent{"GET", "/storage", "", ""}, func(c *Client) error {
				l, err := c.Storage.List(ctx)
				if err == nil && (len(l.Connections) != 1 || l.Total != 1) {
					t.Errorf("list %+v", l)
				}
				return err
			}},
		{"Storage.Get", 200, `{"data":{"id":"prod-media","deliverySummary":{"jobsLast30d":2,"bytesLast30d":9000000000,"lastAt":null,"lastStatus":"failed","lastReason":"bucket_not_found"}}}`,
			sent{"GET", "/storage/prod-media", "", ""}, func(c *Client) error {
				s, err := c.Storage.Get(ctx, "prod-media")
				if err == nil && (s.DeliverySummary.BytesLast30d != 9000000000 || *s.DeliverySummary.LastReason != StorageDeliveryReasonBucketNotFound) {
					t.Errorf("storage %+v", s)
				}
				return err
			}},
		{"Storage.Update", 200, `{"data":{"id":"prod-media","onConflict":"replace"}}`,
			sent{"PATCH", "/storage/prod-media", "", `{"credentials":{"type":"keys","accessKeyId":"a","secretAccessKey":"s"},"onConflict":"replace"}`}, func(c *Client) error {
				s, err := c.Storage.Update(ctx, "prod-media", UpdateStorageParams{Credentials: StorageKeys{AccessKeyID: "a", SecretAccessKey: "s"}, OnConflict: StorageOnConflictReplace})
				if err == nil && s.OnConflict != StorageOnConflictReplace {
					t.Errorf("storage %+v", s)
				}
				return err
			}},
		{"Storage.Delete", 204, ``,
			sent{"DELETE", "/storage/prod-media", "", ""}, func(c *Client) error { return c.Storage.Delete(ctx, "prod-media") }},
		{"Storage.Test", 200, `{"data":{"ok":false,"checks":[{"name":"write","status":"failed","reason":"destination_denied","message":"no"}]}}`,
			sent{"POST", "/storage/prod-media/test", "", ""}, func(c *Client) error {
				r, err := c.Storage.Test(ctx, "prod-media")
				if err == nil && (r.OK || r.Checks[0].Reason != StorageDeliveryReasonDestinationDenied) {
					t.Errorf("test %+v", r)
				}
				return err
			}},
		{"Storage.Stats", 200, `{"data":{"windowDays":30,"deliveries":{"delivered":1,"failed":0,"pending":0,"daily":[]},"throughput":null,"written":{"bytes":5000000000,"files":2},"roundTripMs":null,"measuredFrom":null}}`,
			sent{"GET", "/storage/prod-media/stats", "", ""}, func(c *Client) error {
				s, err := c.Storage.Stats(ctx, "prod-media")
				if err == nil && (s.WindowDays != 30 || s.Throughput != nil || s.Written.Bytes != 5000000000) {
					t.Errorf("stats %+v", s)
				}
				return err
			}},
		{"Storage.ListObjects", 200, `{"data":{"folders":["a/"],"objects":[{"key":"x.mp4","size":4294967296,"lastModified":1763700000000}],"cursor":"next"}}`,
			sent{"GET", "/storage/prod-media/objects", "cursor=c1&limit=10&prefix=out%2F", ""}, func(c *Client) error {
				o, err := c.Storage.ListObjects(ctx, "prod-media", ListObjectsParams{Prefix: "out/", Cursor: "c1", Limit: 10})
				if err == nil && (o.Objects[0].Size != 4294967296 || o.Cursor != "next" || o.Objects[0].LastModified.Time().IsZero()) {
					t.Errorf("objects %+v", o)
				}
				return err
			}},
		{"Storage.PrepareAWS", 201, `{"data":{"id":"aws-1","provider":"s3","pending":true,"quickCreateUrl":"https://console.aws/x","trustPolicy":"{}","terraform":"t","subject":"s","issuer":"i","audience":"a"}}`,
			sent{"POST", "/storage/aws/prepare", "", `{"id":"aws-1","bucket":"b","access":"read"}`}, func(c *Client) error {
				p, err := c.Storage.PrepareAWS(ctx, PrepareAWSStorageParams{ID: "aws-1", Bucket: "b", Access: StorageAccessRead})
				if err == nil && (!p.Pending || p.QuickCreateURL != "https://console.aws/x") {
					t.Errorf("prepared %+v", p)
				}
				return err
			}},
		{"Storage.ListAccountBuckets", 200, `{"data":{"buckets":["a","b"]}}`,
			sent{"GET", "/storage/buckets", "provider=r2", ""}, func(c *Client) error {
				b, err := c.Storage.ListAccountBuckets(ctx, AccountBucketsParams{Provider: StorageProviderR2})
				if err == nil && len(b) != 2 {
					t.Errorf("buckets %v", b)
				}
				return err
			}},
		{"Storage.LookUpS3Bucket", 200, `{"data":{"state":"found","region":"eu-west-1"}}`,
			sent{"GET", "/storage/s3/bucket", "name=my+bucket", ""}, func(c *Client) error {
				l, err := c.Storage.LookUpS3Bucket(ctx, "my bucket")
				if err == nil && (l.State != S3BucketLookupStateFound || l.Region != "eu-west-1") {
					t.Errorf("lookup %+v", l)
				}
				return err
			}},
		{"Storage.StartSupabase", 200, `{"data":{"url":"https://supabase/auth","state":"st_1"}}`,
			sent{"POST", "/storage/supabase/start", "", ""}, func(c *Client) error {
				s, err := c.Storage.StartSupabase(ctx)
				if err == nil && s.State != "st_1" {
					t.Errorf("start %+v", s)
				}
				return err
			}},
		{"Storage.ListSupabaseProjects", 200, `{"data":{"state":"st_1","projects":[{"ref":"abcdefghijklmnopqrst","name":"p","region":"us-east-1"}]}}`,
			sent{"GET", "/storage/supabase/sessions/st_1", "", ""}, func(c *Client) error {
				s, err := c.Storage.ListSupabaseProjects(ctx, "st_1")
				if err == nil && s.Projects[0].Ref != "abcdefghijklmnopqrst" {
					t.Errorf("projects %+v", s)
				}
				return err
			}},
		{"Storage.ListSupabaseBuckets", 200, `{"data":{"state":"st_1","projectRef":"abcdefghijklmnopqrst","buckets":[{"name":"media","public":true}]}}`,
			sent{"GET", "/storage/supabase/sessions/st_1/projects/abcdefghijklmnopqrst/buckets", "", ""}, func(c *Client) error {
				s, err := c.Storage.ListSupabaseBuckets(ctx, "st_1", "abcdefghijklmnopqrst")
				if err == nil && !s.Buckets[0].Public {
					t.Errorf("buckets %+v", s)
				}
				return err
			}},
		{"Storage.FinishSupabase", 201, `{"data":{"id":"sb-1","provider":"supabase"}}`,
			sent{"POST", "/storage/supabase/finish", "", `{"state":"st_1","projectRef":"abcdefghijklmnopqrst","id":"sb-1","bucket":"media"}`}, func(c *Client) error {
				s, err := c.Storage.FinishSupabase(ctx, FinishSupabaseParams{State: "st_1", ProjectRef: "abcdefghijklmnopqrst", ID: "sb-1", Bucket: "media"})
				if err == nil && s.Provider != StorageProviderSupabase {
					t.Errorf("storage %+v", s)
				}
				return err
			}},

		// API keys
		{"APIKeys.Create", 201, `{"data":{"id":"key_1","name":"ci","prefix":"rb_ab","enabled":true,"createdAt":1,"lastUsedAt":null,"expiresAt":1763700000000,"scopes":["jobs:read"],"createdBy":{"name":"A","inWorkspace":true,"email":null,"role":"owner"},"manageable":true,"disabledBy":null,"disabledAt":null,"key":"rb_ab_secret"}}`,
			sent{"POST", "/api-keys", "", `{"name":"ci","expiresIn":2592000,"scopes":["jobs:read"]}`}, func(c *Client) error {
				k, err := c.APIKeys.Create(ctx, CreateAPIKeyParams{Name: "ci", ExpiresIn: Ptr[int64](2592000), Scopes: []string{"jobs:read"}})
				if err == nil && (k.Key != "rb_ab_secret" || *k.CreatedBy.Role != MemberRoleOwner || k.LastUsedAt != 0) {
					t.Errorf("key %+v", k)
				}
				return err
			}},
		{"APIKeys.List", 200, `{"data":[{"id":"key_1","name":"ci","createdBy":null}]}`,
			sent{"GET", "/api-keys", "", ""}, func(c *Client) error {
				ks, err := c.APIKeys.List(ctx)
				if err == nil && (len(ks) != 1 || ks[0].CreatedBy != nil || ks[0].Key != "") {
					t.Errorf("keys %+v", ks)
				}
				return err
			}},
		{"APIKeys.Update", 200, `{"data":{"id":"key_1","enabled":false,"disabledBy":{"name":"A"}}}`,
			sent{"PATCH", "/api-keys/key_1", "", `{"enabled":false}`}, func(c *Client) error {
				k, err := c.APIKeys.Update(ctx, "key_1", UpdateAPIKeyParams{Enabled: Ptr(false)})
				if err == nil && (k.Enabled || k.DisabledBy.Name != "A") {
					t.Errorf("key %+v", k)
				}
				return err
			}},
		{"APIKeys.Revoke", 200, `{"data":{"id":"key_1","revoked":true}}`,
			sent{"DELETE", "/api-keys/key_1", "", ""}, func(c *Client) error { return c.APIKeys.Revoke(ctx, "key_1") }},

		// Orgs
		{"Orgs.List", 200, `{"data":[{"id":"org_1","name":"Acme","slug":"acme","role":"admin"}]}`,
			sent{"GET", "/orgs", "", ""}, func(c *Client) error {
				os, err := c.Orgs.List(ctx)
				if err == nil && (len(os) != 1 || os[0].Role != MemberRoleAdmin) {
					t.Errorf("orgs %+v", os)
				}
				return err
			}},
		{"Orgs.Current", 200, `{"data":{"org":{"id":"org_1","name":"Acme","slug":"acme","createdAt":1,"memberRole":"owner","isDemo":false},"subscription":{"planId":"pro","status":"active","adapterId":"polar","currentPeriodStart":1763700000000,"currentPeriodEnd":null},"plan":null,"balance":{"balance":12000000000,"balanceFormatted":"$12.00"}}}`,
			sent{"GET", "/orgs/current", "", ""}, func(c *Client) error {
				o, err := c.Orgs.Current(ctx)
				if err == nil && (o.Org.MemberRole != MemberRoleOwner || o.Plan != nil || o.Balance.Balance != 12000000000 || o.Subscription.CurrentPeriodStart.Time().IsZero() || o.Subscription.CurrentPeriodEnd != 0) {
					t.Errorf("current %+v", o)
				}
				return err
			}},
		{"Orgs.Create", 201, `{"data":{"id":"org_2","name":"New","slug":"new-co"}}`,
			sent{"POST", "/orgs", "", `{"name":"New","slug":"new-co"}`}, func(c *Client) error {
				o, err := c.Orgs.Create(ctx, CreateOrgParams{Name: "New", Slug: "new-co"})
				if err == nil && o.ID != "org_2" {
					t.Errorf("org %+v", o)
				}
				return err
			}},
		{"Orgs.Update", 200, `{"data":{"id":"org_1","name":"Acme 2","slug":"acme","createdAt":1}}`,
			sent{"PATCH", "/orgs/current", "", `{"name":"Acme 2"}`}, func(c *Client) error {
				o, err := c.Orgs.Update(ctx, UpdateOrgParams{Name: "Acme 2"})
				if err == nil && o.Name != "Acme 2" {
					t.Errorf("org %+v", o)
				}
				return err
			}},
		{"Orgs.Delete", 204, ``,
			sent{"DELETE", "/orgs/current", "", ""}, func(c *Client) error { return c.Orgs.Delete(ctx) }},
		{"Orgs.GetSettings", 200, `{"data":{"billingEmailsEnabled":true,"defaultRegion":"eu-central-1","regionPinned":false,"productAnalyticsEnabled":true}}`,
			sent{"GET", "/orgs/current/settings", "", ""}, func(c *Client) error {
				s, err := c.Orgs.GetSettings(ctx)
				if err == nil && (s.DefaultRegion != RegionEuCentral1 || !s.BillingEmailsEnabled) {
					t.Errorf("settings %+v", s)
				}
				return err
			}},
		{"Orgs.UpdateSettings", 200, `{"data":{"billingEmailsEnabled":false,"defaultRegion":"auto","regionPinned":false,"productAnalyticsEnabled":true}}`,
			sent{"PATCH", "/orgs/current/settings", "", `{"billingEmailsEnabled":false,"defaultRegion":"auto"}`}, func(c *Client) error {
				s, err := c.Orgs.UpdateSettings(ctx, UpdateOrgSettingsParams{BillingEmailsEnabled: Ptr(false), DefaultRegion: RegionAuto})
				if err == nil && s.BillingEmailsEnabled {
					t.Errorf("settings %+v", s)
				}
				return err
			}},
		{"Orgs.Members.Invite", 200, `{"data":{"id":"inv_1","email":"a@b.test","role":"member","expiresAt":1763700000000}}`,
			sent{"POST", "/orgs/current/members", "", `{"email":"a@b.test","role":"member"}`}, func(c *Client) error {
				i, err := c.Orgs.Members.Invite(ctx, InviteMemberParams{Email: "a@b.test", Role: MemberRoleMember})
				if err == nil && (i.ID != "inv_1" || i.Role != MemberRoleMember || i.ExpiresAt.Time().IsZero()) {
					t.Errorf("invitation %+v", i)
				}
				return err
			}},
		{"Orgs.Members.ChangeRole", 200, `{"data":{"memberId":"mem_1","role":"admin"}}`,
			sent{"PATCH", "/orgs/current/members/mem_1", "", `{"role":"admin"}`}, func(c *Client) error {
				return c.Orgs.Members.ChangeRole(ctx, "mem_1", ChangeRoleParams{Role: MemberRoleAdmin})
			}},
		{"Orgs.Members.Remove", 200, `{"data":{"removed":true}}`,
			sent{"DELETE", "/orgs/current/members/mem_1", "", ""}, func(c *Client) error { return c.Orgs.Members.Remove(ctx, "mem_1") }},
		{"Orgs.Invitations.Revoke", 200, `{"data":{"revoked":true}}`,
			sent{"DELETE", "/orgs/current/invitations/inv_1", "", ""}, func(c *Client) error { return c.Orgs.Invitations.Revoke(ctx, "inv_1") }},

		// Account
		{"Account.Capabilities", 200, `{"data":{"plan":{"slug":"pro","name":"Pro"},"limits":{},"features":{}}}`,
			sent{"GET", "/account/capabilities", "", ""}, func(c *Client) error {
				a, err := c.Account.Capabilities(ctx)
				if err == nil && a.Plan.Slug != "pro" {
					t.Errorf("capabilities %+v", a)
				}
				return err
			}},

		// Compose
		{"Compose.Effects", 200, `{"data":{"effects":[{"type":"blur","group":"stylize","medium":"picture","summary":"Blur","params":{"radius":{"kind":"number","description":"px","min":0,"max":100,"default":4,"unit":"px","keyframes":true}},"required":[]}]}}`,
			sent{"GET", "/compose/effects", "", ""}, func(c *Client) error {
				e, err := c.Compose.Effects(ctx)
				if err == nil && (e.Effects[0].Params["radius"].Kind != ComposeParamKindNumber || !e.Effects[0].Params["radius"].Keyframes || string(e.Effects[0].Params["radius"].Default) != "4") {
					t.Errorf("effects %+v", e)
				}
				return err
			}},
		{"Compose.EngineEffects", 200, `{"data":{"filters":[{"filter":"hue","checked":"proven","params":{"h":{"kind":"number","min":0,"max":360}}}]}}`,
			sent{"GET", "/compose/effects/engine", "", ""}, func(c *Client) error {
				e, err := c.Compose.EngineEffects(ctx)
				if err == nil && (e.Filters[0].Checked != EngineCheckProven || e.Filters[0].Params["h"].Max != 360) {
					t.Errorf("engine %+v", e)
				}
				return err
			}},
		{"Compose.Validate valid", 200, `{"data":{"valid":true,"params":{"output":{"width":1280}},"upgraded":["clips"],"warnings":[{"path":["timeline",0,"src"],"message":"slow"}],"price":{"perMinuteUsd":0.04,"minSeconds":15}}}`,
			sent{"POST", "/compose/validate", "", `{"params":{"output":{"width":1280}}}`}, func(c *Client) error {
				v, err := c.Compose.Validate(ctx, ComposeValidateParams{Params: map[string]any{"output": map[string]any{"width": 1280}}})
				if err == nil && (!v.Valid || v.Price.MinSeconds != 15 || v.Warnings[0].Path != "timeline.0.src" || v.Upgraded[0] != "clips" || v.Params["output"] == nil) {
					t.Errorf("validation %+v", v)
				}
				return err
			}},
		{"Compose.Validate invalid", 200, `{"data":{"valid":false,"problems":[{"path":["output"],"message":"4K needs Studio","upgradeTo":"studio"}],"params":null,"upgraded":[],"warnings":[]}}`,
			sent{"POST", "/compose/validate", "", `{"params":{}}`}, func(c *Client) error {
				v, err := c.Compose.Validate(ctx, ComposeValidateParams{Params: map[string]any{}})
				if err == nil && (v.Valid || len(v.Problems) != 1 || v.Problems[0].UpgradeTo != PlanStudio || v.Price != nil || v.Params != nil) {
					t.Errorf("validation %+v", v)
				}
				return err
			}},

		// Assets
		{"Assets.List", 200, `{"data":[{"id":"asset_1","status":"ready","kind":"media","lifecycle":"persisted","sizeBytes":6000000000,"createdAt":1}],"meta":{"total":1,"limit":10,"offset":0}}`,
			sent{"GET", "/assets", "kind=media&lifecycle=persisted&limit=10&status=ready", ""}, func(c *Client) error {
				p, err := c.Assets.List(ctx, ListAssetsParams{Kind: AssetKindMedia, Status: AssetStatusReady, Lifecycle: AssetLifecyclePersisted, Limit: 10})
				if err == nil && (len(p.Assets) != 1 || *p.Assets[0].SizeBytes != 6000000000 || p.Assets[0].Status != AssetStatusReady || p.Meta.Total != 1) {
					t.Errorf("page %+v", p)
				}
				return err
			}},
		{"Assets.Templates", 200, `{"data":[],"meta":{"total":0,"limit":5,"offset":5}}`,
			sent{"GET", "/assets/templates", "limit=5&offset=5", ""}, func(c *Client) error {
				p, err := c.Assets.Templates(ctx, TemplatesParams{Limit: 5, Offset: 5})
				if err == nil && (p.Assets == nil || p.Meta.Offset != 5) {
					t.Errorf("page %+v", p)
				}
				return err
			}},
		{"Assets.Get", 200, `{"data":{"id":"asset_1","status":"archived"}}`,
			sent{"GET", "/assets/asset_1", "", ""}, func(c *Client) error {
				a, err := c.Assets.Get(ctx, "asset_1")
				if err == nil && a.Status != "archived" { // a status newer than this SDK
					t.Errorf("asset %+v", a)
				}
				return err
			}},
		{"Assets.Delete", 200, `{"data":{"deleted":true}}`,
			sent{"DELETE", "/assets/asset_1", "", ""}, func(c *Client) error { return c.Assets.Delete(ctx, "asset_1") }},
		{"Assets.Download", 200, `{"data":{"url":"https://r2/x","expiresAt":1763700000000}}`,
			sent{"GET", "/assets/asset_1/download", "", ""}, func(c *Client) error {
				d, err := c.Assets.Download(ctx, "asset_1")
				if err == nil && (d.URL != "https://r2/x" || d.ExpiresAt.Time().IsZero()) {
					t.Errorf("download %+v", d)
				}
				return err
			}},

		// Notifications
		{"Notifications.List", 200, `{"data":[{"id":"ntf_1","severity":"warning","title":"t","body":"b","href":null,"actionLabel":null,"requiresAck":false,"read":false,"createdAt":1}],"meta":{"unread":3}}`,
			sent{"GET", "/notifications", "", ""}, func(c *Client) error {
				l, err := c.Notifications.List(ctx)
				if err == nil && (len(l.Notifications) != 1 || l.Notifications[0].Severity != NotificationSeverityWarning || l.Unread != 3) {
					t.Errorf("list %+v", l)
				}
				return err
			}},
		{"Notifications.MarkRead", 200, `{"data":{"read":2}}`,
			sent{"POST", "/notifications/read", "", `{"ids":["ntf_1","ntf_2"]}`}, func(c *Client) error {
				r, err := c.Notifications.MarkRead(ctx, MarkReadParams{IDs: []string{"ntf_1", "ntf_2"}})
				if err == nil && r.Read != 2 {
					t.Errorf("read %+v", r)
				}
				return err
			}},
	}
}

func TestEveryResourceMethodSendsAndReads(t *testing.T) {
	for _, tc := range resourceCalls(t) {
		t.Run(tc.name, func(t *testing.T) {
			c, got, hits := recordingServer(t, tc.status, tc.resp)
			if err := tc.run(c); err != nil {
				t.Fatalf("error %v", err)
			}
			if hits.Load() != 1 {
				t.Fatalf("%d requests, want 1", hits.Load())
			}
			if got.Method != tc.want.Method || got.Path != tc.want.Path {
				t.Errorf("sent %s %s, want %s %s", got.Method, got.Path, tc.want.Method, tc.want.Path)
			}
			if sortedQuery(got.Query) != sortedQuery(tc.want.Query) {
				t.Errorf("query %q, want %q", got.Query, tc.want.Query)
			}
			if !sameJSON(t, got.Body, tc.want.Body) {
				t.Errorf("body %s, want %s", got.Body, tc.want.Body)
			}
		})
	}
}

// Every refusal the SDK can make for these methods happens before a request.
func TestResourceMethodsRefuseBadInputBeforeSending(t *testing.T) {
	ctx := context.Background()
	c, _, hits := recordingServer(t, 200, `{"data":{}}`)
	for name, call := range map[string]func() error{
		"Jobs.Logs bad id":             func() error { _, err := c.Jobs.Logs(ctx, "../x"); return err },
		"Jobs.Timings empty id":        func() error { _, err := c.Jobs.Timings(ctx, ""); return err },
		"Jobs.DeleteShare dot dot":     func() error { return c.Jobs.DeleteShare(ctx, "..") },
		"Assets.Get slash":             func() error { _, err := c.Assets.Get(ctx, "../api-keys/key_x"); return err },
		"Assets.Delete empty":          func() error { return c.Assets.Delete(ctx, "") },
		"Webhooks.Get slash":           func() error { _, err := c.Webhooks.Get(ctx, "a/b"); return err },
		"Webhooks.RetryDelivery dot":   func() error { _, err := c.Webhooks.RetryDelivery(ctx, "."); return err },
		"Storage.Get slash":            func() error { _, err := c.Storage.Get(ctx, "a/b"); return err },
		"Storage.ListSupabaseBuckets":  func() error { _, err := c.Storage.ListSupabaseBuckets(ctx, "st", "a/b"); return err },
		"APIKeys.Revoke query":         func() error { return c.APIKeys.Revoke(ctx, "key?x=1") },
		"Billing.DeletePaymentMethod":  func() error { return c.Billing.DeletePaymentMethod(ctx, "pm/1") },
		"Orgs.Members.Remove":          func() error { return c.Orgs.Members.Remove(ctx, "") },
		"Orgs.Invitations.Revoke":      func() error { return c.Orgs.Invitations.Revoke(ctx, "..") },
		"Assets.List negative":         func() error { _, err := c.Assets.List(ctx, ListAssetsParams{Offset: -1}); return err },
		"Assets.Templates negative":    func() error { _, err := c.Assets.Templates(ctx, TemplatesParams{Limit: -1}); return err },
		"Webhooks.ListDeliveries neg":  func() error { _, err := c.Webhooks.ListDeliveries(ctx, ListDeliveriesParams{Limit: -5}); return err },
		"Billing.Transactions neg":     func() error { _, err := c.Billing.Transactions(ctx, PageParams{Page: -1}); return err },
		"Billing.UsageByClient neg":    func() error { _, err := c.Billing.UsageByClient(ctx, UsageByClientParams{Days: -1}); return err },
		"Storage.ListObjects neg":      func() error { _, err := c.Storage.ListObjects(ctx, "a", ListObjectsParams{Limit: -1}); return err },
		"Storage.LookUpS3Bucket empty": func() error { _, err := c.Storage.LookUpS3Bucket(ctx, ""); return err },
		"Storage.Create nil":           func() error { _, err := c.Storage.Create(ctx, nil); return err },
		"Compose.Validate nil":         func() error { _, err := c.Compose.Validate(ctx, ComposeValidateParams{}); return err },
		"Webhooks.Create empty": func() error {
			_, err := c.Webhooks.Create(ctx, CreateWebhookParams{})
			var apiErr *Error
			if errors.As(err, &apiErr) && len(apiErr.Problems) != 3 {
				t.Errorf("problems %+v, want name, url and subscribedEvents", apiErr.Problems)
			}
			return err
		},
		"Notifications.MarkRead too many": func() error {
			_, err := c.Notifications.MarkRead(ctx, MarkReadParams{IDs: make([]string, 51)})
			return err
		},
		"Orgs.Members.Invite unknown role": func() error {
			_, err := c.Orgs.Members.Invite(ctx, InviteMemberParams{Email: "a@b.test", Role: "boss"})
			return err
		},
		"Storage.Create bad bucket": func() error {
			_, err := c.Storage.Create(ctx, CreateStorageS3Params{ID: "x", Bucket: "-bad-", Credentials: StorageKeys{AccessKeyID: "a", SecretAccessKey: "b"}})
			var apiErr *Error
			if errors.As(err, &apiErr) && (len(apiErr.Problems) != 2 || apiErr.Problems[0].Path != "id") {
				t.Errorf("problems %+v, want id and bucket", apiErr.Problems)
			}
			return err
		},
		"Storage.Create missing credentials": func() error {
			_, err := c.Storage.Create(ctx, CreateStorageR2Params{ID: "media", Bucket: "b"})
			var apiErr *Error
			if errors.As(err, &apiErr) && (len(apiErr.Problems) != 1 || apiErr.Problems[0].Path != "credentials") {
				t.Errorf("problems %+v", apiErr.Problems)
			}
			return err
		},
		"Billing.CheckoutCredits too little": func() error {
			_, err := c.Billing.CheckoutCredits(ctx, CheckoutCreditsParams{Amount: Ptr(5.0)})
			return err
		},
		"Orgs.Create bad slug": func() error {
			_, err := c.Orgs.Create(ctx, CreateOrgParams{Name: "Acme", Slug: "Not A Slug"})
			return err
		},
	} {
		if err := call(); codeOf(err) != CodeInvalidArgument {
			t.Errorf("%s: got %v, want INVALID_ARGUMENT", name, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("%d requests sent, want none", hits.Load())
	}
}

func TestResourceMethodsNeedAKey(t *testing.T) {
	t.Setenv("RENDOBAR_API_KEY", "")
	c := New(WithBaseURL("http://127.0.0.1:1"))
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"Billing.State":  func() error { _, err := c.Billing.State(ctx); return err },
		"Webhooks.List":  func() error { _, err := c.Webhooks.List(ctx); return err },
		"Assets.List":    func() error { _, err := c.Assets.List(ctx, ListAssetsParams{}); return err },
		"Storage.Delete": func() error { return c.Storage.Delete(ctx, "x") },
		"Uploads.Create": func() error {
			_, err := c.Uploads.Create(ctx, strings.NewReader("x"), 1, UploadParams{Filename: "x"})
			return err
		},
		"Jobs.Logs":          func() error { _, err := c.Jobs.Logs(ctx, "job_1"); return err },
		"Compose.Effects":    func() error { _, err := c.Compose.Effects(ctx); return err },
		"Notifications.List": func() error { _, err := c.Notifications.List(ctx); return err },
		"Orgs.Members.Invite": func() error {
			_, err := c.Orgs.Members.Invite(ctx, InviteMemberParams{Email: "a", Role: MemberRoleMember})
			return err
		},
	} {
		if err := call(); codeOf(err) != CodeMissingAPIKey {
			t.Errorf("%s: got %v, want MISSING_API_KEY", name, err)
		}
	}
}

func TestResourceErrorsAreTyped(t *testing.T) {
	c, _, _ := recordingServer(t, 404, `{"error":{"code":"NOT_FOUND","message":"Endpoint not found"}}`)
	_, err := c.Webhooks.Get(context.Background(), "whe_gone")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != CodeNotFound || apiErr.StatusCode != 404 || apiErr.Message != "Endpoint not found" {
		t.Fatalf("got %v", err)
	}
	c, _, _ = recordingServer(t, 200, `<html>proxy</html>`)
	if _, err := c.Billing.State(context.Background()); err == nil || !strings.Contains(err.Error(), "unreadable response") {
		t.Fatalf("a non-JSON body: got %v", err)
	}
	c, _, _ = recordingServer(t, 200, `{"data":null}`)
	if _, err := c.Billing.State(context.Background()); err == nil || !strings.Contains(err.Error(), "has no data") {
		t.Fatalf("a null body: got %v", err)
	}
}

// A 5xx or a lost connection may come after the API acted, so only a request
// that is safe to repeat retries one. A 429 means nothing was done, so every
// method retries it.
func TestOnlyRepeatableRequestsRetryA5xx(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		status int
		call   func(c *Client) error
		want   int32
	}{
		{"GET on 503", 503, func(c *Client) error { _, err := c.Webhooks.List(ctx); return err }, 3},
		{"POST on 503", 503, func(c *Client) error {
			_, err := c.Webhooks.Create(ctx, CreateWebhookParams{Name: "n", URL: "https://x", SubscribedEvents: []WebhookEvent{"job.completed"}})
			return err
		}, 1},
		{"DELETE on 502", 502, func(c *Client) error { return c.APIKeys.Revoke(ctx, "key_1") }, 1},
		{"PATCH on 500", 500, func(c *Client) error { return c.Billing.CancelSubscription(ctx) }, 1},
		{"cancel on 503", 503, func(c *Client) error { return c.Jobs.Cancel(ctx, "job_1") }, 1},
		{"submit on 503", 503, func(c *Client) error {
			_, err := c.Jobs.Create(ctx, CreateParams{Params: FfprobeParams{Command: "x"}})
			return err
		}, 3},
		{"POST on 429", 429, func(c *Client) error { _, err := c.Webhooks.Test(ctx, "whe_1"); return err }, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"error":{"code":"X","message":"no"}}`)
			}))
			defer srv.Close()
			if err := tc.call(New(WithAPIKey("rb_test"), WithBaseURL(srv.URL))); statusOf(err) != tc.status {
				t.Fatalf("got %v", err)
			}
			if hits.Load() != tc.want {
				t.Fatalf("%d requests, want %d", hits.Load(), tc.want)
			}
		})
	}
}

func TestAConnectionErrorRetriesOnlyARead(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		_, _ = io.WriteString(w, `{"data":{"id":"whe_1"}}`)
	}))
	defer srv.Close()
	c := New(WithAPIKey("rb_test"), WithBaseURL(srv.URL))
	if _, err := c.Webhooks.Get(context.Background(), "whe_1"); err != nil || hits.Load() != 2 {
		t.Fatalf("GET: %v after %d requests, want success after 2", err, hits.Load())
	}
	hits.Store(0)
	if _, err := c.Webhooks.RotateSecret(context.Background(), "whe_1"); err == nil || hits.Load() != 1 {
		t.Fatalf("POST: %v after %d requests, want an error after 1", err, hits.Load())
	}
}

// The generated request bodies send the one-value fields that pick a branch.
func TestRequestBodiesSendTheirBranch(t *testing.T) {
	for _, tc := range []struct {
		body CreateStorageParams
		want string
	}{
		{CreateStorageR2Params{ID: "media", Bucket: "b", AccountID: "0123456789abcdef0123456789abcdef", Credentials: StorageCloudflareToken{Token: "t"}},
			`{"provider":"r2","id":"media","bucket":"b","accountId":"0123456789abcdef0123456789abcdef","credentials":{"type":"cloudflare-token","token":"t"}}`},
		{CreateStorageSupabaseParams{ID: "media", Bucket: "b", Region: "us-east-1", Credentials: StorageSupabaseSession{AnonKey: "a", SessionToken: "s"}},
			`{"provider":"supabase","id":"media","bucket":"b","region":"us-east-1","credentials":{"type":"supabase-session","anonKey":"a","sessionToken":"s"}}`},
		{CreateStorageS3CompatibleParams{ID: "media", Bucket: "b", Endpoint: "https://e", Region: "auto", PathStyle: Ptr(true), Credentials: &StorageKeys{AccessKeyID: "a", SecretAccessKey: "s"}},
			`{"provider":"s3-compatible","id":"media","bucket":"b","endpoint":"https://e","region":"auto","pathStyle":true,"credentials":{"type":"keys","accessKeyId":"a","secretAccessKey":"s"}}`},
	} {
		raw, err := json.Marshal(tc.body)
		if err != nil {
			t.Fatal(err)
		}
		if !sameJSON(t, string(raw), tc.want) {
			t.Errorf("%T sent %s, want %s", tc.body, raw, tc.want)
		}
		if err := valid(tc.body); err != nil {
			t.Errorf("%T: %v", tc.body, err)
		}
	}
}

// The client offers every resource and method of @rendobar/sdk's contract
// (packages/sdk/src/__tests__/contract.test.ts), under Go names, except the
// deprecated team resource and billing.checkoutPro, which have replacements.
func TestSurfaceMatchesTheTypeScriptContract(t *testing.T) {
	c := New(WithAPIKey("rb_test"))
	want := map[string][]string{
		"Jobs":             {"Create", "Get", "List", "All", "Wait", "Run", "Cancel", "RetryDeliveries", "Download", "Logs", "Timings", "Types", "Schema", "Stats", "CreateShare", "GetShare", "DeleteShare"},
		"Billing":          {"State", "Usage", "UsageByClient", "Transactions", "History", "CheckoutCredits", "CheckoutPlan", "CancelSubscription", "ReactivateSubscription", "CancelCreditSchedule", "PaymentMethods", "DeletePaymentMethod", "SetDefaultPaymentMethod", "PortalURL"},
		"Uploads":          {"Create"},
		"Webhooks":         {"Create", "List", "Get", "Update", "Delete", "ListDeliveries", "RetryDelivery", "RotateSecret", "Test"},
		"Storage":          {"Create", "List", "Get", "Update", "Delete", "Test", "ListObjects", "Stats", "PrepareAWS", "StartSupabase", "ListSupabaseProjects", "ListSupabaseBuckets", "FinishSupabase", "ListAccountBuckets", "LookUpS3Bucket"},
		"APIKeys":          {"Create", "List", "Revoke", "Update"},
		"Orgs":             {"List", "Current", "Create", "Update", "Delete", "GetSettings", "UpdateSettings"},
		"Account":          {"Capabilities"},
		"Compose":          {"Effects", "EngineEffects", "Validate"},
		"Assets":           {"List", "Get", "Delete", "Download", "Templates"},
		"Notifications":    {"List", "MarkRead"},
		"Orgs.Members":     {"Invite", "ChangeRole", "Remove"},
		"Orgs.Invitations": {"Revoke"},
	}
	v := reflect.ValueOf(c).Elem()
	resources := map[string]reflect.Value{}
	for i := 0; i < v.NumField(); i++ {
		if f := v.Type().Field(i); f.IsExported() {
			resources[f.Name] = v.Field(i)
		}
	}
	orgs := resources["Orgs"].Elem()
	resources["Orgs.Members"] = orgs.FieldByName("Members")
	resources["Orgs.Invitations"] = orgs.FieldByName("Invitations")
	for name, methods := range want {
		r, ok := resources[name]
		if !ok || r.IsNil() {
			t.Errorf("no resource %s", name)
			continue
		}
		have := map[string]bool{}
		for i := 0; i < r.Type().NumMethod(); i++ {
			have[r.Type().Method(i).Name] = true
		}
		for _, m := range methods {
			if !have[m] {
				t.Errorf("%s has no method %s", name, m)
			}
		}
		if len(have) != len(methods) {
			t.Errorf("%s has %d methods, the list names %d: update the list", name, len(have), len(methods))
		}
	}
	if n := len(resources) - 2; n != len(want)-2 {
		t.Errorf("the client has %d resources, the list names %d", n, len(want)-2)
	}
}

// Against the conformance mock, which answers every operation with data its
// response schema describes: each method reads that shape without an error.
func TestResourceMethodsReadTheMocksSpecShapes(t *testing.T) {
	reset(t, "")
	c := client()
	ctx := context.Background()
	calls := map[string]func() error{
		"Jobs.Logs":                    func() error { _, err := c.Jobs.Logs(ctx, "job_done"); return err },
		"Jobs.Timings":                 func() error { _, err := c.Jobs.Timings(ctx, "job_done"); return err },
		"Jobs.Stats":                   func() error { _, err := c.Jobs.Stats(ctx, StatsParams{}); return err },
		"Jobs.RetryDeliveries":         func() error { _, err := c.Jobs.RetryDeliveries(ctx, "job_done"); return err },
		"Jobs.CreateShare":             func() error { _, err := c.Jobs.CreateShare(ctx, "job_done"); return err },
		"Jobs.GetShare":                func() error { _, err := c.Jobs.GetShare(ctx, "job_done"); return err },
		"Jobs.DeleteShare":             func() error { return c.Jobs.DeleteShare(ctx, "job_done") },
		"Billing.State":                func() error { _, err := c.Billing.State(ctx); return err },
		"Billing.Usage":                func() error { _, err := c.Billing.Usage(ctx, UsageParams{}); return err },
		"Billing.UsageByClient":        func() error { _, err := c.Billing.UsageByClient(ctx, UsageByClientParams{}); return err },
		"Billing.Transactions":         func() error { _, err := c.Billing.Transactions(ctx, PageParams{}); return err },
		"Billing.History":              func() error { _, err := c.Billing.History(ctx, PageParams{}); return err },
		"Billing.PaymentMethods":       func() error { _, err := c.Billing.PaymentMethods(ctx); return err },
		"Billing.PortalURL":            func() error { _, err := c.Billing.PortalURL(ctx); return err },
		"Billing.CheckoutPlan":         func() error { _, err := c.Billing.CheckoutPlan(ctx, CheckoutPlanParams{Plan: PlanPro}); return err },
		"Webhooks.List":                func() error { _, err := c.Webhooks.List(ctx); return err },
		"Webhooks.Get":                 func() error { _, err := c.Webhooks.Get(ctx, "whe_1"); return err },
		"Webhooks.Test":                func() error { _, err := c.Webhooks.Test(ctx, "whe_1"); return err },
		"Webhooks.ListDeliveries":      func() error { _, err := c.Webhooks.ListDeliveries(ctx, ListDeliveriesParams{}); return err },
		"Webhooks.RetryDelivery":       func() error { _, err := c.Webhooks.RetryDelivery(ctx, "whd_1"); return err },
		"Storage.List":                 func() error { _, err := c.Storage.List(ctx); return err },
		"Storage.Get":                  func() error { _, err := c.Storage.Get(ctx, "media"); return err },
		"Storage.Stats":                func() error { _, err := c.Storage.Stats(ctx, "media"); return err },
		"Storage.ListObjects":          func() error { _, err := c.Storage.ListObjects(ctx, "media", ListObjectsParams{}); return err },
		"Storage.LookUpS3Bucket":       func() error { _, err := c.Storage.LookUpS3Bucket(ctx, "b"); return err },
		"Storage.ListAccountBuckets":   func() error { _, err := c.Storage.ListAccountBuckets(ctx, AccountBucketsParams{}); return err },
		"Storage.ListSupabaseProjects": func() error { _, err := c.Storage.ListSupabaseProjects(ctx, "st_1"); return err },
		"APIKeys.List":                 func() error { _, err := c.APIKeys.List(ctx); return err },
		"Orgs.List":                    func() error { _, err := c.Orgs.List(ctx); return err },
		"Orgs.Current":                 func() error { _, err := c.Orgs.Current(ctx); return err },
		"Orgs.GetSettings":             func() error { _, err := c.Orgs.GetSettings(ctx); return err },
		"Compose.Effects":              func() error { _, err := c.Compose.Effects(ctx); return err },
		"Compose.EngineEffects":        func() error { _, err := c.Compose.EngineEffects(ctx); return err },
		"Compose.Validate": func() error {
			_, err := c.Compose.Validate(ctx, ComposeValidateParams{Params: map[string]any{}})
			return err
		},
		"Assets.List":            func() error { _, err := c.Assets.List(ctx, ListAssetsParams{}); return err },
		"Assets.Get":             func() error { _, err := c.Assets.Get(ctx, "asset_1"); return err },
		"Assets.Download":        func() error { _, err := c.Assets.Download(ctx, "asset_1"); return err },
		"Notifications.List":     func() error { _, err := c.Notifications.List(ctx); return err },
		"Notifications.MarkRead": func() error { _, err := c.Notifications.MarkRead(ctx, MarkReadParams{IDs: []string{"n"}}); return err },
		"Orgs.Members.Invite": func() error {
			_, err := c.Orgs.Members.Invite(ctx, InviteMemberParams{Email: "a@b.test", Role: MemberRoleMember})
			return err
		},
	}
	for name, call := range calls {
		if err := call(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
