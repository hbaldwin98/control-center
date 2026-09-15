package notifications

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

func (s *Service) ListRules(ctx context.Context) ([]Rule, error) {
	rows, err := s.db.Query(ctx, `SELECT id, enabled, match, where_expr, channels, title, body, url, throttle_s
		FROM core_notification_rules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		r, err := scanRule(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if out == nil {
		out = []Rule{}
	}
	return out, rows.Err()
}

func (s *Service) PutRule(ctx context.Context, rule Rule) error {
	actor, err := actor(ctx)
	if err != nil {
		return err
	}
	rule, nerr := s.normalizeRule(rule)
	if nerr != nil {
		return nerr
	}
	ch, err := encodeChannels(rule.Channels)
	if err != nil {
		return err
	}
	return s.db.Tx(ctx, func(tx storage.Tx) error {
		res, err := tx.Exec(ctx, `UPDATE core_notification_rules
			SET enabled = ?, match = ?, where_expr = ?, channels = ?, title = ?, body = ?, url = ?, throttle_s = ?
			WHERE id = ?`,
			boolInt(rule.Enabled), rule.Match, rule.Where, ch, rule.Title, rule.Body, rule.URL,
			int64(rule.Throttle/time.Second), rule.ID)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		action := "update"
		if n == 0 {
			action = "create"
			if err := s.insertRuleTx(ctx, tx, rule); err != nil {
				if isUniqueErr(err) {
					return ErrDuplicateID
				}
				return err
			}
		}
		return s.auditConfig(ctx, tx, actor, "rule", rule.ID, action)
	})
}

func (s *Service) DeleteRule(ctx context.Context, id string) error {
	actor, err := actor(ctx)
	if err != nil {
		return err
	}
	return s.db.Tx(ctx, func(tx storage.Tx) error {
		res, err := tx.Exec(ctx, `DELETE FROM core_notification_rules WHERE id = ?`, id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return ErrUnknownRule
		}
		return s.auditConfig(ctx, tx, actor, "rule", id, "delete")
	})
}

func (s *Service) ListChannels(ctx context.Context) ([]ChannelConfig, error) {
	rows, err := s.db.Query(ctx, `SELECT id, kind, config, credential_id, enabled FROM core_notification_channels ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChannelConfig
	for rows.Next() {
		var c ChannelConfig
		var cfg string
		var enabled int
		if err := rows.Scan(&c.ID, &c.Kind, &cfg, &c.CredentialID, &enabled); err != nil {
			return nil, err
		}
		c.Enabled = enabled != 0
		c.Settings = decodeSettings(cfg)
		out = append(out, c)
	}
	if out == nil {
		out = []ChannelConfig{}
	}
	return out, rows.Err()
}

func (s *Service) PutChannel(ctx context.Context, channel ChannelConfig) error {
	actor, err := actor(ctx)
	if err != nil {
		return err
	}
	if err := s.validateChannel(ctx, channel); err != nil {
		return err
	}
	cfg, err := encodeSettings(channel.Settings)
	if err != nil {
		return err
	}
	return s.db.Tx(ctx, func(tx storage.Tx) error {
		res, err := tx.Exec(ctx, `UPDATE core_notification_channels
			SET kind = ?, config = ?, credential_id = ?, enabled = ? WHERE id = ?`,
			channel.Kind, cfg, channel.CredentialID, boolInt(channel.Enabled), channel.ID)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		action := "update"
		if n == 0 {
			action = "create"
			if _, err := tx.Exec(ctx, `INSERT INTO core_notification_channels(id, kind, config, credential_id, enabled)
				VALUES (?, ?, ?, ?, ?)`, channel.ID, channel.Kind, cfg, channel.CredentialID, boolInt(channel.Enabled)); err != nil {
				if isUniqueErr(err) {
					return ErrDuplicateID
				}
				return err
			}
		}
		if s.refs != nil {
			ids := []string{}
			if channel.CredentialID != "" {
				ids = []string{channel.CredentialID}
			}
			if err := s.refs.ReplaceTx(ctx, tx, "notifications.channel:"+channel.ID, ids); err != nil {
				return err
			}
		}
		if action == "create" && channel.Enabled && isExternalKind(channel.Kind) {
			if err := attachToPluginAlert(ctx, tx, channel.ID); err != nil {
				return err
			}
		}
		return s.auditConfig(ctx, tx, actor, "channel", channel.ID, action)
	})
}

func isExternalKind(kind string) bool {
	return kind == "ntfy" || kind == "webpush" || kind == "cloudflare" || kind == "email"
}

func attachToPluginAlert(ctx context.Context, tx storage.Tx, channelID string) error {
	var raw string
	err := tx.QueryRow(ctx, `SELECT channels FROM core_notification_rules WHERE id = 'plugin-alert'`).Scan(&raw)
	if storage.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}
	ch := decodeChannels(raw)
	for _, c := range ch {
		if c == channelID {
			return nil
		}
	}
	encoded, err := encodeChannels(append(ch, channelID))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE core_notification_rules SET channels = ? WHERE id = 'plugin-alert'`, encoded)
	return err
}

func (s *Service) DeleteChannel(ctx context.Context, id string) error {
	actor, err := actor(ctx)
	if err != nil {
		return err
	}
	if id == "inbox" {
		return fmt.Errorf("%w: inbox cannot be deleted", ErrInvalidChannel)
	}
	return s.db.Tx(ctx, func(tx storage.Tx) error {
		if err := detachChannelFromRules(ctx, tx, id); err != nil {
			return err
		}
		res, err := tx.Exec(ctx, `DELETE FROM core_notification_channels WHERE id = ?`, id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return ErrUnknownChannel
		}
		if s.refs != nil {
			if err := s.refs.ReplaceTx(ctx, tx, "notifications.channel:"+id, nil); err != nil {
				return err
			}
		}
		return s.auditConfig(ctx, tx, actor, "channel", id, "delete")
	})
}

func detachChannelFromRules(ctx context.Context, tx storage.Tx, channelID string) error {
	rows, err := tx.Query(ctx, `SELECT id, channels FROM core_notification_rules`)
	if err != nil {
		return err
	}
	type update struct {
		id       string
		channels string
	}
	var updates []update
	for rows.Next() {
		var rid, raw string
		if err := rows.Scan(&rid, &raw); err != nil {
			rows.Close()
			return err
		}
		ch := decodeChannels(raw)
		next := make([]string, 0, len(ch))
		changed := false
		for _, c := range ch {
			if c == channelID {
				changed = true
				continue
			}
			next = append(next, c)
		}
		if !changed {
			continue
		}
		encoded, err := encodeChannels(next)
		if err != nil {
			rows.Close()
			return err
		}
		updates = append(updates, update{id: rid, channels: encoded})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, u := range updates {
		if _, err := tx.Exec(ctx, `UPDATE core_notification_rules SET channels = ? WHERE id = ?`, u.channels, u.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) DeliveryHealth(ctx context.Context) ([]ChannelHealth, error) {
	rows, err := s.db.Query(ctx, `SELECT c.id,
		COALESCE((
			SELECT s.state FROM core_notification_sends s
			 WHERE s.channel_id = c.id
			 ORDER BY COALESCE(s.last_attempt_at, s.next_attempt_at) DESC, s.id DESC LIMIT 1
		), 'idle'),
		COALESCE((
			SELECT s.last_error FROM core_notification_sends s
			 WHERE s.channel_id = c.id
			 ORDER BY COALESCE(s.last_attempt_at, s.next_attempt_at) DESC, s.id DESC LIMIT 1
		), ''),
		(
			SELECT s.last_attempt_at FROM core_notification_sends s
			 WHERE s.channel_id = c.id
			 ORDER BY COALESCE(s.last_attempt_at, s.next_attempt_at) DESC, s.id DESC LIMIT 1
		)
		FROM core_notification_channels c ORDER BY c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChannelHealth
	for rows.Next() {
		var h ChannelHealth
		var last *string
		if err := rows.Scan(&h.ChannelID, &h.State, &h.LastError, &last); err != nil {
			return nil, err
		}
		h.LastAttemptAt = parseTimePtr(last)
		out = append(out, h)
	}
	if out == nil {
		out = []ChannelHealth{}
	}
	return out, rows.Err()
}

func (s *Service) auditConfig(ctx context.Context, tx storage.Tx, actor, kind, id, action string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO core_notification_audit(id, actor, object_kind, object_id, action, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, newID(), actor, kind, id, action, rfc(s.now())); err != nil {
		return err
	}
	_, err := s.bus.PublishTx(ctx, tx, events.Input{
		Type:    events.TypeNotificationConfigChanged,
		Source:  events.SourceNotifications,
		Subject: kind + ":" + id,
		Payload: map[string]any{"actor": actor, "objectKind": kind, "objectId": id, "action": action},
	})
	return err
}

func (s *Service) normalizeRule(r Rule) (Rule, error) {
	if !validID(r.ID) {
		return Rule{}, fmt.Errorf("%w: id %q", ErrInvalidRule, r.ID)
	}
	if _, err := events.CompilePattern(r.Match); err != nil {
		return Rule{}, fmt.Errorf("%w: match: %v", ErrInvalidRule, err)
	}
	if _, err := compileWhere(s.env, r.Where); err != nil {
		return Rule{}, err
	}
	if err := validateTemplate(r.Title); err != nil {
		return Rule{}, err
	}
	if err := validateTemplate(r.Body); err != nil {
		return Rule{}, err
	}
	norm, err := validateURL(r.URL)
	if err != nil {
		return Rule{}, err
	}
	r.URL = norm
	if r.Throttle < 0 {
		return Rule{}, fmt.Errorf("%w: throttle", ErrInvalidRule)
	}
	if len(r.Channels) == 0 {
		return Rule{}, fmt.Errorf("%w: at least one channel", ErrInvalidRule)
	}
	seen := map[string]struct{}{}
	for _, ch := range r.Channels {
		if !validID(ch) {
			return Rule{}, fmt.Errorf("%w: channel %q", ErrInvalidRule, ch)
		}
		if _, ok := seen[ch]; ok {
			return Rule{}, fmt.Errorf("%w: duplicate channel %q", ErrInvalidRule, ch)
		}
		seen[ch] = struct{}{}
	}
	existing, err := s.ListChannels(context.Background())
	if err != nil {
		return Rule{}, err
	}
	have := map[string]struct{}{}
	for _, c := range existing {
		have[c.ID] = struct{}{}
	}
	for _, ch := range r.Channels {
		if _, ok := have[ch]; !ok {
			return Rule{}, fmt.Errorf("%w: unknown channel %q", ErrInvalidRule, ch)
		}
	}
	return r, nil
}

func (s *Service) validateChannel(ctx context.Context, c ChannelConfig) error {
	if !validID(c.ID) {
		return fmt.Errorf("%w: id %q", ErrInvalidChannel, c.ID)
	}
	switch c.Kind {
	case "inbox", "ntfy", "webpush", "cloudflare", "email":
	default:
		return fmt.Errorf("%w: kind %q", ErrInvalidChannel, c.Kind)
	}
	if c.Kind == "inbox" && c.ID != "inbox" {
		return fmt.Errorf("%w: inbox kind is reserved", ErrInvalidChannel)
	}
	if c.Kind == "ntfy" {
		if strings.TrimSpace(c.Settings["topic"]) == "" {
			return fmt.Errorf("%w: ntfy requires topic", ErrInvalidChannel)
		}
	}
	if c.Kind == "webpush" {
		if strings.TrimSpace(c.Settings["endpoint"]) == "" {
			return fmt.Errorf("%w: webpush requires endpoint", ErrInvalidChannel)
		}
		if err := validatePushEndpoint(c.Settings["endpoint"]); err != nil {
			return fmt.Errorf("%w: webpush endpoint is invalid", ErrInvalidChannel)
		}
	}
	if c.Kind == "cloudflare" {
		if strings.TrimSpace(c.Settings["endpoint"]) == "" {
			return fmt.Errorf("%w: cloudflare requires endpoint", ErrInvalidChannel)
		}
		if _, err := parseCloudflareEndpoint(c.Settings["endpoint"]); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidChannel, err)
		}
		if strings.TrimSpace(c.CredentialID) == "" {
			return fmt.Errorf("%w: cloudflare requires a credential id", ErrInvalidChannel)
		}
	}
	if c.Kind == "email" {
		if _, err := newEmailChannel(c, s.creds); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidChannel, err)
		}
	}
	for k, v := range c.Settings {
		if looksSecret(k, v) {
			return fmt.Errorf("%w: settings must not contain secrets", ErrInvalidChannel)
		}
	}
	if c.CredentialID != "" {
		if s.creds == nil {
			return fmt.Errorf("%w: credential runtime unavailable", ErrInvalidChannel)
		}
		if _, err := s.creds.Attributes(ctx, c.CredentialID); err != nil {
			return fmt.Errorf("%w: credential: %v", ErrInvalidChannel, err)
		}
	}
	return nil
}

func looksSecret(k, v string) bool {
	lk := strings.ToLower(k)
	if strings.Contains(lk, "secret") || strings.Contains(lk, "token") || strings.Contains(lk, "password") || strings.Contains(lk, "private") {
		return true
	}
	return strings.Contains(strings.ToLower(v), "secret")
}

type scanner func(dest ...any) error

func scanRule(scan scanner) (Rule, error) {
	var r Rule
	var enabled int
	var ch string
	var throttle int64
	if err := scan(&r.ID, &enabled, &r.Match, &r.Where, &ch, &r.Title, &r.Body, &r.URL, &throttle); err != nil {
		return Rule{}, err
	}
	r.Enabled = enabled != 0
	r.Channels = decodeChannels(ch)
	r.Throttle = time.Duration(throttle) * time.Second
	return r, nil
}

func (s *Service) List(ctx context.Context, q InboxQuery) (InboxPage, error) {
	if q.Limit <= 0 {
		q.Limit = 50
	}
	if q.Limit > 200 {
		q.Limit = 200
	}
	now := rfc(s.now())
	args := []any{now}
	where := `in_inbox = 1 AND available_at <= ? AND ready_published = 1`
	if q.UnreadOnly {
		where += ` AND read_at IS NULL`
	}
	if q.AfterID != "" {
		where += ` AND (created_at, id) < ((SELECT created_at FROM core_notifications WHERE id = ?), ?)`
		args = append(args, q.AfterID, q.AfterID)
	}
	args = append(args, q.Limit+1)
	rows, err := s.db.Query(ctx, `SELECT id, source_event_id, title, body, url, subject_value, collapsed_count,
		read_at, created_at, available_at, rule_id
		FROM core_notifications WHERE `+where+` ORDER BY created_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return InboxPage{}, err
	}
	defer rows.Close()
	var items []Notification
	for rows.Next() {
		n, err := scanNotif(rows.Scan)
		if err != nil {
			return InboxPage{}, err
		}
		items = append(items, n)
	}
	if err := rows.Err(); err != nil {
		return InboxPage{}, err
	}
	page := InboxPage{Notifications: items, NextAfter: ""}
	if len(items) > q.Limit {
		page.Notifications = items[:q.Limit]
		page.NextAfter = page.Notifications[len(page.Notifications)-1].ID
	}
	if page.Notifications == nil {
		page.Notifications = []Notification{}
	}
	return page, nil
}

func (s *Service) Get(ctx context.Context, id string) (Notification, error) {
	row := s.db.QueryRow(ctx, `SELECT id, source_event_id, title, body, url, subject_value, collapsed_count,
		read_at, created_at, available_at, rule_id FROM core_notifications WHERE id = ? AND in_inbox = 1`, id)
	n, err := scanNotif(row.Scan)
	if storage.IsNoRows(err) {
		return Notification{}, ErrUnknownNotif
	}
	return n, err
}

func (s *Service) MarkRead(ctx context.Context, id string, read bool) error {
	return s.db.Tx(ctx, func(tx storage.Tx) error {
		var exists int
		if err := tx.QueryRow(ctx, `SELECT 1 FROM core_notifications WHERE id = ? AND in_inbox = 1 AND ready_published = 1`, id).Scan(&exists); err != nil {
			if storage.IsNoRows(err) {
				return ErrUnknownNotif
			}
			return err
		}
		var readAt any
		if read {
			readAt = rfc(s.now())
		} else {
			readAt = nil
		}
		_, err := tx.Exec(ctx, `UPDATE core_notifications SET read_at = ? WHERE id = ?`, readAt, id)
		return err
	})
}

func scanNotif(scan scanner) (Notification, error) {
	var n Notification
	var readAt *string
	var created, avail string
	if err := scan(&n.ID, &n.SourceEventID, &n.Title, &n.Body, &n.URL, &n.Subject, &n.Collapsed, &readAt, &created, &avail, &n.RuleID); err != nil {
		return Notification{}, err
	}
	n.Read = readAt != nil
	t, _ := parseTime(created)
	n.CreatedAt = t
	t, _ = parseTime(avail)
	n.AvailableAt = t
	return n, nil
}
