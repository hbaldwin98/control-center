package pagewatch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hbaldwin98/control-center/host"
	hostai "github.com/hbaldwin98/control-center/host/ai"
	hostbrowser "github.com/hbaldwin98/control-center/host/browser"
	hostevents "github.com/hbaldwin98/control-center/host/events"
	hostjobs "github.com/hbaldwin98/control-center/host/jobs"
	hoststorage "github.com/hbaldwin98/control-center/host/storage"
)

const (
	snapshotKey    = "latest.txt"
	maxStoredText  = 512 << 10
	maxPromptText  = 480
	maxSummaryText = 500
	maxAITokens    = 64
	aiInterval     = 24 * time.Hour
)

var (
	hiddenMarkup = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script\s*>|<style\b[^>]*>.*?</style\s*>|<noscript\b[^>]*>.*?</noscript\s*>|<svg\b[^>]*>.*?</svg\s*>`)
	comments     = regexp.MustCompile(`(?s)<!--.*?-->`)
	tags         = regexp.MustCompile(`(?s)<[^>]*>`)
)

type checkEvent struct {
	CheckedAt     string `json:"checkedAt"`
	URL           string `json:"url"`
	Status        string `json:"status"`
	ContentHash   string `json:"contentHash"`
	ExpectedText  string `json:"expectedText"`
	ExpectedFound bool   `json:"expectedFound"`
	Summary       string `json:"summary"`
	AIRan         bool   `json:"aiRan"`
	InputTokens   int64  `json:"inputTokens"`
	OutputTokens  int64  `json:"outputTokens"`
	CostMicroUSD  int64  `json:"costMicroUsd"`
	BrowserMS     int64  `json:"browserMs"`
	AIMS          int64  `json:"aiMs"`
	JobID         int64  `json:"jobId"`
}

// alertEvent is the payload behind the alert event. It is a struct, not a map,
// so the manifest can be derived from it rather than restate it.
type alertEvent struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

func (p *Plugin) check(jc hostjobs.Context) error {
	h, ok := p.host()
	if !ok {
		return fmt.Errorf("pagewatch: host is not initialized")
	}
	s := p.settings()
	target, allowedHost, err := validateTarget(s.URL)
	if err != nil {
		_ = jc.Logf("target %q rejected: %v", s.URL, err)
		return hostjobs.Permanent(err)
	}
	_ = jc.Logf("checking %s (host %s, expected text %q)", target, allowedHost, s.ExpectedText)

	if err := jc.Progress(0.05, "opening page"); err != nil {
		return err
	}
	browserStart := time.Now()
	visible, err := fetchVisibleText(jc, h, target, allowedHost)
	if err != nil {
		_ = jc.Logf("browser failed after %dms: %v", time.Since(browserStart).Milliseconds(), err)
		return fmt.Errorf("pagewatch: browser: %w", err)
	}
	browserMS := time.Since(browserStart).Milliseconds()
	if visible == "" {
		_ = jc.Logf("browser returned 0 bytes of visible text after %dms", browserMS)
		return fmt.Errorf("pagewatch: page has no visible text")
	}
	_ = jc.Logf("fetched %d bytes of visible text in %dms", len(visible), browserMS)

	var previousURL, previousHash, previousSummary, lastAIAt, previousCheckedAt string
	err = h.Store().QueryRow(jc, `
		SELECT url, content_hash, last_summary, last_ai_at, checked_at
		FROM pagewatch_state WHERE id = 1`).Scan(
		&previousURL, &previousHash, &previousSummary, &lastAIAt, &previousCheckedAt)
	if err != nil {
		return fmt.Errorf("pagewatch: read state: %w", err)
	}

	hash := sha256.Sum256([]byte(visible))
	contentHash := hex.EncodeToString(hash[:])
	status := checkStatus(previousURL, previousHash, target, contentHash)
	expectedFound := s.ExpectedText == "" || strings.Contains(
		strings.ToLower(visible), strings.ToLower(s.ExpectedText),
	)
	if !expectedFound {
		status = "attention"
	}
	_ = jc.Logf("status %s (hash %s, previous %s, expected text present: %t, last checked %s)",
		status, shortHash(contentHash), shortHash(previousHash), expectedFound, orNever(previousCheckedAt))

	now := h.Clock().Now().UTC()
	lastAI, _ := time.Parse(time.RFC3339Nano, lastAIAt)
	aiRan := status != "unchanged" || lastAI.IsZero() || now.Sub(lastAI) >= aiInterval
	summary := previousSummary
	var inputTokens, outputTokens, costMicroUSD, aiMS int64
	if aiRan {
		if err := jc.Progress(0.5, "asking model"); err != nil {
			return err
		}
		_ = jc.Logf("asking model (reason: %s; last ran %s)", aiReason(status, lastAI), orNever(lastAIAt))
		aiStart := time.Now()
		resp, err := h.AI().Chat(jc, hostai.ChatRequest{
			Model:     "cheap-chat",
			MaxTokens: maxAITokens,
			Messages: []hostai.Message{{Role: hostai.RoleUser, Text: checkPrompt(
				target, status, s.ExpectedText, expectedFound, visible,
			)}},
		})
		if err != nil {
			_ = jc.Logf("model call failed after %dms: %v", time.Since(aiStart).Milliseconds(), err)
			return fmt.Errorf("pagewatch: ai: %w", err)
		}
		aiMS = time.Since(aiStart).Milliseconds()
		_ = jc.Logf("model answered in %dms (%d in / %d out tokens, %d microUSD)",
			aiMS, resp.Usage.InputTokens, resp.Usage.OutputTokens, int64(resp.Usage.CostMicroUSD))
		summary = truncateUTF8(strings.TrimSpace(resp.Text), maxSummaryText)
		inputTokens = resp.Usage.InputTokens
		outputTokens = resp.Usage.OutputTokens
		costMicroUSD = int64(resp.Usage.CostMicroUSD)
		lastAIAt = now.Format(time.RFC3339Nano)
	}
	if !aiRan {
		_ = jc.Logf("skipped the model: unchanged and last asked %s (interval %s)", orNever(lastAIAt), aiInterval)
	}
	if summary == "" {
		summary = "Page loaded and the expected text was present."
	}

	if err := jc.Progress(0.8, "saving result"); err != nil {
		return err
	}
	stored := truncateUTF8(visible, maxStoredText)
	if _, err := h.Blobs().Put(jc, snapshotKey, bytes.NewBufferString(stored), "text/plain; charset=utf-8"); err != nil {
		_ = jc.Logf("saving snapshot %s failed: %v", snapshotKey, err)
		return fmt.Errorf("pagewatch: save snapshot: %w", err)
	}
	_ = jc.Logf("saved %d byte snapshot to %s", len(stored), snapshotKey)

	result := checkEvent{
		CheckedAt: now.Format(time.RFC3339Nano), URL: target, Status: status,
		ContentHash: contentHash, ExpectedText: s.ExpectedText, ExpectedFound: expectedFound,
		Summary: summary, AIRan: aiRan, InputTokens: inputTokens, OutputTokens: outputTokens,
		CostMicroUSD: costMicroUSD, BrowserMS: browserMS, AIMS: aiMS, JobID: jc.JobID(),
	}
	err = h.Store().Tx(jc, func(tx hoststorage.Tx) error {
		if _, err := tx.Exec(jc, `UPDATE pagewatch_state
			SET url = ?, content_hash = ?, last_summary = ?, last_ai_at = ?, checked_at = ?
			WHERE id = 1`, target, contentHash, summary, lastAIAt, result.CheckedAt); err != nil {
			return err
		}
		if err := h.Events().PublishTx(jc, tx, "check.completed", allowedHost, result); err != nil {
			return err
		}
		if status == "attention" {
			return h.Events().PublishTx(jc, tx, "alert", allowedHost+": expected text missing", alertEvent{
				Title: "Page Watch needs attention",
				Body:  fmt.Sprintf("%q was not found on %s", s.ExpectedText, target),
			})
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("pagewatch: commit result: %w", err)
	}
	return jc.Progress(1, "complete")
}

func fetchVisibleText(ctx context.Context, h host.Host, target, allowedHost string) (string, error) {
	session, err := h.Browser().Open(ctx, hostbrowser.OpenOptions{AllowedHosts: []string{allowedHost}})
	if err != nil {
		return "", err
	}
	defer session.Close(ctx)
	page, err := session.NewPage(ctx)
	if err != nil {
		return "", err
	}
	defer page.Close(ctx)
	if err := page.Goto(ctx, target); err != nil {
		return "", err
	}
	if err := page.WaitFor(ctx, "body", 15*time.Second); err != nil {
		return "", err
	}
	raw, err := page.Content(ctx)
	if err != nil {
		return "", err
	}
	return visibleText(raw), nil
}

func validateTarget(raw string) (target, host string, err error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return "", "", fmt.Errorf("pagewatch: target must be a public HTTPS URL")
	}
	if u.User != nil || (u.Port() != "" && u.Port() != "443") || net.ParseIP(u.Hostname()) != nil {
		return "", "", fmt.Errorf("pagewatch: target cannot contain userinfo, an IP address, or an alternate port")
	}
	host = strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	return u.String(), host, nil
}

func checkStatus(previousURL, previousHash, target, currentHash string) string {
	if previousHash == "" || previousURL != target {
		return "baseline"
	}
	if previousHash == currentHash {
		return "unchanged"
	}
	return "changed"
}

func checkPrompt(target, status, expected string, found bool, visible string) string {
	prompt := fmt.Sprintf(`Assess this page in at most 40 words. Summarize its current content. Give one action only if it appears broken or the expected text is missing. Do not speculate.
URL: %s
Result: %s
Expected %q present: %t
Visible text: %s`, truncateUTF8(target, 100), status, truncateUTF8(expected, 60), found, visible)
	return truncateUTF8(prompt, maxPromptText)
}

func visibleText(raw string) string {
	raw = hiddenMarkup.ReplaceAllString(raw, " ")
	raw = comments.ReplaceAllString(raw, " ")
	raw = tags.ReplaceAllString(raw, " ")
	raw = html.UnescapeString(raw)
	return strings.Join(strings.Fields(raw), " ")
}

func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	end := max
	for end > 0 && !utf8.ValidString(s[:end]) {
		end--
	}
	return strings.TrimSpace(s[:end])
}

func (p *Plugin) onCheckCompleted(ctx context.Context, tx hoststorage.Tx, event hostevents.Event) error {
	var c checkEvent
	if err := json.Unmarshal(event.Payload, &c); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO pagewatch_checks
		(checked_at, url, status, content_hash, expected_text, expected_found, summary,
		 ai_ran, input_tokens, output_tokens, cost_micro_usd, browser_ms, ai_ms, job_id, event_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.CheckedAt, c.URL, c.Status, c.ContentHash, c.ExpectedText, boolInt(c.ExpectedFound), c.Summary,
		boolInt(c.AIRan), c.InputTokens, c.OutputTokens, c.CostMicroUSD, c.BrowserMS, c.AIMS, c.JobID, event.ID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM pagewatch_checks WHERE id NOT IN
		(SELECT id FROM pagewatch_checks ORDER BY id DESC LIMIT 100)`)
	return err
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// shortHash keeps a content hash readable in a job log line.
func shortHash(h string) string {
	if h == "" {
		return "none"
	}
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func orNever(ts string) string {
	if ts == "" {
		return "never"
	}
	return ts
}

// aiReason says why the model was consulted, so a log line explains a cost rather than
// only recording it.
func aiReason(status string, lastAI time.Time) string {
	switch {
	case status != "unchanged":
		return "status is " + status
	case lastAI.IsZero():
		return "no previous run"
	default:
		return "refresh interval elapsed"
	}
}
