package tid

import (
	"bytes"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/hbaldwin98/control-center/host"
	hostbrowser "github.com/hbaldwin98/control-center/host/browser"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
)

const (
	portalHost = "my.tid.org"
	loginURL   = "https://my.tid.org/authentication/login"
	usageURL   = "https://my.tid.org/usage/graphs"
)

// Origin CX (Angular) hosts the SPA on my.tid.org, authenticates against Cognito via
// POST /auth/login on the Origin backend, and loads usage with POST /ouaf/retrieve-usage-for-sa.
// Those XHRs must be allowlisted or Playwright abort them and login never completes.
var allowedHosts = []string{
	"my.tid.org",
	"www.tid.org",
	"tid.org",
	"tid-ocx-prod-be.originsmartops.com",
	"us-east-1eaqzwamod.auth.us-east-1.amazoncognito.com",
	"cognito-idp.us-east-1.amazonaws.com",
	"fonts.gstatic.com",
	"fonts.googleapis.com",
	"www.googletagmanager.com",
	"www.google-analytics.com",
	"maps.googleapis.com",
}

var userSelectors = []string{
	`input[formControlName=email]`,
	"#email",
	"input[type=email]",
	"input[name=email]",
	"input[name=username]",
	"input[name=user]",
	"input[name=Email]",
	"input[type=text]",
}

var passwordSelectors = []string{
	`input[formControlName=password]`,
	"input[type=password]",
}

// Sign-in is a Material mat-flat-button, not the first <button> on the page
// (language, campaign close, and the password-visibility toggle all come first).
var submitSelectors = []string{
	"button[mat-flat-button]",
	"button[type=submit]",
	"input[type=submit]",
	"button.w-100",
}

var usagePaths = []string{
	"/usage/graphs",
	"/usage/insights",
	"/usage",
	"/dashboard",
}

func (p *Plugin) collect(jc hostjobs.Context, h host.Host, cfg settings) ([]Reading, error) {
	if strings.TrimSpace(cfg.Username) == "" || strings.TrimSpace(cfg.CredentialID) == "" {
		return nil, hostjobs.Permanent(fmt.Errorf("tid: set username and a password credential_id in plugin config"))
	}

	sess, err := h.Browser().Open(jc, hostbrowser.OpenOptions{AllowedHosts: allowedHosts})
	if err != nil {
		return nil, err
	}
	defer sess.Close(jc)

	page, err := sess.NewPage(jc)
	if err != nil {
		return nil, err
	}
	defer page.Close(jc)

	if err := jc.Progress(0.1, "opening My TID"); err != nil {
		return nil, err
	}
	_ = jc.Logf("opening login page %s", loginURL)
	if err := page.Goto(jc, loginURL); err != nil {
		_ = jc.Logf("login page did not load: %v", err)
		return nil, fmt.Errorf("tid: login page: %w", err)
	}
	if err := waitAny(jc, page, passwordSelectors, 45*time.Second); err != nil {
		_ = jc.Logf("no password field matched %d selectors within 45s: %v", len(passwordSelectors), err)
		return nil, fmt.Errorf("tid: no password field on login page: %w", err)
	}

	userFilled := false
	for _, sel := range userSelectors {
		if err := page.Fill(jc, sel, cfg.Username); err == nil {
			userFilled = true
			break
		}
	}
	if !userFilled {
		_ = jc.Logf("no username field matched any of %d selectors; the login page markup has changed", len(userSelectors))
		return nil, hostjobs.Permanent(fmt.Errorf("tid: could not find a username field on the login page"))
	}
	passFilled := false
	for _, sel := range passwordSelectors {
		if err := page.FillCredential(jc, sel, cfg.CredentialID); err == nil {
			passFilled = true
			break
		}
	}
	if !passFilled {
		_ = jc.Logf("no password field accepted credential %s across %d selectors", cfg.CredentialID, len(passwordSelectors))
		return nil, fmt.Errorf("tid: password field")
	}

	clicked := false
	for _, sel := range submitSelectors {
		if err := page.Click(jc, sel); err == nil {
			clicked = true
			break
		}
	}
	if !clicked {
		_ = jc.Logf("no sign-in button matched any of %d selectors", len(submitSelectors))
		return nil, hostjobs.Permanent(fmt.Errorf("tid: could not find a sign-in button"))
	}
	if err := jc.Progress(0.35, "signed in"); err != nil {
		return nil, err
	}
	waitLogin(jc, page, 25*time.Second)

	found := waitHarvest(jc, page, 20*time.Second)
	_ = jc.Logf("after sign-in, harvested %d readings from the landing page", len(found))

	if cfg.UsageURL != "" {
		u, err := url.Parse(cfg.UsageURL)
		if err != nil || u.Scheme != "https" {
			return nil, hostjobs.Permanent(fmt.Errorf("tid: usage_url must be an https URL"))
		}
		res, err := page.Get(jc, cfg.UsageURL)
		if err != nil {
			_ = jc.Logf("configured usage_url %s failed: %v", cfg.UsageURL, err)
			return nil, fmt.Errorf("tid: usage_url: %w", err)
		}
		_ = storeBlob(jc, h, "sync/override", res.MIME, res.Body)
		got := Parse(res.Body, res.MIME)
		_ = jc.Logf("configured usage_url returned %d bytes of %s, parsed %d readings", len(res.Body), res.MIME, len(got))
		if len(got) > len(found) {
			found = got
		}
	}

	if len(found) == 0 {
		if err := jc.Progress(0.55, "opening usage graphs"); err != nil {
			return nil, err
		}
		_ = jc.Logf("no readings yet; opening usage graphs at %s", usageURL)
		if err := page.Goto(jc, usageURL); err != nil {
			_ = jc.Logf("usage graphs: %v", err)
		}
		found = waitHarvest(jc, page, 30*time.Second)
		_ = jc.Logf("usage graphs yielded %d readings", len(found))
	}

	if len(found) == 0 {
		if err := jc.Progress(0.7, "trying usage paths"); err != nil {
			return nil, err
		}
		_ = jc.Logf("still empty; trying %d fallback usage paths", len(usagePaths))
		for _, path := range usagePaths {
			if jc.Err() != nil {
				return nil, jc.Err()
			}
			if err := page.Goto(jc, "https://"+portalHost+path); err != nil {
				_ = jc.Logf("path %s did not load: %v", path, err)
				continue
			}
			got := waitHarvest(jc, page, 8*time.Second)
			_ = jc.Logf("path %s yielded %d readings", path, len(got))
			if len(got) > len(found) {
				found = got
			}
			if len(found) > 0 {
				break
			}
		}
	}

	if len(found) == 0 {
		_ = jc.Logf("exhausted every usage source and found no rows; saving page HTML for inspection")
		if html, err := page.Content(jc); err == nil {
			_ = storeBlob(jc, h, "sync/last.html", "text/html", []byte(html))
		}
		return nil, hostjobs.Permanent(fmt.Errorf("tid: signed in but found no usage rows; on My TID open Usage Graphs and paste the CSV export instead"))
	}
	if html, err := page.Content(jc); err == nil {
		_ = storeBlob(jc, h, "sync/last.html", "text/html", []byte(html))
	}
	if resps, err := page.Responses(jc); err == nil {
		for i := len(resps) - 1; i >= 0; i-- {
			if len(Parse(resps[i].Body, resps[i].MIME)) > 0 {
				_ = storeBlob(jc, h, "sync/last.json", resps[i].MIME, resps[i].Body)
				break
			}
		}
	}
	_ = jc.Logf("parsed %d daily readings", len(found))
	return found, nil
}

func waitLogin(jc hostjobs.Context, page hostbrowser.Page, d time.Duration) {
	deadline := time.Now().Add(d)
	for {
		if resps, err := page.Responses(jc); err == nil {
			for _, r := range resps {
				if r.Status < 400 && strings.Contains(r.URL, "/auth/login") {
					return
				}
			}
		}
		if html, err := page.Content(jc); err == nil && !looksLikeLogin(html) {
			return
		}
		if jc.Err() != nil || !time.Now().Before(deadline) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func looksLikeLogin(html string) bool {
	low := strings.ToLower(html)
	return strings.Contains(low, "formcontrolname=\"password\"") || strings.Contains(low, `type="password"`) || strings.Contains(low, "type=password")
}

func waitAny(jc hostjobs.Context, page hostbrowser.Page, selectors []string, d time.Duration) error {
	var last error
	per := d / time.Duration(len(selectors))
	if per < 2*time.Second {
		per = d
	}
	for _, sel := range selectors {
		last = page.WaitFor(jc, sel, per)
		if last == nil {
			return nil
		}
	}
	return last
}

func waitHarvest(jc hostjobs.Context, page hostbrowser.Page, d time.Duration) []Reading {
	deadline := time.Now().Add(d)
	var found []Reading
	for {
		if got := harvest(jc, page); len(got) > len(found) {
			found = got
		}
		if len(found) > 0 || jc.Err() != nil || !time.Now().Before(deadline) {
			return found
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func harvest(jc hostjobs.Context, page hostbrowser.Page) []Reading {
	var best []Reading
	consider := func(body []byte, mime string) {
		got := Parse(body, mime)
		if len(got) > len(best) {
			best = got
		}
	}
	if resps, err := page.Responses(jc); err == nil {
		for _, r := range resps {
			consider(r.Body, r.MIME)
		}
	}
	if html, err := page.Content(jc); err == nil {
		consider([]byte(html), "text/html")
	}
	return best
}

func storeBlob(ctx hostjobs.Context, h host.Host, key, mime string, body []byte) error {
	if mime == "" {
		mime = "application/octet-stream"
	}
	_, err := h.Blobs().Put(ctx, key, bytes.NewReader(body), mime)
	return err
}
