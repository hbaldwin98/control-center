# ai

**Layer 3** · `internal/core/ai` · imports `storage`, `events`, `policy`, `credentials` ·
used by `pluginhost`, `web`

**Responsibility.** Route logical model names to providers, reserve their maximum cost,
and durably account for every provider attempt.

This is the only host-managed paid provider path. Plugins may still create their own
network clients and incur external cost outside host accounting; the host kill switch is
capability revocation, not process or network isolation.

---

## Interface

```go
package ai

type AI interface {
    Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
    ChatStream(ctx context.Context, req ChatRequest) (Stream, error)
    Embed(ctx context.Context, req EmbedRequest) (*EmbedResponse, error)
}

// Query is injected only into the authenticated web API. It returns no credentials.
type Query interface {
    Routes(ctx context.Context) ([]RouteDescriptor, error)
    Calls(ctx context.Context, q CallQuery) (CallPage, error)
}

type RouteDescriptor struct {
    LogicalName  string
    Capabilities []string
    AttemptPlan  []AttemptDescriptor // provider/model names are visible to the administrator
    Healthy      bool
    LastError    string
}

type AttemptDescriptor struct {
    Provider string
    Model    string
}

type CallQuery struct {
    PluginID, JobID, LogicalModel, Status string
    AfterID string
    Limit   int // 1..200
}

type CallPage struct {
    Calls     []CallRecord
    NextAfter string
}

type CallRecord struct {
    ID, PluginID, JobID, Operation, LogicalModel, Status, ErrorClass string
    ReservedMicroUSD, SettledMicroUSD policy.MicroUSD
    StartedAt time.Time
    FinalizedAt *time.Time
}

type Stream interface {
    Recv() (Chunk, error)
    Close() error
}
```

Conversation state belongs to the caller. Every operation is attributed to the plugin ID
stamped by its scoped host facade and, when present, the current job and source event.

### Chat

```go
type ChatRequest struct {
    Model    string // logical name, never a provider model ID
    Messages []Message

    Schema    *jsonschema.Schema
    Tools     []Tool
    Grounding *GroundingOptions

    MaxTokens   int
    Temperature *float64

    // Options belong to the logical route contract, not a provider. Unknown options
    // and options whose maximum cost cannot be calculated are rejected before reservation.
    Options map[string]any
}

type GroundingOptions struct {
    MaxQueries     int
    Freshness      time.Duration // zero means no freshness constraint
    AllowedDomains []string      // empty means unrestricted
}

type Message struct {
    Role   Role
    Text   string
    Images []Image
}

type Image struct {
    Blob       storage.BlobRef
    MIME       string
    Resolution Resolution // Low | Medium | High | Default
}
```

`GroundingOptions.MaxQueries` must be positive and within the logical route's configured
limit. Adapters must enforce the query limit and domain/freshness constraints rather than
treat them as hints.

```go
type ChatResponse struct {
    Text      string
    Parsed    json.RawMessage
    ToolCalls []ToolCall
    Citations []Citation
    Sources   []Source
    Usage     Usage
    Finish    FinishReason
}

type Citation struct {
    Start, End int // byte offsets in Text
    Source     int // index into Sources
}

type Source struct {
    URL         string
    Title       string
    PublishedAt *time.Time
}

type Usage struct {
    InputTokens     int64
    OutputTokens    int64
    ReasoningTokens int64
    CachedTokens    int64
    ImageCount      int64
    SearchQueries   int64
    CostMicroUSD    policy.MicroUSD
    Latency         time.Duration
    Attempts        int
}
```

Provider identity and provider model names remain in the administrator-only attempt
audit; plugin-facing responses and events expose only the requested logical model.

Streaming chunks carry text/tool deltas; the terminal chunk carries finish reason,
citations, sources, and aggregate usage.

### Embeddings

`EmbedRequest` names a logical embedding route and contains a finite list of bounded
inputs. `EmbedResponse` returns vectors plus aggregate `Usage`. Empty or oversized input,
or a route without embedding capability, fails before reservation or dispatch.

---

## Routing

Each logical route has a finite ordered attempt plan: primary, bounded retries, and
fallbacks. It also declares its supported capabilities and hard request limits. Startup
fails unless every configured attempt has credentials and pricing and supports every
capability enabled on that route: chat, streaming, embeddings, tools, structured output,
vision, and grounding/search as applicable. A request for a capability outside the route
contract is rejected before reservation.

Spillover is limited to configured error classes such as rate limit, quota exhaustion,
and provider 5xx. Refusals and schema validation failures do not spill over. The finite
attempt plan is part of the reservation calculation.

---

## Reservation and settlement

All money uses `policy.MicroUSD`, backed by `int64`; negative values are invalid.
Floating-point dollars are display values only, and estimates round up.

Before the first provider dispatch, AI computes a conservative maximum for every attempt
that the route may make. The estimate includes maximum input, output, reasoning and cached
tokens; image count and resolution; grounding/search queries; embeddings; and every
priced provider option. Missing pricing, an unbounded dimension, or arithmetic overflow
rejects the request. No unbounded paid request may dispatch.

An adapter reporting cost above that bound is an invariant breach, not a reason to hide
the charge. Finalization records the actual cost, policy disables the plugin, and the
logical route reports unhealthy until its pricing or limits are corrected.

AI generates the call ID, then uses one storage transaction to insert the pending
`core_ai_calls` row and call `policy.Gate.ReserveSpendTx`. Policy compares limits against
committed spend plus all open reservations, so concurrent calls cannot each pass against
the same remaining budget. If policy returns a domain rejection that generated an event,
AI commits that event without the call row and returns the domain error after commit. The
successful transaction is the call's admission point; disable wins against later
admissions but may race with an admitted call.

Before each primary, retry, or fallback transport invocation, AI checks context
cancellation. Disable prevents new call admissions and cancels admitted contexts, so no
later attempt starts once that cancellation is observed. It cannot stop an attempt whose
transport was already invoked.

Finalization runs exactly once in one storage transaction. It:

1. persists the final call and every provider attempt, including failed attempts
2. calls `policy.Gate.SettleSpendTx` to move the actual charge from reserved to committed
   spend, or `ReleaseSpendTx` when every attempt is definitely unbilled
3. inserts `core.ai.usage` with `events.PublishTx`

The event dispatcher can see only the committed event row. A definitely unbilled attempt
releases its reserved amount. If provider billing is ambiguous because of timeout,
cancellation, transport loss, or crash recovery, settlement permanently records the
conservative amount. V1 does not revise settled accounting from later provider reports.
Disabling a plugin cancels admitted call contexts but does not void their reservations:
an already-admitted paid request may finish and is always finalized or conservatively
settled.

---

## Call records

```
core_ai_calls(
  id, reservation_id, plugin_id, job_id, source_event_id, usage_event_id,
  operation, logical_model, status, error_class,
  reserved_micro_usd, settled_micro_usd,
  started_at, finalized_at
)

core_ai_attempts(
  id, call_id, ordinal, provider, provider_model,
  status, provider_status, error_class, billing_state,
  input_tokens, output_tokens, reasoning_tokens, cached_tokens,
  image_count, search_queries, cost_micro_usd, latency_ms
)
```

An attempt begins when an adapter invokes provider transport. Every such attempt gets a
row, whether it succeeds, fails, is cancelled, or has ambiguous billing. Status and error
class are normalized, while `provider_status` retains the provider-specific code. Call
usage and cost aggregate all attempts, including failed attempts that were billed.

Before invoking transport, AI durably inserts the attempt's ordinal, provider, model, and
`dispatching` status. Finalization updates that row with outcome, usage, and cost in the
shared transaction. A crash after dispatch intent commits but before an outcome is known
leaves an explicit ambiguous attempt, which recovery settles conservatively. This may
overcount a request that never reached the provider, but cannot omit one that did.

Pending calls and reservations are recovered after restart. If the provider's billing
cannot prove that an interrupted attempt was unbilled, recovery finalizes it at the
conservative reserved amount and records the ambiguous billing state for audit.

---

## Ownership and cancellation

`Chat` and `Embed` return only after response ownership is closed and final accounting is
committed. Context cancellation asks the adapter to stop; it does not assert that the
provider did no work.

For `ChatStream`, the service owns the provider stream and its accounting state. The
caller owns the returned `Stream` and must call `Close`. `Recv` returns `io.EOF` only
after final accounting commits. `Close` cancels provider I/O, closes the response body,
waits for the single finalizer, and returns any finalization error. Request-context
cancellation follows the same path. If usage or billing remains unknown, finalization
uses the conservative settlement rule rather than releasing the reservation.

---

## Events emitted

| Event | When |
|---|---|
| `core.ai.usage` | once for every finalized call, successful or not; payload includes plugin, job, operation, logical model, status, usage totals, attempts, and cost in micro-USD |
