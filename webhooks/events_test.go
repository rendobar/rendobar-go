package webhooks

import (
	"errors"
	"testing"
	"time"

	"github.com/rendobar/rendobar-go"
)

const at = 1700000000

func parse(t *testing.T, body string) (*Event, error) {
	t.Helper()
	return ParseWith([]byte(body), signedHeader("whsec_x", at, body), "whsec_x", Options{Now: time.Unix(at, 0)})
}

func TestParseReadsAJobCompletedEvent(t *testing.T) {
	body := `{"version":"1","event":"job.completed","deliveryId":"whd_1","timestamp":1700000000123,"orgId":"org_1",
"data":{"jobId":"job_1","jobType":"ffmpeg","status":"complete","output":{"data":null,"file":{"url":"https://cdn.example/o.mp4",
"path":"o.mp4","type":"video","size":10},"files":[],"expiresAt":1700003600000},"cost":{"amount":1000000,"currency":"USD",
"formatted":"$0.0010","breakdown":"x"},"timing":{"createdAt":1700000000000,"completedAt":1700000005000}}}`
	e, err := parse(t, body)
	if err != nil {
		t.Fatal(err)
	}
	if e.Version != "1" || e.Type != JobCompleted || e.ID != "whd_1" || e.OrgID != "org_1" || e.SentAt.UnixMilli() != 1700000000123 {
		t.Fatalf("event %+v", e)
	}
	job, err := e.Job()
	if err != nil {
		t.Fatal(err)
	}
	if job.JobID != "job_1" || job.Status != rendobar.StatusComplete || job.Output == nil || job.Output.File.URL != "https://cdn.example/o.mp4" ||
		job.Cost.Nanodollars != 1000000 || job.CompletedAt.Sub(job.CreatedAt) != 5*time.Second {
		t.Fatalf("job %+v", job)
	}
	if _, err := e.Balance(); err == nil {
		t.Fatal("a job event read as a balance event")
	}
}

func TestParseOtherEvents(t *testing.T) {
	e, err := parse(t, `{"version":"1","event":"job.delivery_failed","deliveryId":"whd_2","timestamp":1,"orgId":"org_1",
"data":{"jobId":"job_1","storageId":"st_1","status":"failed","reason":"destination_denied"}}`)
	if err != nil {
		t.Fatal(err)
	}
	d, err := e.Delivery()
	if err != nil || d.JobID != "job_1" || d.Delivery == nil || d.Delivery.Reason != rendobar.DeliveryReasonDestinationDenied {
		t.Fatalf("%+v %v", d, err)
	}
	e, err = parse(t, `{"version":"1","event":"balance.low","deliveryId":"whd_3","timestamp":1,"orgId":"org_1",
"data":{"balance":500000000,"balanceFormatted":"$0.50","threshold":1000000000,"thresholdFormatted":"$1.00",
"triggeredByJobId":"job_9","triggeredByJobType":"ffmpeg"}}`)
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.Balance()
	if err != nil || b.Nanodollars != 500000000 || b.ThresholdNanodollars != 1000000000 || b.TriggeredByJobID != "job_9" {
		t.Fatalf("%+v %v", b, err)
	}
}

func TestParseKeepsAnUnknownEventAndVersion(t *testing.T) {
	e, err := parse(t, `{"version":"2","event":"job.teleported","deliveryId":"whd_4","timestamp":1,"orgId":"org_1","data":{"to":"mars"}}`)
	if err != nil || e.Type != "job.teleported" || e.Version != "2" || string(e.Data) != `{"to":"mars"}` {
		t.Fatalf("%+v %v", e, err)
	}
	if _, err := e.Job(); err == nil {
		t.Fatal("an unknown event read as a job event")
	}
}

func TestParseNamesTheReason(t *testing.T) {
	body := `{"version":"1","event":"test","deliveryId":"whd_5","timestamp":1,"orgId":"org_1","data":{}}`
	good := signedHeader("whsec_x", at, body)
	now := Options{Now: time.Unix(at, 0)}
	cases := []struct {
		name   string
		body   string
		secret string
		o      Options
		want   error
	}{
		{"no secret", body, "  ", now, ErrNoSecret},
		{"stale", body, "whsec_x", Options{Now: time.Unix(at+301, 0)}, ErrTimestamp},
		{"wrong secret", body, "whsec_y", now, ErrSignature},
		{"tampered", body + " ", "whsec_x", now, ErrSignature},
	}
	for _, c := range cases {
		if _, err := ParseWith([]byte(c.body), good, c.secret, c.o); !errors.Is(err, c.want) {
			t.Fatalf("%s: got %v", c.name, err)
		}
	}
	notJSON := "not json"
	if _, err := ParseWith([]byte(notJSON), signedHeader("whsec_x", at, notJSON), "whsec_x", now); !errors.Is(err, ErrPayload) {
		t.Fatalf("got %v", err)
	}
	h := signedHeader("whsec_x", at, body)
	h.Del("X-Rendobar-Timestamp")
	if _, err := ParseWith([]byte(body), h, "whsec_x", now); !errors.Is(err, ErrTimestamp) {
		t.Fatalf("got %v", err)
	}
}
