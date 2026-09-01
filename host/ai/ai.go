// Package ai is the plugin-facing host-managed model surface.
// Plugins ask for a logical model name; they never select a provider or hold a token.
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/hbaldwin98/control-center/host/policy"
)

// AI is the only host-managed paid provider path.
type AI interface {
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
	ChatStream(ctx context.Context, req ChatRequest) (Stream, error)
	Embed(ctx context.Context, req EmbedRequest) (*EmbedResponse, error)
}

// ChatRequest is one logical-model invocation.
type ChatRequest struct {
	Model     string
	Schema    json.RawMessage
	Messages  []Message
	MaxTokens int
	Grounding *GroundingOptions
}

// Message is one turn. Related images belong on the same message.
type Message struct {
	Role   string
	Text   string
	Images []Image
}

// Image is an inline picture the model should see.
type Image struct {
	Blob       []byte
	MIME       string
	Resolution Resolution
}

// Resolution is the main cost lever for images.
type Resolution string

const (
	ResolutionLow    Resolution = "low"
	ResolutionMedium Resolution = "medium"
	ResolutionHigh   Resolution = "high"
)

const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleSystem    = "system"
)

// GroundingOptions ask the host to attach inspectable web evidence.
type GroundingOptions struct {
	MaxQueries     int
	Freshness      time.Duration
	AllowedDomains []string
}

// ChatResponse is the model output plus usage.
type ChatResponse struct {
	Text      string
	Parsed    json.RawMessage
	ToolCalls []ToolCall
	Citations []Citation
	Sources   []Source
	Usage     Usage
	Finish    string
}

// ToolCall is a structured tool invocation the model requested.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// Citation points at a byte range in Text that a Source supports.
type Citation struct {
	Start, End int
	Source     int
}

// Source is one grounding document.
type Source struct {
	URL         string
	Title       string
	PublishedAt *time.Time
}

// Stream is an incremental chat response.
type Stream interface {
	Recv() (Chunk, error)
	Close() error
}

// Chunk is one streamed fragment.
type Chunk struct {
	Text   string
	Finish string
	Usage  *Usage
}

// EmbedRequest is one embedding invocation.
type EmbedRequest struct {
	Model  string
	Inputs []string
}

// EmbedResponse is vectors plus usage.
type EmbedResponse struct {
	Vectors [][]float64
	Usage   Usage
}

// Usage is the accounted cost of one call.
type Usage struct {
	InputTokens  int64
	OutputTokens int64
	CostMicroUSD policy.MicroUSD
	Latency      time.Duration
	Attempts     int
}

// Errors returned across the AI boundary.
var (
	ErrUnknownRoute = errors.New("ai: unknown logical model")
	ErrCapability   = errors.New("ai: capability not enabled on this route")
)
