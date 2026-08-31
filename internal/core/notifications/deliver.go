package notifications

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

func (s *Service) releaseWindows(ctx context.Context) {
	for ctx.Err() == nil {
		released, err := s.releaseOneWindow(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Error("notifications: release window", "err", err)
			return
		}
		if !released {
			return
		}
	}
}

func (s *Service) releaseOneWindow(ctx context.Context) (bool, error) {
	now := rfc(s.now())
	var released bool
	err := s.db.Tx(ctx, func(tx storage.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `SELECT id FROM core_notifications
			WHERE ready_published = 0 AND available_at <= ?
			ORDER BY available_at ASC, id ASC LIMIT 1`, now).Scan(&id)
		if storage.IsNoRows(err) {
			return errSkip
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE core_notifications SET ready_published = 1 WHERE id = ?`, id); err != nil {
			return err
		}
		if _, err := s.bus.PublishTx(ctx, tx, events.Input{
			Type:    events.TypeNotificationReady,
			Source:  events.SourceNotifications,
			Subject: id,
			Payload: map[string]any{"notificationId": id, "state": "ready"},
		}); err != nil {
			return err
		}
		released = true
		return nil
	})
	if errors.Is(err, errSkip) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if released {
		s.signal()
	}
	return released, nil
}

var errSkip = errors.New("skip")

func (s *Service) claimUntilEmpty(ctx context.Context) {
	for ctx.Err() == nil {
		claimed, err := s.claimOne(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Error("notifications: claim send", "err", err)
			return
		}
		if !claimed {
			return
		}
	}
}

type sendRow struct {
	id, notifID, channelID, idempotency, state string
	attempts                                   int
}

func (s *Service) claimOne(ctx context.Context) (bool, error) {
	now := s.now()
	nowStr := rfc(now)
	leaseUntil := rfc(now.Add(s.opts.LeaseTTL))
	var row sendRow
	err := s.db.Tx(ctx, func(tx storage.Tx) error {
		err := tx.QueryRow(ctx, `SELECT s.id, s.notification_id, s.channel_id, s.idempotency_key, s.state, s.attempts
			FROM core_notification_sends s
			JOIN core_notifications n ON n.id = s.notification_id
			WHERE n.ready_published = 1 AND n.available_at <= ?
			  AND (
			        (s.state IN ('pending', 'retry_wait') AND s.next_attempt_at <= ?)
			     OR (s.state = 'sending' AND s.lease_until IS NOT NULL AND s.lease_until <= ?)
			      )
			ORDER BY s.next_attempt_at ASC, s.id ASC LIMIT 1`, nowStr, nowStr, nowStr).
			Scan(&row.id, &row.notifID, &row.channelID, &row.idempotency, &row.state, &row.attempts)
		if storage.IsNoRows(err) {
			return errSkip
		}
		if err != nil {
			return err
		}
		res, err := tx.Exec(ctx, `UPDATE core_notification_sends
			SET state = 'sending', lease_until = ?, last_attempt_at = ?
			WHERE id = ? AND (lease_until IS NULL OR lease_until <= ? OR state != 'sending')`,
			leaseUntil, nowStr, row.id, nowStr)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return errSkip
		}
		return nil
	})
	if errors.Is(err, errSkip) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	n, err := s.loadNotification(ctx, row.notifID)
	if err != nil {
		_ = s.failSend(ctx, row, err, false)
		return true, nil
	}
	ch, cfg, err := s.channelFor(ctx, row.channelID)
	if err != nil {
		_ = s.failSend(ctx, row, err, isPermanent(err) || isUnknownChannel(err))
		return true, nil
	}
	sendErr := ch.Send(ctx, Delivery{Notification: n, IdempotencyKey: row.idempotency})
	if sendErr != nil {
		perm := isPermanent(sendErr) || isHTTPClientError(sendErr)
		_ = s.failSend(ctx, row, sendErr, perm)
		_ = s.publishDelivery(ctx, row, cfg, "failed")
		return true, nil
	}
	if err := s.completeSend(ctx, row); err != nil {
		return false, err
	}
	_ = s.publishDelivery(ctx, row, cfg, "sent")
	return true, nil
}

func (s *Service) loadNotification(ctx context.Context, id string) (Notification, error) {
	row := s.db.QueryRow(ctx, `SELECT id, source_event_id, title, body, url, subject_value, collapsed_count,
		read_at, created_at, available_at, rule_id FROM core_notifications WHERE id = ?`, id)
	return scanNotif(row.Scan)
}

func (s *Service) completeSend(ctx context.Context, row sendRow) error {
	now := rfc(s.now())
	return s.db.Tx(ctx, func(tx storage.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE core_notification_sends
			SET state = 'sent', sent_at = ?, lease_until = NULL, last_error = '', attempts = attempts + 1, last_attempt_at = ?
			WHERE id = ?`, now, now, row.id)
		return err
	})
}

func (s *Service) failSend(ctx context.Context, row sendRow, sendErr error, permanent bool) error {
	now := s.now()
	nextAttempt := row.attempts + 1
	state := "retry_wait"
	next := now.Add(backoff(nextAttempt))
	if permanent || nextAttempt >= maxAttempts {
		state = "failed"
		next = now
	}
	msg := sendErr.Error()
	if len(msg) > 1000 {
		msg = msg[:1000]
	}
	return s.db.Tx(ctx, func(tx storage.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE core_notification_sends
			SET state = ?, attempts = ?, next_attempt_at = ?, lease_until = NULL, last_error = ?, last_attempt_at = ?
			WHERE id = ?`, state, nextAttempt, rfc(next), msg, rfc(now), row.id)
		return err
	})
}

func (s *Service) publishDelivery(ctx context.Context, row sendRow, cfg ChannelConfig, state string) error {
	return s.db.Tx(ctx, func(tx storage.Tx) error {
		_, err := s.bus.PublishTx(ctx, tx, events.Input{
			Type:    events.TypeNotificationDeliveryChanged,
			Source:  events.SourceNotifications,
			Subject: row.notifID,
			Payload: map[string]any{
				"notificationId": row.notifID,
				"channel":        row.channelID,
				"kind":           cfg.Kind,
				"state":          state,
			},
		})
		return err
	})
}

func (s *Service) channelFor(ctx context.Context, id string) (Channel, ChannelConfig, error) {
	var c ChannelConfig
	var cfg string
	var enabled int
	err := s.db.QueryRow(ctx, `SELECT id, kind, config, credential_id, enabled FROM core_notification_channels WHERE id = ?`,
		id).Scan(&c.ID, &c.Kind, &cfg, &c.CredentialID, &enabled)
	if storage.IsNoRows(err) {
		return nil, c, ErrUnknownChannel
	}
	if err != nil {
		return nil, c, err
	}
	c.Enabled = enabled != 0
	c.Settings = decodeSettings(cfg)
	if !c.Enabled {
		return nil, c, Permanent(fmtDisabled(id))
	}
	ch, err := s.buildChannel(c)
	return ch, c, err
}

func fmtDisabled(id string) error {
	return errors.New("notifications: channel " + id + " is disabled")
}

func isUnknownChannel(err error) bool {
	return errors.Is(err, ErrUnknownChannel)
}

func isHTTPClientError(err error) bool {
	return strings.Contains(err.Error(), "permanent:")
}
