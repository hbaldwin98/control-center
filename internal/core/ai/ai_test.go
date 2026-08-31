package ai

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"path/filepath"
	"testing"

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
	creds, err := credentials.New(st, st, bus, credentials.Options{Keys: map[int][]byte{1: key}, Active: 1})
	if err != nil {
		t.Fatal(err)
	}
	actx := credentials.WithActor(ctx, "admin")
	if _, err := creds.CreateAPIKey(actx, credentials.APIKeyInput{
		ID: "fake-key", Provider: "fake", Secret: credentials.SecretInput{Value: "test-token"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := creds.Replace(ctx, "ai.routes", []string{"fake-key"}); err != nil {
		t.Fatal(err)
	}
	if err := pol.Register(ctx, "hello", false); err != nil {
		t.Fatal(err)
	}
	if err := pol.Enable(ctx, "hello", "test", "setup"); err != nil {
		t.Fatal(err)
	}
	routes, err := compileRoutes(File{Routes: map[string]RouteYAML{
		"cheap-chat": {
			Capabilities:    []string{"chat"},
			MaxInputTokens:  128,
			MaxOutputTokens: 64,
			Attempts: []AttemptYAML{{
				Provider: "fake", Model: "echo", Credential: "fake-key",
				InputMicroUSDPerMillion: 1_000_000, OutputMicroUSDPerMillion: 2_000_000,
			}},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(st, st, bus, pol, creds, Options{Routes: routes, Providers: []Provider{Fake{}}, Refs: creds})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, ai: svc, pol: pol, creds: creds, bus: bus}
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
