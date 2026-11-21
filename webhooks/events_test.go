package webhooks

import (
	"errors"
	"testing"
	"time"

	"github.com/rendobar/rendobar-go"
)

const at = 1700000000

func parse(t *testing.T, body string) *Event {
	t.Helper()
	e, err := ParseWith([]byte(body), signedHeader("whsec_x", at, body), "whsec_x", Options{Now: time.Unix(at, 0)})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestPayloadReadsAJobCompletedEvent(t *testing.T) {
	e := parse(t, `{"version":"1","event":"job.completed","deliveryId":"whd_1","timestamp":1700000000123,"orgId":"org_1",
"data":{"jobId":"job_1","jobType":"ffmpeg","status":"complete","output":{"data":null,"file":{"url":"https://cdn.example/o.mp4",
"path":"o.mp4","type":"video","size":10,"sha256":null},"files":[],"expiresAt":1700003600000},"cost":{"amount":1000000,"currency":"USD",
"formatted":"$0.0010","breakdown":"x"},"timing":{"createdAt":1700000000000,"completedAt":1700000005000}}}`)
	if e.Version != "1" || e.Type != JobCompleted || e.ID != "whd_1" || e.OrgID != "org_1" || e.SentAt.UnixMilli() != 1700000000123 {
		t.Fatalf("event %+v", e)
	}
	payload, err := e.Payload()
	if err != nil {
		t.Fatal(err)
	}
	data, ok := payload.(*JobCompletedData)
	if !ok {
		t.Fatalf("payload %T", payload)
	}
	if data.JobID != "job_1" || data.Output.File.URL != "https://cdn.example/o.mp4" || data.Cost.Amount != 1000000 ||
		data.Cost.Currency != rendobar.CurrencyUSD || data.Cost.Breakdown != "x" ||
		data.Timing.CompletedAt.Time().Sub(data.Timing.CreatedAt.Time()) != 5*time.Second {
		t.Fatalf("data %+v", data)
	}
}

func TestPayloadReadsEveryKnownEvent(t *testing.T) {
	for body, want := range map[string]string{
		`{"version":"1","event":"job.delivery_failed","data":{"jobId":"job_1","storageId":"st_1","status":"failed","reason":"destination_denied"}}`: "*webhooks.JobDeliveryFailedData",
		`{"version":"1","event":"job.deliveries_settled","data":{"jobId":"job_1","deliveries":[{"storageId":"st_1","status":"delivered"}]}}`:        "*webhooks.JobDeliveriesSettledData",
		`{"version":"1","event":"balance.low","data":{"balance":500000000,"threshold":1000000000,"triggeredByJobId":"job_9"}}`:                      "*webhooks.BalanceLowData",
		`{"version":"1","event":"test","data":{"message":"hello","endpointId":"we_1"}}`:                                                             "*webhooks.TestEventData",
	} {
		payload, err := parse(t, body).Payload()
		if err != nil || typeName(payload) != want {
			t.Fatalf("%s: %T %v", body, payload, err)
		}
	}
	settled, _ := parse(t, `{"version":"1","event":"job.deliveries_settled","data":{"jobId":"job_1","deliveries":[{"storageId":"st_1","status":"failed","reason":"already_exists"}]}}`).Payload()
	if d := settled.(*JobDeliveriesSettledData).Deliveries[0]; d.Reason != rendobar.DeliveryReasonAlreadyExists || d.Status != rendobar.DeliveryStatusFailed {
		t.Fatalf("delivery %+v", d)
	}
	low, _ := parse(t, `{"version":"1","event":"balance.low","data":{"balance":500000000,"threshold":1000000000}}`).Payload()
	if b := low.(*BalanceLowData); b.Balance != 500000000 || b.Threshold != 1000000000 {
		t.Fatalf("balance %+v", b)
	}
}

func typeName(v any) string {
	switch v.(type) {
	case *JobDeliveryFailedData:
		return "*webhooks.JobDeliveryFailedData"
	case *JobDeliveriesSettledData:
		return "*webhooks.JobDeliveriesSettledData"
	case *BalanceLowData:
		return "*webhooks.BalanceLowData"
	case *TestEventData:
		return "*webhooks.TestEventData"
	}
	return "other"
}

func TestPayloadKeepsAnUnknownEventAndVersion(t *testing.T) {
	e := parse(t, `{"version":"2","event":"job.teleported","deliveryId":"whd_4","timestamp":1,"orgId":"org_1","data":{"to":"mars"}}`)
	payload, err := e.Payload()
	unknown, ok := payload.(*UnknownPayload)
	if err != nil || !ok || unknown.Type != "job.teleported" || string(unknown.Data) != `{"to":"mars"}` || e.Version != "2" {
		t.Fatalf("%+v %v", payload, err)
	}
}

func TestAnotherEnvelopeVersionIsUnknown(t *testing.T) {
	payload, err := parse(t, `{"version":"2","event":"job.completed","data":{"jobId":{"nested":"differently"}}}`).Payload()
	if _, ok := payload.(*UnknownPayload); err != nil || !ok {
		t.Fatalf("%T %v", payload, err)
	}
}

func TestPayloadNamesUnreadableData(t *testing.T) {
	_, err := parse(t, `{"version":"1","event":"job.completed","data":{"jobId":42}}`).Payload()
	if !errors.Is(err, ErrPayload) {
		t.Fatalf("got %v", err)
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
