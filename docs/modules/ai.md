# ai

**Layer 3** · `internal/core/ai` · imports `storage`, `events`, `policy`, `credentials` ·
used by `pluginhost`, `web`

**Responsibility.** Route logical model names to providers, and record what every call cost.

This is the only module through which a plugin can spend money, which is why the gate
lives on its hot path.

---

## Interface

```go
package ai

type AI interface {
    Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
    ChatStream(ctx context.Context, req ChatRequest) (Stream, error)
    Embed(ctx context.Context, req EmbedRequest) (*EmbedResponse, error)
}
```

Stateless by design. Conversation state belongs to whoever needs it, because "a
conversation" means something different per plugin — a single per-lot analysis for
`bidrl`, a long thread for a chat UI. A shared conversation store can be added when a
second plugin wants the same shape.

### Request

```go
type ChatRequest struct {
    Model    string      // logical name, never a provider model ID
    Messages []Message

    Schema   *jsonschema.Schema // structured output, via the provider's native mechanism
    Tools    []Tool
    Grounded bool               // provider web-search grounding

    MaxTokens   int
    Temperature *float64

    // ProviderOpts is the deliberate escape hatch. Keys are namespaced by
    // provider ("google.thinking_level"). An unknown key for the resolved
    // provider is an error, never silently dropped.
    ProviderOpts map[string]any
}

type Message struct {
    Role   Role     // system | user | assistant | tool
    Text   string
    Images []Image  // several images in one message is a first-class case
}

type Image struct {
    Blob storage.BlobRef // avoids holding image bytes in memory
    MIME string

    // Resolution is first-class rather than a ProviderOpt because it is the
    // main cost lever for vision workloads. Adapters map it to whatever the
    // provider calls it, or ignore it where the concept does not exist.
    Resolution Resolution // Low | Medium | High | Default
}
```

### Response

```go
type ChatResponse struct {
    Text      string
    Parsed    json.RawMessage // populated when Schema was set
    ToolCalls []ToolCall
    Usage     Usage
    Finish    FinishReason
}

type Usage struct {
    Provider        string
    Model           string // resolved provider model ID
    InputTokens     int
    OutputTokens    int
    ReasoningTokens int
    CachedTokens    int
    ImageCount      int
    CostUSD         float64
    Latency         time.Duration
    Attempts        int // > 1 means spillover occurred
}
```

---

## Routing

Logical names are configured centrally. Plugins ask for `cheap-vision` and never learn
which provider served it.

```yaml
# config/models.yaml — illustrative; fill in real models and prices
models:
  cheap-vision:
    primary:
      provider: google
      model: <vision-flash-model>
      credential: google-api
    fallback:
      - provider: openrouter
        model: <equivalent>
        credential: openrouter-key

  reasoning:
    primary:
      provider: codex          # subscription-backed capacity, preferred
      model: <model>
      credential: codex-oauth
    fallback:
      - provider: openai       # paid API, spillover on 429 / quota
        model: <model>
        credential: openai-key

pricing:
  # USD per 1M tokens. Hand-maintained; review whenever a model is added.
  google/<vision-flash-model>: { input: 0.00, output: 0.00, cached_input: 0.00 }
```

**Spillover** triggers on 429, quota exhaustion, and 5xx. It does not trigger on model
refusals or schema validation failures — those are answers, not outages. Each attempt is
recorded and reported in `Usage.Attempts`.

---

## Cost

Cost is computed from the local pricing table rather than from provider-reported billing,
so the number is available immediately, is attributable to a plugin and job, and can be
audited. The table is hand-maintained; adding a model without adding its prices should
fail startup rather than silently record zero.

Every call writes a row and publishes `core.ai.usage`:

```
core_ai_usage(
  id, plugin_id, job_id, event_id,
  logical_model, provider, provider_model,
  input_tokens, output_tokens, reasoning_tokens, cached_tokens, image_count,
  cost_usd, latency_ms, attempts, created_at
)
```

`job_id` is captured automatically when the call originates inside a job handler, so
"what did last night's scan cost" is one query rather than a reconstruction.

---

## The gate

```go
func (s *service) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
    pid := PluginFromContext(ctx)   // stamped by the Host facade, not by the caller
    if err := s.gate.CheckSpend(ctx, pid, s.estimate(req)); err != nil {
        return nil, err             // policy.ErrPluginDisabled | ErrBudgetExceeded
    }

    resp, usage, err := s.route(ctx, req)
    if err != nil {
        return nil, err
    }

    s.gate.Spend(ctx, pid, usage.CostUSD)
    s.record(ctx, pid, usage)       // row + core.ai.usage
    return resp, nil
}
```

`PluginFromContext` reads a value the facade set. A plugin cannot set it, because it never
holds an unscoped `AI`.

This check is the last line of defence: a goroutine that outlived job cancellation still
cannot reach a provider.

---

## Events emitted

| Event | When |
|---|---|
| `core.ai.usage` | after every completed call, successful or not |

---

## Notes

- **Two known abstraction leaks**, both handled deliberately: per-image resolution is
  promoted to a first-class field, and everything else provider-specific goes through
  `ProviderOpts`. `Grounded bool` is the third candidate and is an
  [open question](../../DESIGN.md#9-open-questions).
- The request shape deliberately resembles the OpenAI schema. Nothing external calls this
  today, but shaping it this way makes exposing a gateway later additive rather than a
  rewrite.
