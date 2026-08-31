// Package events is the plugin-facing event DTO and handler types.
package events

import (
	"context"
	"encoding/json"
	"time"

	"github.com/hbaldwin98/control-center/host/storage"
)

// Event is one committed row of the log as a plugin handler sees it.
type Event struct {
	ID        int64
	Type      string
	Source    string
	Subject   string
	Payload   json.RawMessage
	CreatedAt time.Time
}

// Handler receives one event. A live subscription is lossy and bounded; a durable
// Handler's error triggers the host retry policy.
type Handler func(ctx context.Context, e Event) error

// TxHandler runs inside the cursor transaction. Returning an error rolls back both
// the handler's writes and the acknowledgement.
type TxHandler func(ctx context.Context, tx storage.Tx, e Event) error
