package tid

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/hbaldwin98/control-center/host"
	hostbrowser "github.com/hbaldwin98/control-center/host/browser"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
)

const apiBaseURL = "https://tid-ocx-prod-be.originsmartops.com"

var apiHosts = []string{"tid-ocx-prod-be.originsmartops.com"}

type loginData struct {
	AccessToken string `json:"accessToken"`
	Email       string `json:"email"`
	Username    string `json:"username"`
	FirstName   string `json:"firstName"`
	LastName    string `json:"lastName"`
}

type billPeriod struct {
	UsageStart string `json:"usagePeriodStartDateTime"`
	UsageEnd   string `json:"usagePeriodEndDateTime"`
}

type collectedPeriod struct {
	Start          string
	End            string
	PeakDemandDate string
	PeakDemandKW   *float64
	Readings       []Reading
}

type collection struct {
	Readings []Reading
	Periods  []collectedPeriod
}

func (p *Plugin) collect(jc hostjobs.Context, h host.Host, cfg settings, includeHistory bool) (collection, error) {
	if strings.TrimSpace(cfg.Username) == "" || strings.TrimSpace(cfg.CredentialID) == "" || strings.TrimSpace(cfg.TenantID) == "" {
		return collection{}, hostjobs.Permanent(fmt.Errorf("tid: set tenant_id, username, and a password credential_id in plugin config"))
	}

	if err := jc.Progress(0.1, "signing in to TID"); err != nil {
		return collection{}, err
	}
	loginBody, _ := json.Marshal(map[string]string{"email": cfg.Username})
	loginResponse, err := apiRequest(jc, h.Browser(), cfg.TenantID, "", http.MethodPost, "/auth/login", loginBody, &hostbrowser.JSONCredential{
		ID: cfg.CredentialID, Field: "password",
	})
	if err != nil {
		return collection{}, fmt.Errorf("tid: login: %w", err)
	}
	var loginEnvelope struct {
		Data loginData `json:"data"`
	}
	if err := json.Unmarshal(loginResponse, &loginEnvelope); err != nil {
		return collection{}, fmt.Errorf("tid: decode login response: %w", err)
	}
	login := loginEnvelope.Data
	if login.AccessToken == "" || login.Username == "" {
		return collection{}, hostjobs.Permanent(fmt.Errorf("tid: login response omitted accessToken or username"))
	}

	if err := jc.Progress(0.25, "loading TID account"); err != nil {
		return collection{}, err
	}
	userResponse, err := apiRequest(jc, h.Browser(), cfg.TenantID, login.AccessToken, http.MethodGet, "/user/user-details", nil, nil)
	if err != nil {
		return collection{}, fmt.Errorf("tid: user details: %w", err)
	}
	var userEnvelope struct {
		UserDetails struct {
			Accounts []struct {
				AccountID string `json:"accountId"`
			} `json:"accounts"`
		} `json:"userDetails"`
	}
	if err := json.Unmarshal(userResponse, &userEnvelope); err != nil {
		return collection{}, fmt.Errorf("tid: decode user details: %w", err)
	}
	if len(userEnvelope.UserDetails.Accounts) == 0 || userEnvelope.UserDetails.Accounts[0].AccountID == "" {
		return collection{}, hostjobs.Permanent(fmt.Errorf("tid: user details contained no account"))
	}
	accountID := userEnvelope.UserDetails.Accounts[0].AccountID

	activeBody, _ := json.Marshal(map[string]any{
		"payload":  map[string]string{"accountId": accountID, "action": "READ"},
		"username": login.Username,
	})
	activeResponse, err := apiRequest(jc, h.Browser(), cfg.TenantID, login.AccessToken, http.MethodPost, "/ouaf/get-active-services", activeBody, nil)
	if err != nil {
		return collection{}, fmt.Errorf("tid: active services: %w", err)
	}
	agreements, err := electricAgreements(activeResponse)
	if err != nil {
		return collection{}, fmt.Errorf("tid: active services response: %w", err)
	}
	if len(agreements) == 0 {
		return collection{}, hostjobs.Permanent(fmt.Errorf("tid: account contained no electric service agreements"))
	}

	if err := jc.Progress(0.45, "loading current billing periods"); err != nil {
		return collection{}, err
	}
	var readings []Reading
	periodsByRange := map[string]*collectedPeriod{}
	periodOrder := []string{}
	for _, agreement := range agreements {
		saID := stringValue(agreement, "saId")
		billBody, _ := json.Marshal(map[string]any{
			"payload":                  map[string]string{"accountId": accountID, "action": "READ", "saId": saID},
			"selectedServiceAgreement": agreement,
			"username":                 login.Username,
		})
		billResponse, err := apiRequest(jc, h.Browser(), cfg.TenantID, login.AccessToken, http.MethodPost, "/ouaf/get-bill-data-extract", billBody, nil)
		if err != nil {
			return collection{}, fmt.Errorf("tid: bill data: %w", err)
		}
		periods, err := billPeriods(billResponse)
		if err != nil {
			return collection{}, fmt.Errorf("tid: bill data response: %w", err)
		}
		if !includeHistory {
			periods = periods[:1]
		}
		for _, period := range periods {
			usageBody, _ := json.Marshal(map[string]any{
				"payload": map[string]string{
					"action": "READ", "username": login.Username, "firstname": login.FirstName,
					"lastname": login.LastName, "emailAddress": login.Email, "accountId": accountID,
					"personId": "", "saId": saID, "viewModeFlg": "D2BB",
					"usagePeriodStartDateTime": period.UsageStart, "usagePeriodEndDateTime": period.UsageEnd,
				},
				"selectedServiceAgreement": agreement,
				"username":                 login.Username,
			})
			usageResponse, err := apiRequest(jc, h.Browser(), cfg.TenantID, login.AccessToken, http.MethodPost, "/ouaf/retrieve-usage-for-sa", usageBody, nil)
			if err != nil {
				return collection{}, fmt.Errorf("tid: usage: %w", err)
			}
			periodReadings, peakDate, peakKW, err := parseUsage(usageResponse)
			if err != nil {
				return collection{}, fmt.Errorf("tid: decode usage: %w", err)
			}
			readings = append(readings, periodReadings...)
			periodStart, periodEnd := parseDay(period.UsageStart), parseDay(period.UsageEnd)
			key := periodStart + "\x00" + periodEnd
			stored := periodsByRange[key]
			if stored == nil {
				stored = &collectedPeriod{Start: periodStart, End: periodEnd}
				periodsByRange[key] = stored
				periodOrder = append(periodOrder, key)
			}
			stored.Readings = append(stored.Readings, periodReadings...)
			if peakKW != nil && (stored.PeakDemandKW == nil || *peakKW > *stored.PeakDemandKW) {
				stored.PeakDemandDate = peakDate
				stored.PeakDemandKW = peakKW
			}
		}
	}
	readings = mergeDays(readings)
	if len(readings) == 0 {
		return collection{}, hostjobs.Permanent(fmt.Errorf("tid: billing history contained no daily readings"))
	}
	periods := make([]collectedPeriod, 0, len(periodOrder))
	for _, key := range periodOrder {
		period := periodsByRange[key]
		period.Readings = mergeDays(period.Readings)
		periods = append(periods, *period)
	}
	_ = jc.Logf("parsed %d daily readings across %d billing periods from %d electric service agreements", len(readings), len(periods), len(agreements))
	return collection{Readings: readings, Periods: periods}, nil
}

func apiRequest(ctx context.Context, browser hostbrowser.Browser, tenantID, token, method, path string, body []byte, credential *hostbrowser.JSONCredential) ([]byte, error) {
	headers := map[string]string{"ocx-tenant-id": tenantID, "Accept": "application/json"}
	if len(body) > 0 {
		headers["Content-Type"] = "application/json"
	}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	res, err := browser.Do(ctx, hostbrowser.OpenOptions{AllowedHosts: apiHosts}, hostbrowser.Request{
		Method: method, URL: apiBaseURL + path, Headers: headers, Body: body, Credential: credential,
	})
	if err != nil {
		return nil, err
	}
	if res.Status < 200 || res.Status >= 300 {
		return nil, fmt.Errorf("unexpected HTTP status %d", res.Status)
	}
	return res.Body, nil
}

func electricAgreements(body []byte) ([]map[string]any, error) {
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	premises := values(envelope.Data["premiseList"])
	var result []map[string]any
	for _, premise := range premises {
		object, ok := premise.(map[string]any)
		if !ok {
			continue
		}
		agreementValue := object["serviceAgreements"]
		if agreementValue == nil {
			agreementValue = object["serviceAggrements"]
		}
		for _, value := range values(agreementValue) {
			agreement, ok := value.(map[string]any)
			if ok && strings.EqualFold(strings.TrimSpace(stringValue(agreement, "serviceType")), "E") && stringValue(agreement, "saId") != "" {
				result = append(result, agreement)
			}
		}
	}
	return result, nil
}

func values(value any) []any {
	if list, ok := value.([]any); ok {
		return list
	}
	if value == nil {
		return nil
	}
	return []any{value}
}

func stringValue(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}

func billPeriods(body []byte) ([]billPeriod, error) {
	var envelope struct {
		Data struct {
			BillHistory []billPeriod `json:"billHistoryList"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	var periods []billPeriod
	seen := map[string]bool{}
	for _, period := range envelope.Data.BillHistory {
		if period.UsageStart != "" && period.UsageEnd != "" {
			key := parseDay(period.UsageStart) + "\x00" + parseDay(period.UsageEnd)
			if seen[key] {
				continue
			}
			seen[key] = true
			periods = append(periods, period)
		}
	}
	if len(periods) == 0 {
		return nil, fmt.Errorf("no complete usage periods")
	}
	return periods, nil
}

func parseUsage(body []byte) ([]Reading, string, *float64, error) {
	var envelope struct {
		Data struct {
			UsageList  []json.RawMessage `json:"usageList"`
			DemandInfo struct {
				PeakDemandDate string   `json:"peakDemandDate"`
				PeakDemandKW   *float64 `json:"peakDemandKw"`
			} `json:"demandInfo"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, "", nil, err
	}
	readings := make([]Reading, 0, len(envelope.Data.UsageList))
	for _, raw := range envelope.Data.UsageList {
		var item map[string]any
		if err := json.Unmarshal(raw, &item); err != nil {
			continue
		}
		if reading, ok := readingFromMap(item); ok {
			readings = append(readings, reading)
		}
	}
	return mergeDays(readings), parseDay(envelope.Data.DemandInfo.PeakDemandDate), envelope.Data.DemandInfo.PeakDemandKW, nil
}
