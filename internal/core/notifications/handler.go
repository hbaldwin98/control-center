package notifications

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

func (s *Service) handleEvent(ctx context.Context, tx storage.Tx, e events.Event) error {
	if skipNotificationEvent(e.Type) {
		return nil
	}
	rules, err := s.loadEnabledRules(ctx, tx)
	if err != nil {
		return err
	}
	payload := decodePayload(e.Payload)
	now := s.now()
	for _, r := range rules {
		if err := s.applyRule(ctx, tx, r, e, payload, now); err != nil {
			return err
		}
	}
	return nil
}

type storedRule struct {
	Rule
	pattern events.Pattern
}

func (s *Service) loadEnabledRules(ctx context.Context, q interface {
	Query(ctx context.Context, q string, args ...any) (*sql.Rows, error)
}) ([]storedRule, error) {
	rows, err := q.Query(ctx, `SELECT id, enabled, match, where_expr, channels, title, body, url, throttle_s
		FROM core_notification_rules WHERE enabled = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storedRule
	for rows.Next() {
		var r storedRule
		var enabled int
		var ch string
		var throttle int64
		if err := rows.Scan(&r.ID, &enabled, &r.Match, &r.Where, &ch, &r.Title, &r.Body, &r.URL, &throttle); err != nil {
			return nil, err
		}
		r.Enabled = enabled != 0
		r.Channels = decodeChannels(ch)
		r.Throttle = time.Duration(throttle) * time.Second
		p, err := events.CompilePattern(r.Match)
		if err != nil {
			s.logRuleErr(r.ID, err)
			continue
		}
		r.pattern = p
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) applyRule(ctx context.Context, tx storage.Tx, r storedRule, e events.Event, payload any, now time.Time) error {
	if !r.pattern.Matches(e.Type) {
		return nil
	}
	prg, err := compileWhere(s.env, r.Where)
	if err != nil {
		s.logRuleErr(r.ID, err)
		return nil
	}
	ok, err := evalWhere(prg, e, payload)
	if err != nil {
		s.logRuleErr(r.ID, err)
		return nil
	}
	if !ok {
		return nil
	}
	if r.Throttle > 0 {
		return s.applyThrottled(ctx, tx, r, e, payload, now)
	}
	return s.applyImmediate(ctx, tx, r, e, payload, now)
}

func (s *Service) applyImmediate(ctx context.Context, tx storage.Tx, r storedRule, e events.Event, payload any, now time.Time) error {
	title, body, url, err := render(r.Rule, e, payload, 0)
	if err != nil {
		s.logRuleErr(r.ID, err)
		return nil
	}
	subjS, subjV := throttleSubject(e)
	key := eventUniq(r.ID, e.ID)
	id := newID()
	inInbox := hasInbox(r.Channels)
	res, err := tx.Exec(ctx, `INSERT OR IGNORE INTO core_notifications
		(id, source_event_id, uniqueness_key, rule_id, subject_source, subject_value,
		 title, body, url, collapsed_count, available_at, created_at, in_inbox, ready_published)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, 1)`,
		id, e.ID, key, r.ID, subjS, subjV, title, body, url, rfc(now), rfc(now), boolInt(inInbox))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `INSERT OR IGNORE INTO core_notification_sources(notification_id, event_id) VALUES (?, ?)`,
		id, e.ID); err != nil {
		return err
	}
	if err := s.insertSends(ctx, tx, id, key, r.Channels, now); err != nil {
		return err
	}
	if inInbox {
		if _, err := s.bus.PublishTx(ctx, tx, events.Input{
			Type:    events.TypeNotificationReady,
			Source:  events.SourceNotifications,
			Subject: id,
			Payload: map[string]any{"notificationId": id, "state": "ready"},
		}); err != nil {
			return err
		}
	} else if _, err := s.bus.PublishTx(ctx, tx, events.Input{
		Type:    events.TypeNotificationReady,
		Source:  events.SourceNotifications,
		Subject: id,
		Payload: map[string]any{"notificationId": id, "state": "ready"},
	}); err != nil {
		return err
	}
	s.signal()
	return nil
}

func (s *Service) applyThrottled(ctx context.Context, tx storage.Tx, r storedRule, e events.Event, payload any, now time.Time) error {
	subjS, subjV := throttleSubject(e)
	var notifID, ends string
	err := tx.QueryRow(ctx, `SELECT notification_id, window_ends_at FROM core_notification_throttles
		WHERE rule_id = ? AND subject_source = ? AND subject_value = ? AND window_ends_at > ?
		ORDER BY window_started_at DESC LIMIT 1`, r.ID, subjS, subjV, rfc(now)).Scan(&notifID, &ends)
	if err == nil {
		return s.attachThrottle(ctx, tx, r, e, payload, notifID)
	}
	if !storage.IsNoRows(err) {
		return err
	}
	title, body, url, err := render(r.Rule, e, payload, 0)
	if err != nil {
		s.logRuleErr(r.ID, err)
		return nil
	}
	start := now
	end := now.Add(r.Throttle)
	id := newID()
	key := windowUniq(r.ID, subjS, subjV, rfc(start))
	inInbox := hasInbox(r.Channels)
	res, err := tx.Exec(ctx, `INSERT OR IGNORE INTO core_notifications
		(id, source_event_id, uniqueness_key, rule_id, subject_source, subject_value,
		 title, body, url, collapsed_count, available_at, created_at, in_inbox, ready_published)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, 0)`,
		id, e.ID, key, r.ID, subjS, subjV, title, body, url, rfc(end), rfc(now), boolInt(inInbox))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `INSERT OR IGNORE INTO core_notification_throttles
		(rule_id, subject_source, subject_value, window_started_at, window_ends_at, notification_id)
		VALUES (?, ?, ?, ?, ?, ?)`, r.ID, subjS, subjV, rfc(start), rfc(end), id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT OR IGNORE INTO core_notification_sources(notification_id, event_id) VALUES (?, ?)`,
		id, e.ID); err != nil {
		return err
	}
	if err := s.insertSends(ctx, tx, id, key, r.Channels, end); err != nil {
		return err
	}
	s.signal()
	return nil
}

func (s *Service) attachThrottle(ctx context.Context, tx storage.Tx, r storedRule, e events.Event, payload any, notifID string) error {
	res, err := tx.Exec(ctx, `INSERT OR IGNORE INTO core_notification_sources(notification_id, event_id) VALUES (?, ?)`,
		notifID, e.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE core_notifications SET collapsed_count = collapsed_count + 1 WHERE id = ?`, notifID); err != nil {
		return err
	}
	var collapsed int
	if err := tx.QueryRow(ctx, `SELECT collapsed_count FROM core_notifications WHERE id = ?`, notifID).Scan(&collapsed); err != nil {
		return err
	}
	title, body, url, err := render(r.Rule, e, payload, collapsed)
	if err != nil {
		s.logRuleErr(r.ID, err)
		return nil
	}
	_, err = tx.Exec(ctx, `UPDATE core_notifications SET title = ?, body = ?, url = ? WHERE id = ?`, title, body, url, notifID)
	return err
}

func (s *Service) insertSends(ctx context.Context, tx storage.Tx, notifID, notifKey string, channels []string, next time.Time) error {
	for _, ch := range channels {
		if ch == "inbox" {
			continue
		}
		var kind string
		var enabled int
		err := tx.QueryRow(ctx, `SELECT kind, enabled FROM core_notification_channels WHERE id = ?`, ch).Scan(&kind, &enabled)
		if err != nil {
			if storage.IsNoRows(err) {
				continue
			}
			return err
		}
		if enabled == 0 {
			continue
		}
		sid := newID()
		key := sendUniq(notifKey, ch)
		if _, err := tx.Exec(ctx, `INSERT OR IGNORE INTO core_notification_sends
			(id, notification_id, channel_id, idempotency_key, state, attempts, next_attempt_at)
			VALUES (?, ?, ?, ?, 'pending', 0, ?)`,
			sid, notifID, ch, key, rfc(next)); err != nil {
			return err
		}
	}
	return nil
}

func render(r Rule, e events.Event, payload any, collapsed int) (title, body, url string, err error) {
	title, err = interpolate(r.Title, e, payload, collapsed)
	if err != nil {
		return "", "", "", err
	}
	body, err = interpolate(r.Body, e, payload, collapsed)
	if err != nil {
		return "", "", "", err
	}
	url, err = renderURL(r.URL, e, payload, collapsed)
	return title, body, url, err
}

func renderURL(tmpl string, e events.Event, payload any, collapsed int) (string, error) {
	if tmpl == "" {
		return "", nil
	}
	rendered, err := interpolate(tmpl, e, payload, collapsed)
	if err != nil {
		return "", err
	}
	// URL templates are checked again after interpolation. A payload is data, not
	// permission to leave the application, so a bad or missing value becomes a
	// rule error rather than an external redirect.
	return validateURL(rendered)
}

func isUniqueErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "UNIQUE") || strings.Contains(s, "unique")
}
