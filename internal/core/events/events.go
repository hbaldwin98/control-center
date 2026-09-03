// Package events persists every event and dispatches committed events to live and durable
// subscribers.
//
// Layer 1. It imports storage and nothing else. This is the spine: modules do not call
// each other sideways, they publish here.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/storage"
)

// MaxPayloadBytes bounds a serialized payload.
const MaxPayloadBytes = 256 << 10

// Event is one committed row of the log.
type Event struct {
	ID        int64           `json:"id"`   // monotonic within the persisted log
	Type      string          `json:"type"` // "bidrl.deal_found", "core.job.failed"
	Source    string          `json:"source"`
	Subject   string          `json:"subject"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"createdAt"`
}

// Input is an event before insertion.
type Input struct {
	Type    string
	Source  string
	Subject string
	Payload any
}

// Query reads the persisted log.
type Query struct {
	Pattern string
	AfterID int64
	Limit   int // 1..1000
}

// Bus is the publishing and subscription surface every module above layer 1 uses.
type Bus interface {
	// Publish commits one event in its own transaction.
	Publish(ctx context.Context, in Input) (int64, error)

	// PublishTx inserts into tx. A rollback removes the event. Dispatchers tail the
	// committed log, so they cannot observe it before the publisher commits.
	PublishTx(ctx context.Context, tx storage.Tx, in Input) (int64, error)

	SubscribeLive(pattern string, h Handler) (Subscription, error)
	SubscribeDurable(cfg DurableConfig, h Handler) (Subscription, error)
	SubscribeDurableTx(cfg DurableConfig, h TxHandler) (Subscription, error)
	Query(ctx context.Context, q Query) ([]Event, error)
}

// Handler receives one event. A durable handler's error triggers the retry policy.
type Handler func(ctx context.Context, e Event) error

// TxHandler runs inside the cursor transaction. The dispatcher advances the cursor in the
// same storage transaction, so returning an error rolls back both the handler's writes and
// the acknowledgement.
type TxHandler func(ctx context.Context, tx storage.Tx, e Event) error

// Subscription is the handle returned by every Subscribe call.
type Subscription interface {
	// Name is the durable subscriber name, or a generated name for a live subscription.
	Name() string
	// Close stops delivery. For a durable subscription it releases the dispatch lease;
	// the persisted cursor survives.
	Close()
}

// DurableConfig configures a durable subscription.
type DurableConfig struct {
	Name    string // globally unique stable subscriber name
	Pattern string
	Start   CursorStart // used only when no persisted cursor exists
	Retry   RetryPolicy
}

// CursorStart is applied only when creating a cursor for a new durable name.
type CursorStart struct {
	Mode    StartMode
	EventID int64 // required only for AfterEvent
}

// StartMode selects where a brand-new cursor begins.
type StartMode string

const (
	FromNow       StartMode = "now"
	FromBeginning StartMode = "beginning"
	AfterEvent    StartMode = "after_event"
)

// RetryPolicy governs durable handler failures. MaxAttempts must be positive and the
// backoffs must be finite and positive.
type RetryPolicy struct {
	MaxAttempts int
	Initial     time.Duration
	Maximum     time.Duration
}

// DefaultRetry is 10 attempts with exponential delays from one second, capped at five
// minutes. After the last failure the subscription pauses on the poison event.
var DefaultRetry = RetryPolicy{MaxAttempts: 10, Initial: time.Second, Maximum: 5 * time.Minute}

func (p RetryPolicy) validate() error {
	if p.MaxAttempts <= 0 {
		return errors.New("events: retry MaxAttempts must be positive")
	}
	if p.Initial <= 0 || p.Maximum <= 0 {
		return errors.New("events: retry backoffs must be finite and positive")
	}
	if p.Maximum < p.Initial {
		return errors.New("events: retry Maximum must not be below Initial")
	}
	return nil
}

// delay returns the backoff before the given attempt number, counting from one.
func (p RetryPolicy) delay(attempt int) time.Duration {
	d := p.Initial
	for range attempt - 1 {
		d *= 2
		if d >= p.Maximum {
			return p.Maximum
		}
	}
	return d
}

// CursorState is the persisted lifecycle of a durable subscriber.
type CursorState string

const (
	// CursorActive is dispatching normally, possibly waiting on a retry backoff.
	CursorActive CursorState = "active"
	// CursorPaused stopped on a poison event. Later events do not pass it silently; an
	// operator must retry, skip, or reset it.
	CursorPaused CursorState = "paused"
)

// SubscriberStatus is the operational view of one durable subscriber.
type SubscriberStatus struct {
	Name          string      `json:"name"`
	Pattern       string      `json:"pattern"`
	LastEventID   int64       `json:"lastEventId"`
	State         CursorState `json:"state"`
	FailedEventID int64       `json:"failedEventId"`
	Attempts      int         `json:"attempts"`
	RetryAt       *time.Time  `json:"retryAt"`
	LastError     string      `json:"lastError"`
	Healthy       bool        `json:"healthy"`
}

// Errors returned across the events boundary.
var (
	ErrInvalidType    = errors.New("events: invalid event type")
	ErrInvalidSource  = errors.New("events: invalid event source")
	ErrInvalidPattern = errors.New("events: invalid pattern")
	ErrPayloadTooBig  = errors.New("events: payload exceeds 256 KiB")
	ErrPatternChanged = errors.New("events: durable name already exists with a different pattern")
	ErrUnknownName    = errors.New("events: unknown durable subscriber")
	ErrNotPaused      = errors.New("events: subscriber is not paused")
)

// Core event type names. Publishers use these constants so a rename is a compile error
// rather than a silently dead notification rule.
const (
	TypeJobEnqueued   = "core.job.enqueued"
	TypeJobStarted    = "core.job.started"
	TypeJobProgressed = "core.job.progressed"
	TypeJobLogged     = "core.job.logged"
	TypeJobSucceeded  = "core.job.succeeded"
	TypeJobFailed     = "core.job.failed"
	TypeJobCancelled  = "core.job.cancelled"
	TypeJobDead       = "core.job.dead"

	TypeAIUsage = "core.ai.usage"

	TypeBrowserDenied = "core.browser.denied"

	TypeHarnessCreated     = "core.harness.created"
	TypeHarnessStarted     = "core.harness.started"
	TypeHarnessOutput      = "core.harness.output"
	TypeHarnessStopping    = "core.harness.stopping"
	TypeHarnessExited      = "core.harness.exited"
	TypeHarnessFailed      = "core.harness.failed"
	TypeHarnessStopped     = "core.harness.stopped"
	TypeHarnessInterrupted = "core.harness.interrupted"

	TypePluginEnabled                   = "core.plugin.enabled"
	TypePluginDisabled                  = "core.plugin.disabled"
	TypePluginBudgetExceeded            = "core.plugin.budget_exceeded"
	TypePluginAccountingInvariantFailed = "core.plugin.accounting_invariant_failed"

	TypeCredentialNeedsReauth = "core.credential.needs_reauth"

	TypeSubscriptionPaused = "core.event.subscription_paused"

	TypeNotificationReady           = "core.notification.ready"
	TypeNotificationDeliveryChanged = "core.notification.delivery_changed"
	TypeNotificationConfigChanged   = "core.notification.config_changed"
)

// Core module sources. A core publisher must use its assigned source.
const (
	SourceEvents        = "core.events"
	SourceJobs          = "core.jobs"
	SourceAI            = "core.ai"
	SourceBrowser       = "core.browser"
	SourceHarness       = "core.harness"
	SourcePolicy        = "core.policy"
	SourceCredentials   = "core.credentials"
	SourceNotifications = "core.notifications"
	SourcePluginHost    = "core.pluginhost"
)
