// Package ai routes logical model names to providers, reserves their maximum cost, and
// durably accounts for every provider attempt.
//
// Layer 3. It imports storage, events, policy, and credentials. Plugins never name a
// provider or see a credential.
package ai

import (
	"context"
	"errors"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/policy"
)

type AI interface {
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
	ChatStream(ctx context.Context, req ChatRequest) (Stream, error)
	Embed(ctx context.Context, req EmbedRequest) (*EmbedResponse, error)
}

type Query interface {
	Routes(ctx context.Context) ([]RouteDescriptor, error)
	Calls(ctx context.Context, q CallQuery) (CallPage, error)
}

// RouteDescriptor is a route as an administrator sees and edits it. It carries the whole
// stored intent, not just what compiled, so a route the editor loads is the route that
// was saved even when it currently refuses to dispatch.
type RouteDescriptor struct {
	LogicalName     string              `json:"logicalName"`
	Capabilities    []string            `json:"capabilities"`
	MaxInputTokens  int                 `json:"maxInputTokens"`
	MaxOutputTokens int                 `json:"maxOutputTokens"`
	AttemptPlan     []AttemptDescriptor `json:"attemptPlan"`
	Healthy         bool                `json:"healthy"`
	LastError       string              `json:"lastError"`
}

// AttemptDescriptor is one step of the plan. The prices are the ones recorded when the
// route was saved, which are what a call is admitted against — not whatever the
// provider's catalog says today.
type AttemptDescriptor struct {
	Provider                 string          `json:"provider"`
	Model                    string          `json:"model"`
	Billing                  Billing         `json:"billing"`
	InputMicroUSDPerMillion  policy.MicroUSD `json:"inputMicroUsdPerMillion"`
	OutputMicroUSDPerMillion policy.MicroUSD `json:"outputMicroUsdPerMillion"`
}

type CallQuery struct {
	PluginID, JobID, LogicalModel, Status string
	AfterID                               string
	Limit                                 int
}

type CallPage struct {
	Calls     []CallRecord `json:"calls"`
	NextAfter string       `json:"nextAfter"`
}

type CallRecord struct {
	ID               string          `json:"id"`
	PluginID         string          `json:"pluginId"`
	JobID            string          `json:"jobId"`
	Operation        string          `json:"operation"`
	LogicalModel     string          `json:"logicalModel"`
	Status           string          `json:"status"`
	ErrorClass       string          `json:"errorClass"`
	ReservedMicroUSD policy.MicroUSD `json:"reservedMicroUsd"`
	SettledMicroUSD  policy.MicroUSD `json:"settledMicroUsd"`
	StartedAt        time.Time       `json:"startedAt"`
	FinalizedAt      *time.Time      `json:"finalizedAt"`
}

type Stream interface {
	Recv() (Chunk, error)
	Close() error
}

type Chunk struct {
	Text   string
	Finish string
	Usage  *Usage
}

type ChatRequest struct {
	Model     string
	Messages  []Message
	MaxTokens int
}

type EmbedRequest struct {
	Model  string
	Inputs []string
}

type EmbedResponse struct {
	Vectors [][]float64
	Usage   Usage
}

type Message struct {
	Role string
	Text string
}

type ChatResponse struct {
	Text   string
	Usage  Usage
	Finish string
}

type Usage struct {
	InputTokens  int64
	OutputTokens int64
	CostMicroUSD policy.MicroUSD
	Latency      time.Duration
	Attempts     int
}

var (
	ErrUnknownRoute      = errors.New("ai: unknown logical model")
	ErrCapability        = errors.New("ai: capability not enabled on this route")
	ErrNoPlugin          = errors.New("ai: plugin identity missing")
	ErrUnbounded         = errors.New("ai: request cost cannot be bounded")
	ErrMissingPrice      = errors.New("ai: attempt is missing pricing")
	ErrMissingCredential = errors.New("ai: attempt is missing credentials")
)

type pluginKey struct{}
type jobKey struct{}

// WithPlugin stamps the calling plugin. Chat and Embed reject a context without one.
func WithPlugin(ctx context.Context, pluginID string) context.Context {
	return context.WithValue(ctx, pluginKey{}, pluginID)
}

func WithJob(ctx context.Context, jobID string) context.Context {
	return context.WithValue(ctx, jobKey{}, jobID)
}

func pluginID(ctx context.Context) (string, error) {
	id, _ := ctx.Value(pluginKey{}).(string)
	if id == "" {
		return "", ErrNoPlugin
	}
	return id, nil
}

func jobID(ctx context.Context) string {
	id, _ := ctx.Value(jobKey{}).(string)
	return id
}
