package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hbaldwin98/control-center/internal/core/credentials"
	"github.com/hbaldwin98/control-center/internal/core/policy"
)

func TestUSDPerTokenConversionRoundsUp(t *testing.T) {
	cases := []struct {
		in    string
		want  policy.MicroUSD
		valid bool
	}{
		// $0.000003/token is $3 per million, which is 3_000_000 micro-USD.
		{"0.000003", 3_000_000, true},
		{"0.0000006", 600_000, true},
		// Anything with a remainder rounds up: a reservation may overestimate, never
		// underestimate.
		{"0.0000000000015", 2, true},
		// A published zero is "unknown", not "free".
		{"0", 0, false},
		{"", 0, false},
		{"free", 0, false},
		{"-1", 0, false},
	}
	for _, c := range cases {
		got, ok := usdPerTokenToMicroPerMillion(c.in)
		if ok != c.valid {
			t.Fatalf("%q: valid = %v, want %v", c.in, ok, c.valid)
		}
		if ok && got != c.want {
			t.Fatalf("%q: got %d, want %d", c.in, got, c.want)
		}
	}
}

func TestProviderValidation(t *testing.T) {
	cases := []struct {
		name string
		in   ProviderConfig
		ok   bool
	}{
		{"https base url", ProviderConfig{ID: "openai", Kind: KindOpenAICompatible,
			BaseURL: "https://api.openai.com/v1", CredentialID: "k"}, true},
		{"loopback http allowed", ProviderConfig{ID: "local", Kind: KindOpenAICompatible,
			BaseURL: "http://localhost:8080/v1", CredentialID: "k"}, true},
		{"remote http refused", ProviderConfig{ID: "bad", Kind: KindOpenAICompatible,
			BaseURL: "http://example.com/v1", CredentialID: "k"}, false},
		{"no credential", ProviderConfig{ID: "openai", Kind: KindOpenAICompatible,
			BaseURL: "https://api.openai.com/v1"}, false},
		{"bad id", ProviderConfig{ID: "Open AI", Kind: KindOpenAICompatible,
			BaseURL: "https://api.openai.com/v1", CredentialID: "k"}, false},
		{"unknown kind", ProviderConfig{ID: "x", Kind: "grpc", CredentialID: "k"}, false},
	}
	for _, c := range cases {
		err := c.in.normalize().validate()
		if (err == nil) != c.ok {
			t.Fatalf("%s: err = %v, want ok = %v", c.name, err, c.ok)
		}
	}

	// A codex provider is forced to subscription billing and defaults to the ChatGPT
	// backend, whatever the caller asked for.
	p := ProviderConfig{ID: "codex", Kind: KindCodex, CredentialID: "oauth-codex", Billing: BillingMetered}.normalize()
	if p.Billing != BillingSubscription {
		t.Fatalf("billing = %q, want subscription", p.Billing)
	}
	if p.BaseURL != CodexBaseURL {
		t.Fatalf("base url = %q, want %q", p.BaseURL, CodexBaseURL)
	}
}

func TestSubscriptionRouteReservesNothingAndStillRecordsUsage(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	actx := credentials.WithActor(ctx, "admin")

	// A subscription credential: an OAuth entry whose id_token carries the account id.
	if _, err := h.creds.ImportOAuth(actx, credentials.OAuthImport{
		Provider: "codex", AccessToken: credentials.SecretInput{Value: "access-token"},
		RefreshToken: credentials.SecretInput{Value: "refresh-token"},
		IDToken:      credentials.SecretInput{Value: idTokenWithAccount("acct-123", "pro")},
		ExpiresIn:    3600,
	}); err != nil {
		t.Fatal(err)
	}

	var gotAccount, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccount = r.Header.Get("ChatGPT-Account-Id")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello \"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"world\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":11,\"output_tokens\":7}}}\n\n")
	}))
	defer srv.Close()

	if err := h.ai.PutProvider(ctx, ProviderConfig{
		ID: "codex", Kind: KindCodex, BaseURL: srv.URL, CredentialID: "oauth-codex",
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.ai.PutRoute(ctx, RouteInput{
		Name: "plan-chat", Capabilities: []string{"chat"},
		MaxInputTokens: 4096, MaxOutputTokens: 1024,
		// No pricing: a subscription attempt does not need any, and demanding it would
		// invent a per-token cost the plan does not charge.
		Attempts: []RouteAttemptInput{{Provider: "codex", Model: "gpt-5-codex"}},
	}); err != nil {
		t.Fatal(err)
	}

	resp, err := h.ai.Chat(WithPlugin(ctx, "hello"), ChatRequest{
		Model: "plan-chat", Messages: []Message{{Role: "user", Text: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "hello world" {
		t.Fatalf("text = %q", resp.Text)
	}
	if resp.Usage.CostMicroUSD != 0 {
		t.Fatalf("subscription call cost %d micro-USD, want 0", resp.Usage.CostMicroUSD)
	}
	if resp.Usage.InputTokens != 11 || resp.Usage.OutputTokens != 7 {
		t.Fatalf("usage = %+v, want the tokens the provider reported", resp.Usage)
	}
	if gotAccount != "acct-123" {
		t.Fatalf("ChatGPT-Account-Id = %q, want the id from the id_token", gotAccount)
	}
	if gotAuth != "Bearer access-token" {
		t.Fatalf("Authorization = %q", gotAuth)
	}

	// The reservation was zero, but the call is still audited like any other.
	var reserved, settled int64
	var billing string
	if err := h.ai.db.QueryRow(ctx,
		`SELECT reserved_micro_usd, settled_micro_usd FROM core_ai_calls WHERE logical_model = 'plan-chat'`).
		Scan(&reserved, &settled); err != nil {
		t.Fatal(err)
	}
	if reserved != 0 || settled != 0 {
		t.Fatalf("reserved %d settled %d, want 0/0", reserved, settled)
	}
	if err := h.ai.db.QueryRow(ctx,
		`SELECT billing_state FROM core_ai_attempts WHERE provider = 'codex'`).Scan(&billing); err != nil {
		t.Fatal(err)
	}
	if billing != "subscription" {
		t.Fatalf("billing_state = %q, want subscription", billing)
	}
}

func TestMeteredRouteStillRequiresPricing(t *testing.T) {
	h := newHarness(t)
	err := h.ai.PutRoute(context.Background(), RouteInput{
		Name: "unpriced", Capabilities: []string{"chat"},
		MaxInputTokens: 128, MaxOutputTokens: 64,
		Attempts: []RouteAttemptInput{{Provider: "fake", Model: "echo"}},
	})
	if !errors.Is(err, ErrMissingPrice) {
		t.Fatalf("err = %v, want ErrMissingPrice", err)
	}
}

func TestProviderInUseCannotBeDeleted(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.ai.DeleteProvider(ctx, "fake"); !errors.Is(err, ErrProviderInUse) {
		t.Fatalf("err = %v, want ErrProviderInUse", err)
	}
	if err := h.ai.DeleteRoute(ctx, "cheap-chat"); err != nil {
		t.Fatal(err)
	}
	if err := h.ai.DeleteProvider(ctx, "fake"); err != nil {
		t.Fatalf("delete after the last route went away: %v", err)
	}
	// With no provider left, nothing claims the credential any more.
	refs, err := h.creds.References(ctx, "fake-key")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 {
		t.Fatalf("references = %v, want none", refs)
	}
}

func TestProviderCredentialIsReferencedWhileConfigured(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	refs, err := h.creds.References(ctx, "fake-key")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0] != credentialRefOwner {
		t.Fatalf("references = %v, want [%s]", refs, credentialRefOwner)
	}
	if err := h.creds.Delete(credentials.WithActor(ctx, "admin"), "fake-key"); !errors.Is(err, credentials.ErrCredentialInUse) {
		t.Fatalf("err = %v, want ErrCredentialInUse", err)
	}
}

func TestModelsAreDiscoveredAndCached(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	models, err := h.ai.Models(ctx, "fake", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "echo" {
		t.Fatalf("models = %+v", models)
	}
	if !models[0].Priced || models[0].InputMicroUSDPerMillion != 1_000_000 {
		t.Fatalf("pricing did not survive the catalog round trip: %+v", models[0])
	}
	if models[0].FetchedAt.IsZero() {
		t.Fatal("fetched time was not recorded")
	}
	if _, err := h.ai.Models(ctx, "nope", false); !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("err = %v, want ErrUnknownProvider", err)
	}
}

func TestOpenAICompatibleDiscoveryReadsPublishedPricing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path = %q, want /models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer key" {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"data":[
		  {"id":"priced/model","name":"Priced","context_length":128000,
		   "pricing":{"prompt":"0.000003","completion":"0.000015"}},
		  {"id":"unpriced/model","pricing":{"prompt":"0","completion":"0"}}
		]}`))
	}))
	defer srv.Close()

	got, err := OpenAICompatible{}.Models(context.Background(), Dispatch{
		ProviderID: "openrouter", BaseURL: srv.URL, Token: "key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("models = %+v", got)
	}
	priced := got[0]
	if priced.ID != "priced/model" || !priced.Priced {
		t.Fatalf("first model = %+v", priced)
	}
	if priced.InputMicroUSDPerMillion != 3_000_000 || priced.OutputMicroUSDPerMillion != 15_000_000 {
		t.Fatalf("pricing = %d/%d micro-USD per million", priced.InputMicroUSDPerMillion, priced.OutputMicroUSDPerMillion)
	}
	if priced.ContextWindow != 128000 {
		t.Fatalf("context window = %d", priced.ContextWindow)
	}
	if got[1].Priced {
		t.Fatal("a published price of zero must not be reported as priced")
	}
}

func TestOpenAICompatibleChatChargesReportedTokens(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	actx := credentials.WithActor(ctx, "admin")
	if _, err := h.creds.CreateAPIKey(actx, credentials.APIKeyInput{
		ID: "router-key", Provider: "openrouter", Secret: credentials.SecretInput{Value: "sk-test"},
	}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body chatCompletionsRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		if body.Model != "some/model" {
			t.Errorf("model = %q", body.Model)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"pong"},
		    "finish_reason":"stop"}],"usage":{"prompt_tokens":1000,"completion_tokens":500}}`))
	}))
	defer srv.Close()

	if err := h.ai.PutProvider(ctx, ProviderConfig{
		ID: "openrouter", Kind: KindOpenAICompatible, BaseURL: srv.URL,
		CredentialID: "router-key", Billing: BillingMetered,
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.ai.PutRoute(ctx, RouteInput{
		Name: "router-chat", Capabilities: []string{"chat"},
		MaxInputTokens: 4000, MaxOutputTokens: 1000,
		Attempts: []RouteAttemptInput{{
			Provider: "openrouter", Model: "some/model",
			InputMicroUSDPerMillion: 3_000_000, OutputMicroUSDPerMillion: 15_000_000,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	resp, err := h.ai.Chat(WithPlugin(ctx, "hello"), ChatRequest{
		Model: "router-chat", Messages: []Message{{Role: "user", Text: "ping"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 1000 input at $3/M plus 500 output at $15/M is 3000 + 7500 micro-USD.
	if resp.Usage.CostMicroUSD != 10_500 {
		t.Fatalf("cost = %d micro-USD, want 10500", resp.Usage.CostMicroUSD)
	}
	if resp.Text != "pong" {
		t.Fatalf("text = %q", resp.Text)
	}
}

func TestOpenAICompatibleEmbedChargesReportedTokens(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	actx := credentials.WithActor(ctx, "admin")
	if _, err := h.creds.CreateAPIKey(actx, credentials.APIKeyInput{
		ID: "router-key", Provider: "openrouter", Secret: credentials.SecretInput{Value: "sk-test"},
	}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("path = %q", r.URL.Path)
		}
		var body embeddingsRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		if body.Model != "text-embedding-3-small" || len(body.Input) != 2 {
			t.Errorf("body = %+v", body)
		}
		_, _ = w.Write([]byte(`{"data":[
			{"embedding":[0.1,0.2],"index":1},
			{"embedding":[0.3,0.4],"index":0}
		],"usage":{"prompt_tokens":1000}}`))
	}))
	defer srv.Close()

	if err := h.ai.PutProvider(ctx, ProviderConfig{
		ID: "openrouter", Kind: KindOpenAICompatible, BaseURL: srv.URL,
		CredentialID: "router-key", Billing: BillingMetered,
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.ai.PutRoute(ctx, RouteInput{
		Name: "lot-embed", Capabilities: []string{"embed"},
		MaxInputTokens: 4000, MaxOutputTokens: 1,
		Attempts: []RouteAttemptInput{{
			Provider: "openrouter", Model: "text-embedding-3-small",
			InputMicroUSDPerMillion: 3_000_000, OutputMicroUSDPerMillion: 0,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	resp, err := h.ai.Embed(WithPlugin(ctx, "hello"), EmbedRequest{
		Model: "lot-embed", Inputs: []string{"tent", "stove"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.CostMicroUSD != 3_000 {
		t.Fatalf("cost = %d micro-USD, want 3000", resp.Usage.CostMicroUSD)
	}
	if len(resp.Vectors) != 2 || resp.Vectors[0][0] != 0.3 || resp.Vectors[1][0] != 0.1 {
		t.Fatalf("vectors = %+v, want index order", resp.Vectors)
	}
}

func TestRouteBecomesUnusableRatherThanUnknown(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Break the route out from under itself: rewrite its attempt to name a provider
	// that is not configured, the way a hand-edited database or a future migration
	// could, then reload.
	if _, err := h.ai.db.Exec(ctx,
		`UPDATE core_ai_route_attempts SET provider_id = 'gone' WHERE route_name = 'cheap-chat'`); err != nil {
		t.Fatal(err)
	}
	if err := h.ai.reload(ctx); err != nil {
		t.Fatal(err)
	}

	_, err := h.ai.Chat(WithPlugin(ctx, "hello"), ChatRequest{
		Model: "cheap-chat", Messages: []Message{{Role: "user", Text: "hi"}},
	})
	if !errors.Is(err, ErrRouteUncompiled) {
		t.Fatalf("err = %v, want ErrRouteUncompiled", err)
	}
	if errors.Is(err, ErrUnknownRoute) {
		t.Fatal("a misconfigured route must not look like a plugin naming a model that does not exist")
	}

	routes, err := h.ai.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].Healthy {
		t.Fatalf("routes = %+v, want the route listed and unhealthy", routes)
	}
	if !strings.Contains(routes[0].LastError, "gone") {
		t.Fatalf("last error = %q, want it to name the missing provider", routes[0].LastError)
	}
}

func TestSeedIsIgnoredOnceConfigured(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.ai.DeleteRoute(ctx, "cheap-chat"); err != nil {
		t.Fatal(err)
	}
	// The database is configured — a provider remains — so re-seeding must not bring
	// the deleted route back.
	if err := h.ai.seed(ctx, cheapChatSeed()); err != nil {
		t.Fatal(err)
	}
	routes, err := h.ai.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 0 {
		t.Fatalf("routes = %+v, want the deletion to stick", routes)
	}
}

func TestCodexStreamAggregation(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"type":"response.created","response":{}}`,
		`data: {"type":"response.output_text.delta","delta":"one "}`,
		`data: {"type":"response.output_text.delta","delta":"two"}`,
		`data: {"type":"response.completed","response":{"usage":{"input_tokens":5,"output_tokens":2}}}`,
		`data: [DONE]`,
		"",
	}, "\n\n")
	text, usage, err := readResponsesStream(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if text != "one two" {
		t.Fatalf("text = %q", text)
	}
	if usage.InputTokens != 5 || usage.OutputTokens != 2 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestCodexStreamWithoutDeltasUsesTheCompletedEvent(t *testing.T) {
	stream := `data: {"type":"response.completed","response":{"output":[{"content":[` +
		`{"type":"output_text","text":"whole answer"}]}],"usage":{"input_tokens":3,"output_tokens":4}}}` + "\n\n"
	text, usage, err := readResponsesStream(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if text != "whole answer" {
		t.Fatalf("text = %q", text)
	}
	if usage.OutputTokens != 4 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestCodexStreamReportsAFailedResponse(t *testing.T) {
	stream := `data: {"type":"response.failed","error":{"message":"model overloaded"}}` + "\n\n"
	_, _, err := readResponsesStream(strings.NewReader(stream))
	if err == nil || !strings.Contains(err.Error(), "model overloaded") {
		t.Fatalf("err = %v, want the provider's message", err)
	}
}

func TestCodexRefusesWithoutAnAccountID(t *testing.T) {
	_, err := Codex{}.Chat(context.Background(), Dispatch{
		ProviderID: "codex", BaseURL: "https://example.invalid", Token: "t", Model: "m",
	}, ChatRequest{Messages: []Message{{Role: "user", Text: "hi"}}})
	if !errors.Is(err, ErrMissingCredential) {
		t.Fatalf("err = %v, want ErrMissingCredential", err)
	}
}

func TestCodexChatHonorsSchema(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["basis"],"properties":{"basis":{"type":"string"}}}`)
	payload := `{"basis":"exact_text"}`
	delta, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var body struct {
			Text struct {
				Format struct {
					Type   string          `json:"type"`
					Name   string          `json:"name"`
					Schema json.RawMessage `json:"schema"`
				} `json:"format"`
			} `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		if body.Text.Format.Type != "json_schema" {
			t.Errorf("text.format.type = %q, want json_schema", body.Text.Format.Type)
		}
		if string(body.Text.Format.Schema) != string(schema) {
			t.Errorf("schema = %s", body.Text.Format.Schema)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":%s}\n\n", delta)
		fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":4}}}\n\n")
	}))
	defer srv.Close()

	got, err := Codex{Client: srv.Client()}.Chat(context.Background(), Dispatch{
		ProviderID: "codex", BaseURL: srv.URL, Token: "t", AccountID: "acct", Model: "m",
		Billing: BillingSubscription, attempt: attempt{billing: BillingSubscription},
	}, ChatRequest{
		Schema:   schema,
		Messages: []Message{{Role: "user", Text: "identify"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(got.parsed) != payload {
		t.Fatalf("parsed = %s, want %s", got.parsed, payload)
	}
}

// idTokenWithAccount builds an unsigned JWT carrying the namespaced ChatGPT claims.
// Nothing verifies the signature — the account id is routing metadata, and the provider
// is the one that decides whether the bearer token is real.
func idTokenWithAccount(accountID, plan string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, _ := json.Marshal(map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": accountID,
			"chatgpt_plan_type":  plan,
		},
	})
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}
