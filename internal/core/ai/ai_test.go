package ai

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/hbaldwin98/control-center/internal/core/credentials"
	"github.com/hbaldwin98/control-center/internal/core/events"
	"github.com/hbaldwin98/control-center/internal/core/policy"
	"github.com/hbaldwin98/control-center/internal/core/storage"
)

type harness struct {
	t     *testing.T
	ai    *Service
	pol   *policy.Store
	creds *credentials.Store
	bus   *events.Log
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	st, err := storage.Open(ctx, storage.Options{Path: filepath.Join(t.TempDir(), "ai.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bus, err := events.New(st, st, events.Options{})
	if err != nil {
		t.Fatal(err)
	}
	pol, err := policy.New(st, st, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	creds, err := credentials.New(st, st, bus, credentials.Options{
		Keys: map[int][]byte{1: key}, Active: 1,
		OAuth: map[string]credentials.OAuthProvider{"codex": {
			AuthURL: "https://auth.example/authorize", TokenURL: "https://auth.example/token",
			ClientID: "test-client", RedirectURI: "http://localhost:1455/auth/callback",
			AccountClaim: []string{"https://api.openai.com/auth", "chatgpt_account_id"},
			PlanClaim:    []string{"https://api.openai.com/auth", "chatgpt_plan_type"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	actx := credentials.WithActor(ctx, "admin")
	if _, err := creds.CreateAPIKey(actx, credentials.APIKeyInput{
		ID: "fake-key", Provider: "fake", Secret: credentials.SecretInput{Value: "test-token"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := pol.Register(ctx, "hello", false); err != nil {
		t.Fatal(err)
	}
	if err := pol.Enable(ctx, "hello", "test", "setup"); err != nil {
		t.Fatal(err)
	}
	svc, err := New(st, st, bus, pol, creds, Options{Seed: cheapChatSeed(), Refs: creds})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, ai: svc, pol: pol, creds: creds, bus: bus}
}

// cheapChatSeed is the one route every test in this file dispatches through: a single
// metered attempt on the in-process fake provider.
func cheapChatSeed() Seed {
	return Seed{
		Providers: []ProviderConfig{{
			ID: "fake", Kind: KindFake, CredentialID: "fake-key", Billing: BillingMetered,
		}},
		Routes: []RouteInput{{
			Name: "cheap-chat", Capabilities: []string{"chat"},
			MaxInputTokens: 128, MaxOutputTokens: 64,
			Attempts: []RouteAttemptInput{{
				Provider: "fake", Model: "echo",
				InputMicroUSDPerMillion: 1_000_000, OutputMicroUSDPerMillion: 2_000_000,
			}},
		}},
	}
}

func TestChatReservesAndSettles(t *testing.T) {
	h := newHarness(t)
	ctx := WithPlugin(context.Background(), "hello")
	resp, err := h.ai.Chat(ctx, ChatRequest{Model: "cheap-chat", Messages: []Message{{Role: "user", Text: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "echo: hi" || resp.Usage.CostMicroUSD <= 0 || resp.Usage.Attempts != 1 {
		t.Fatalf("%+v", resp)
	}
	page, err := h.ai.Calls(ctx, CallQuery{PluginID: "hello"})
	if err != nil || len(page.Calls) != 1 || page.Calls[0].Status != "succeeded" {
		t.Fatalf("%+v %v", page, err)
	}
	if page.Calls[0].SettledMicroUSD != resp.Usage.CostMicroUSD {
		t.Fatalf("settled %d usage %d", page.Calls[0].SettledMicroUSD, resp.Usage.CostMicroUSD)
	}
	st, err := h.pol.State(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if st.CommittedDay != resp.Usage.CostMicroUSD || st.ReservedDay != 0 {
		t.Fatalf("spend %+v", st)
	}
	events, err := h.bus.Query(ctx, events.Query{Pattern: events.TypeAIUsage})
	if err != nil || len(events) != 1 {
		t.Fatalf("usage events %v %v", events, err)
	}
}

func TestFakeChatHonorsMaxTokens(t *testing.T) {
	h := newHarness(t)
	ctx := WithPlugin(context.Background(), "hello")
	resp, err := h.ai.Chat(ctx, ChatRequest{
		Model: "cheap-chat", MaxTokens: 8,
		Messages: []Message{{Role: "user", Text: "This prompt is long enough that the echo must be truncated by the fake provider."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.OutputTokens > 8 {
		t.Fatalf("output tokens = %d, want at most 8", resp.Usage.OutputTokens)
	}
}

func TestChatRequiresPluginAndEnabled(t *testing.T) {
	h := newHarness(t)
	if _, err := h.ai.Chat(context.Background(), ChatRequest{Model: "cheap-chat"}); !errors.Is(err, ErrNoPlugin) {
		t.Fatalf("no plugin: %v", err)
	}
	if err := h.pol.Disable(context.Background(), "hello", "test", "off"); err != nil {
		t.Fatal(err)
	}
	_, err := h.ai.Chat(WithPlugin(context.Background(), "hello"), ChatRequest{
		Model: "cheap-chat", Messages: []Message{{Role: "user", Text: "x"}},
	})
	if !errors.Is(err, policy.ErrPluginDisabled) {
		t.Fatalf("disabled: %v", err)
	}
}

func TestChatStreamYieldsEOFAfterAccounting(t *testing.T) {
	h := newHarness(t)
	stream, err := h.ai.ChatStream(WithPlugin(context.Background(), "hello"), ChatRequest{
		Model: "cheap-chat", Messages: []Message{{Role: "user", Text: "stream"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := stream.Recv()
	if err != nil || chunk.Text != "echo: stream" || chunk.Usage == nil {
		t.Fatalf("%+v %v", chunk, err)
	}
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("eof: %v", err)
	}
}

func TestRoutesHideCredentials(t *testing.T) {
	h := newHarness(t)
	routes, err := h.ai.Routes(context.Background())
	if err != nil || len(routes) != 1 {
		t.Fatalf("%v %v", routes, err)
	}
	if routes[0].AttemptPlan[0].Provider != "fake" || routes[0].AttemptPlan[0].Model != "echo" {
		t.Fatalf("%+v", routes[0])
	}
}

func TestChatAdmittedBeforeDisableStillSettles(t *testing.T) {
	ctx := context.Background()
	st, err := storage.Open(ctx, storage.Options{Path: filepath.Join(t.TempDir(), "ai.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bus, err := events.New(st, st, events.Options{})
	if err != nil {
		t.Fatal(err)
	}
	pol, err := policy.New(st, st, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	creds, err := credentials.New(st, st, bus, credentials.Options{
		Keys: map[int][]byte{1: key}, Active: 1,
		OAuth: map[string]credentials.OAuthProvider{"codex": {
			AuthURL: "https://auth.example/authorize", TokenURL: "https://auth.example/token",
			ClientID: "test-client", RedirectURI: "http://localhost:1455/auth/callback",
			AccountClaim: []string{"https://api.openai.com/auth", "chatgpt_account_id"},
			PlanClaim:    []string{"https://api.openai.com/auth", "chatgpt_plan_type"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	actx := credentials.WithActor(ctx, "admin")
	if _, err := creds.CreateAPIKey(actx, credentials.APIKeyInput{
		ID: "fake-key", Provider: "fake", Secret: credentials.SecretInput{Value: "test-token"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := pol.Register(ctx, "hello", false); err != nil {
		t.Fatal(err)
	}
	if err := pol.Enable(ctx, "hello", "test", "setup"); err != nil {
		t.Fatal(err)
	}
	hold := NewHoldFake()
	svc, err := New(st, st, bus, pol, creds, Options{
		Seed: cheapChatSeed(), Providers: []Provider{hold}, Refs: creds,
	})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := svc.Chat(WithPlugin(context.Background(), "hello"), ChatRequest{
			Model: "cheap-chat", Messages: []Message{{Role: "user", Text: "hi"}},
		})
		done <- err
	}()
	select {
	case <-hold.Started():
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not start")
	}
	if err := pol.Disable(ctx, "hello", "test", "mid"); err != nil {
		t.Fatal(err)
	}
	hold.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("chat did not finish")
	}
	page, err := svc.Calls(ctx, CallQuery{PluginID: "hello"})
	if err != nil || len(page.Calls) != 1 || page.Calls[0].Status != "succeeded" || page.Calls[0].SettledMicroUSD <= 0 {
		t.Fatalf("%+v %v", page, err)
	}
}

func TestEmbedReservesAndSettles(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.ai.PutRoute(ctx, RouteInput{
		Name: "lot-embed", Capabilities: []string{"embed"},
		MaxInputTokens: 512, MaxOutputTokens: 1,
		Attempts: []RouteAttemptInput{{
			Provider: "fake", Model: "echo",
			InputMicroUSDPerMillion: 1_000_000, OutputMicroUSDPerMillion: 0,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	resp, err := h.ai.Embed(WithPlugin(ctx, "hello"), EmbedRequest{
		Model: "lot-embed", Inputs: []string{"camping tent", "coffee maker"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Vectors) != 2 || len(resp.Vectors[0]) == 0 {
		t.Fatalf("%+v", resp)
	}
	if resp.Usage.CostMicroUSD <= 0 || resp.Usage.OutputTokens != 0 || resp.Usage.Attempts != 1 {
		t.Fatalf("usage %+v", resp.Usage)
	}
	page, err := h.ai.Calls(ctx, CallQuery{PluginID: "hello", LogicalModel: "lot-embed"})
	if err != nil || len(page.Calls) != 1 || page.Calls[0].Status != "succeeded" || page.Calls[0].Operation != "embed" {
		t.Fatalf("%+v %v", page, err)
	}
}

func TestEmbedRequiresCapabilityAndBoundedInput(t *testing.T) {
	h := newHarness(t)
	ctx := WithPlugin(context.Background(), "hello")
	if _, err := h.ai.Embed(ctx, EmbedRequest{Model: "cheap-chat", Inputs: []string{"hi"}}); !errors.Is(err, ErrCapability) {
		t.Fatalf("chat route: %v", err)
	}
	if _, err := h.ai.Embed(ctx, EmbedRequest{Model: "cheap-chat", Inputs: nil}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty: %v", err)
	}
}

func TestFakeEmbedIsSemanticEnoughForIntent(t *testing.T) {
	campQ := fakeEmbed("things that would help me camp\ntent headlamp lantern canopy cooler stove")
	tent := fakeEmbed("4-person camping tent")
	lamp := fakeEmbed("Black Diamond LED headlamp")
	coffee := fakeEmbed("Keurig K-Supreme Plus")
	chair := fakeEmbed("Herman Miller Aeron office chair")
	if cosine(campQ, tent) <= cosine(campQ, coffee) || cosine(campQ, tent) <= cosine(campQ, chair) {
		t.Fatalf("camp→tent=%v camp→coffee=%v camp→chair=%v", cosine(campQ, tent), cosine(campQ, coffee), cosine(campQ, chair))
	}
	if cosine(campQ, lamp) <= cosine(campQ, coffee) {
		t.Fatalf("expanded camping should retrieve a headlamp: camp→lamp=%v camp→coffee=%v", cosine(campQ, lamp), cosine(campQ, coffee))
	}
	coffeeQ := fakeEmbed("coffee")
	if cosine(coffeeQ, coffee) <= cosine(coffeeQ, tent) {
		t.Fatalf("coffee→keurig=%v coffee→tent=%v", cosine(coffeeQ, coffee), cosine(coffeeQ, tent))
	}
}

func cosine(a, b []float64) float64 {
	if len(a) != len(b) {
		return 0
	}
	var sum float64
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}
