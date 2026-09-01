// Package notifications turns committed events into inbox records and external deliveries
// through user-configured rules and channels.
//
// Layer 3. It imports storage, events, and credentials. No module calls Send; plugins
// publish domain events and the web module only administers rules and channels.
package notifications

import (
	"context"
	"errors"
	"time"
)

type Rule struct {
	ID       string        `json:"id"`
	Enabled  bool          `json:"enabled"`
	Match    string        `json:"match"`
	Where    string        `json:"where"`
	Channels []string      `json:"channels"`
	Title    string        `json:"title"`
	Body     string        `json:"body"`
	URL      string        `json:"url"`
	Throttle time.Duration `json:"throttleSeconds"`
}

// Admin is injected only into the authenticated web API. Channel config may contain a
// credential ID but never secret material.
type Admin interface {
	ListRules(ctx context.Context) ([]Rule, error)
	PutRule(ctx context.Context, rule Rule) error
	DeleteRule(ctx context.Context, id string) error
	ListChannels(ctx context.Context) ([]ChannelConfig, error)
	PutChannel(ctx context.Context, channel ChannelConfig) error
	DeleteChannel(ctx context.Context, id string) error
	DeliveryHealth(ctx context.Context) ([]ChannelHealth, error)
}

type Inbox interface {
	List(ctx context.Context, q InboxQuery) (InboxPage, error)
	Get(ctx context.Context, id string) (Notification, error)
	MarkRead(ctx context.Context, id string, read bool) error
}

type InboxQuery struct {
	UnreadOnly bool
	AfterID    string
	Limit      int // 1..200
}

type InboxPage struct {
	Notifications []Notification `json:"notifications"`
	NextAfter     string         `json:"nextAfter"`
}

type ChannelConfig struct {
	ID           string            `json:"id"`
	Kind         string            `json:"kind"`
	CredentialID string            `json:"credentialId"`
	Enabled      bool              `json:"enabled"`
	Settings     map[string]string `json:"settings"`
}

type ChannelHealth struct {
	ChannelID     string     `json:"channelId"`
	State         string     `json:"state"`
	LastError     string     `json:"lastError"`
	LastAttemptAt *time.Time `json:"lastAttemptAt"`
}

type Channel interface {
	ID() string
	Send(ctx context.Context, d Delivery) error
}

type Delivery struct {
	Notification   Notification
	IdempotencyKey string
}

type Notification struct {
	ID            string    `json:"id"`
	SourceEventID int64     `json:"sourceEventId"`
	Title         string    `json:"title"`
	Body          string    `json:"body"`
	URL           string    `json:"url"`
	Subject       string    `json:"subject"`
	Collapsed     int       `json:"collapsed"`
	Read          bool      `json:"read"`
	CreatedAt     time.Time `json:"createdAt"`
	AvailableAt   time.Time `json:"availableAt"`
	RuleID        string    `json:"ruleId"`
}

var (
	ErrUnknownRule     = errors.New("notifications: unknown rule")
	ErrUnknownChannel  = errors.New("notifications: unknown channel")
	ErrUnknownNotif    = errors.New("notifications: unknown notification")
	ErrInvalidRule     = errors.New("notifications: invalid rule")
	ErrInvalidChannel  = errors.New("notifications: invalid channel")
	ErrNoActor         = errors.New("notifications: authenticated actor required")
	ErrChannelInUse    = errors.New("notifications: channel is referenced by a rule")
	ErrDuplicateID     = errors.New("notifications: id already exists")
	ErrInvalidURL      = errors.New("notifications: url must be an application-relative path")
	ErrInvalidTemplate = errors.New("notifications: invalid template")
	ErrInvalidWhere    = errors.New("notifications: invalid where expression")
)

type actorKey struct{}

// WithActor stamps the authenticated principal. Admin mutations reject a context without one.
func WithActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

const ActorSystem = "system"

func actor(ctx context.Context) (string, error) {
	s, _ := ctx.Value(actorKey{}).(string)
	if s == "" {
		return "", ErrNoActor
	}
	return s, nil
}

// Permanent records a send failure that must not be retried.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err: err}
}

type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

func isPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}
