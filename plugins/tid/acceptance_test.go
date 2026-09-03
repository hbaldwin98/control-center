package tid_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hbaldwin98/control-center/host/hosttest"
	"github.com/hbaldwin98/control-center/plugins/tid"
)

// portalHost is the DNS name the plugin talks to. The double routes it to the mux
// below, so the test needs no HTTP server and no URL rewriting: the plugin's real
// production URL is the one under test.
const portalHost = "tid-ocx-prod-be.originsmartops.com"

const (
	tenant     = "tenant-test"
	credential = "tid-pass"
	password   = "secret"
)

// portal is the utility's API, reduced to what the plugin actually calls. It enforces
// the call order, the tenant and authorization headers, and the request bodies,
// because those are the parts of the integration a regression would silently break.
type portal struct {
	t    *testing.T
	step int
	// days are the three usage dates the fixture reports, oldest first.
	d0, d1, d2 string
	usageCall  int
}

// newPortal dates its fixture relative to the plugin's clock, not the wall clock. The
// summary and history endpoints select a window ending "now", so a fixture built from
// time.Now() would fall outside that window and those endpoints would correctly return
// nothing.
func newPortal(t *testing.T, now time.Time) *portal {
	return &portal{
		t:  t,
		d0: now.AddDate(0, 0, -6).Format("2006-01-02"),
		d1: now.AddDate(0, 0, -3).Format("2006-01-02"),
		d2: now.AddDate(0, 0, -1).Format("2006-01-02"),
	}
}

// nextStep asserts the plugin calls the portal in the order the portal requires. The
// sequence is load-bearing: each response carries an identifier the next request needs.
func (p *portal) nextStep(w http.ResponseWriter, want int) bool {
	p.step++
	if p.step != want {
		http.Error(w, fmt.Sprintf("request step %d, want %d", p.step, want), http.StatusConflict)
		return false
	}
	return true
}

func (p *portal) checkHeaders(w http.ResponseWriter, r *http.Request, auth bool) bool {
	if r.Header.Get("ocx-tenant-id") != tenant {
		http.Error(w, "tenant header", http.StatusBadRequest)
		return false
	}
	if auth && r.Header.Get("Authorization") != "Bearer access-test" {
		http.Error(w, "authorization header", http.StatusUnauthorized)
		return false
	}
	return true
}

func (p *portal) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /auth/login", func(w http.ResponseWriter, r *http.Request) {
		if !p.nextStep(w, 1) || !p.checkHeaders(w, r, false) {
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		// The password arrives because the host injected it. The plugin only ever
		// named the credential.
		if body["email"] != "person@example.test" || body["password"] != password {
			http.Error(w, "denied", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"accessToken":"access-test","email":"person@example.test","username":"internal-test","firstName":"Test","lastName":"Person"}}`)
	})

	mux.HandleFunc("GET /user/user-details", func(w http.ResponseWriter, r *http.Request) {
		if !p.nextStep(w, 2) || !p.checkHeaders(w, r, true) {
			return
		}
		_, _ = io.WriteString(w, `{"userDetails":{"accounts":[{"accountId":"account-test"}]}}`)
	})

	mux.HandleFunc("POST /ouaf/get-active-services", func(w http.ResponseWriter, r *http.Request) {
		if !p.nextStep(w, 3) || !p.checkHeaders(w, r, true) {
			return
		}
		var body struct {
			Payload  map[string]string `json:"payload"`
			Username string            `json:"username"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Payload["accountId"] != "account-test" || body.Payload["action"] != "READ" || body.Username != "internal-test" {
			http.Error(w, "active services request", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"premiseList":{"serviceAgreements":{"saId":"service-test","serviceType":"E","saRateSchedule":{"rateSchedule":"TEST"},"ServicePoints":{"meterId":"meter-test"}}}}}`)
	})

	mux.HandleFunc("POST /ouaf/get-bill-data-extract", func(w http.ResponseWriter, r *http.Request) {
		if !p.nextStep(w, 4) || !p.checkHeaders(w, r, true) {
			return
		}
		var body struct {
			Payload                  map[string]string `json:"payload"`
			SelectedServiceAgreement map[string]any    `json:"selectedServiceAgreement"`
			Username                 string            `json:"username"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Payload["accountId"] != "account-test" || body.Payload["action"] != "READ" ||
			body.Payload["saId"] != "service-test" || body.Username != "internal-test" {
			http.Error(w, "bill data request", http.StatusBadRequest)
			return
		}
		if body.SelectedServiceAgreement["saId"] != "service-test" || body.SelectedServiceAgreement["ServicePoints"] == nil {
			http.Error(w, "bill service agreement", http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, `{"data":{"billHistoryList":[{"usagePeriodStartDateTime":"%sT00:00:00-07:00","usagePeriodEndDateTime":"%sT23:59:59-07:00"},{"usagePeriodStartDateTime":"%sT00:00:00-07:00","usagePeriodEndDateTime":"%sT23:59:59-07:00"}]}}`,
			p.d1, p.d2, p.d0, p.d0)
	})

	mux.HandleFunc("POST /ouaf/retrieve-usage-for-sa", func(w http.ResponseWriter, r *http.Request) {
		p.usageCall++
		if !p.nextStep(w, 4+p.usageCall) || !p.checkHeaders(w, r, true) {
			return
		}
		var body struct {
			Payload                  map[string]string `json:"payload"`
			SelectedServiceAgreement map[string]any    `json:"selectedServiceAgreement"`
			Username                 string            `json:"username"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Payload["action"] != "READ" || body.Payload["username"] != "internal-test" ||
			body.Payload["firstname"] != "Test" || body.Payload["lastname"] != "Person" ||
			body.Payload["emailAddress"] != "person@example.test" || body.Payload["accountId"] != "account-test" ||
			body.Payload["personId"] != "" || body.Payload["saId"] != "service-test" ||
			body.Payload["viewModeFlg"] != "D2BB" || body.Username != "internal-test" {
			http.Error(w, "usage request", http.StatusBadRequest)
			return
		}
		if body.SelectedServiceAgreement["saId"] != "service-test" || body.SelectedServiceAgreement["saRateSchedule"] == nil {
			http.Error(w, "selected service agreement", http.StatusBadRequest)
			return
		}
		if p.usageCall == 1 {
			fmt.Fprintf(w, `{"status":"OK","data":{"usageList":[{"costDate":"%s","usage":"12.5","dailyCost":3.25,"onPeakKwh":5.5,"offPeakKwh":7},{"costDate":"%s","usage":"8","dailyCost":2,"onPeakKwh":3,"offPeakKwh":5}],"demandInfo":{"peakDemandDate":"%s","peakDemandKw":7.92}}}`,
				p.d1, p.d2, p.d2)
			return
		}
		fmt.Fprintf(w, `{"status":"OK","data":{"usageList":[{"costDate":"%s","usage":"9","dailyCost":2.25,"onPeakKwh":4,"offPeakKwh":5}],"demandInfo":{"peakDemandDate":"%s","peakDemandKw":4.5}}}`,
			p.d0, p.d0)
	})

	return mux
}

func TestCollectsUsageFromPortal(t *testing.T) {
	ctx := context.Background()
	h := hosttest.New(t, tid.New())
	p := newPortal(t, h.Clock.Now())
	h.Browser.Handle(portalHost, p.handler())
	h.Browser.Credential(credential, password)
	h.Run(ctx)
	h.SetConfig(ctx, map[string]any{
		"tenant_id":     tenant,
		"username":      "person@example.test",
		"credential_id": credential,
		"cents_per_kwh": 0,
	})

	var posted struct {
		JobID int64 `json:"jobId"`
	}
	h.DecodeJSON(h.POST("/history/sync", nil), http.StatusOK, &posted)
	if posted.JobID == 0 {
		t.Fatal("POST /history/sync returned no job id")
	}
	if err := h.RunJob(ctx, posted.JobID); err != nil {
		t.Fatalf("sync job failed: %v", err)
	}

	// The daily summary carries one row per day the portal reported.
	var page struct {
		Days []struct {
			Day string  `json:"day"`
			KWh float64 `json:"kwh"`
		} `json:"days"`
	}
	h.DecodeJSON(h.GET("/summary"), http.StatusOK, &page)
	if len(page.Days) != 3 {
		t.Fatalf("summary days = %+v, want 3", page.Days)
	}
	byDay := map[string]float64{}
	for _, d := range page.Days {
		byDay[d.Day] = d.KWh
	}
	if byDay[p.d0] != 9 || byDay[p.d1] != 12.5 || byDay[p.d2] != 8 {
		t.Fatalf("summary days = %+v", page.Days)
	}

	// History groups those days into billing periods and keeps the peak demand.
	var history struct {
		Periods []struct {
			Start, End, PeakDemandDate      string
			TotalKWh, OnPeakKWh, OffPeakKWh float64
			PeakDemandKW                    *float64               `json:"peakDemandKw"`
			Days                            []struct{ Day string } `json:"days"`
		} `json:"periods"`
	}
	h.DecodeJSON(h.GET("/history"), http.StatusOK, &history)
	if len(history.Periods) != 2 {
		t.Fatalf("history periods = %d, want 2", len(history.Periods))
	}
	first := history.Periods[0]
	if first.TotalKWh != 20.5 || first.OnPeakKWh != 8.5 || first.OffPeakKWh != 12 {
		t.Fatalf("first period totals = %+v", first)
	}
	if first.PeakDemandKW == nil || *first.PeakDemandKW != 7.92 {
		t.Fatalf("first period peak demand = %v, want 7.92", first.PeakDemandKW)
	}
	if len(history.Periods[1].Days) != 1 {
		t.Fatalf("second period days = %d, want 1", len(history.Periods[1].Days))
	}
}

func TestSyncAlertsOnNewReadingsOnly(t *testing.T) {
	ctx := context.Background()
	h := hosttest.New(t, tid.New())
	p := newPortal(t, h.Clock.Now())
	h.Browser.Handle(portalHost, p.handler())
	h.Browser.Credential(credential, password)
	h.Run(ctx)
	h.SetConfig(ctx, map[string]any{
		"tenant_id":     tenant,
		"username":      "person@example.test",
		"credential_id": credential,
		"cents_per_kwh": 0,
	})

	runHistorySync(t, ctx, h)
	if n := countEvents(h, "tid.alert"); n != 1 {
		t.Fatalf("after first sync, tid.alert events = %d, published %v", n, eventTypes(h))
	}

	p.step = 0
	p.usageCall = 0
	runHistorySync(t, ctx, h)
	if n := countEvents(h, "tid.alert"); n != 1 {
		t.Fatalf("a repeat sync without new days re-alerted: tid.alert events = %d", n)
	}
}

func TestSyncPublishesLatestReadingAndInsight(t *testing.T) {
	ctx := context.Background()
	h := hosttest.New(t, tid.New())
	p := newPortal(t, h.Clock.Now())
	h.Browser.Handle(portalHost, p.handler())
	h.Browser.Credential(credential, password)
	if err := h.AI.ReplyJSON("cheap-chat", map[string]any{
		"summary":        "Usage is steady across the week.",
		"recommendation": "Run the dishwasher after 9pm.",
		"anomalies":      []string{},
	}); err != nil {
		t.Fatal(err)
	}
	h.Run(ctx)
	h.SetConfig(ctx, map[string]any{
		"tenant_id":     tenant,
		"username":      "person@example.test",
		"credential_id": credential,
		"cents_per_kwh": 0,
	})

	runHistorySync(t, ctx, h)

	syncPay := payloadOf(t, h, "tid.synced")
	if syncPay["day"] != p.d2 {
		t.Fatalf("synced day = %v, want the newest portal day %s; payload %#v", syncPay["day"], p.d2, syncPay)
	}
	if syncPay["kwh"] != 8.0 {
		t.Fatalf("synced kwh = %v, want 8; payload %#v", syncPay["kwh"], syncPay)
	}
	body, _ := syncPay["body"].(string)
	if body == "" || !strings.Contains(body, p.d2) || !strings.Contains(body, "8") {
		t.Fatalf("synced body = %q, want the day's reading", body)
	}

	yesterday, _ := syncPay["yesterday"].(map[string]any)
	if yesterday == nil || yesterday["day"] != p.d2 || yesterday["kwh"] != 8.0 {
		t.Fatalf("synced yesterday = %#v, want %s at 8 kWh", syncPay["yesterday"], p.d2)
	}
	settled, _ := syncPay["settled"].(map[string]any)
	if settled == nil || settled["day"] != p.d2 {
		t.Fatalf("synced settled = %#v, want %s", syncPay["settled"], p.d2)
	}
	recent, _ := syncPay["recent"].([]any)
	if len(recent) != 3 {
		t.Fatalf("synced recent = %#v, want the 3 portal days newest first", syncPay["recent"])
	}
	if first, _ := recent[0].(map[string]any); first == nil || first["day"] != p.d2 {
		t.Fatalf("synced recent[0] = %#v, want %s", recent[0], p.d2)
	}

	insightPay := payloadOf(t, h, "tid.insight")
	if insightPay["summary"] != "Usage is steady across the week." {
		t.Fatalf("insight summary = %v", insightPay["summary"])
	}
	insightBody, _ := insightPay["body"].(string)
	if !strings.Contains(insightBody, "Usage is steady") || !strings.Contains(insightBody, "dishwasher") {
		t.Fatalf("insight body = %q", insightBody)
	}
}

func payloadOf(t *testing.T, h *hosttest.Harness, typ string) map[string]any {
	t.Helper()
	for _, e := range h.Events() {
		if e.Type != typ {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(e.Payload, &payload); err != nil {
			t.Fatalf("%s payload: %v", typ, err)
		}
		return payload
	}
	t.Fatalf("no %s event in %v", typ, eventTypes(h))
	return nil
}

func runHistorySync(t *testing.T, ctx context.Context, h *hosttest.Harness) {
	t.Helper()
	var posted struct {
		JobID int64 `json:"jobId"`
	}
	h.DecodeJSON(h.POST("/history/sync", nil), http.StatusOK, &posted)
	if posted.JobID == 0 {
		t.Fatal("POST /history/sync returned no job id")
	}
	if err := h.RunJob(ctx, posted.JobID); err != nil {
		t.Fatalf("sync job failed: %v", err)
	}
}

func eventTypes(h *hosttest.Harness) []string {
	var out []string
	for _, e := range h.Events() {
		out = append(out, e.Type)
	}
	return out
}

func countEvents(h *hosttest.Harness, typ string) int {
	n := 0
	for _, e := range h.Events() {
		if e.Type == typ {
			n++
		}
	}
	return n
}
