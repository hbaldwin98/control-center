package ai

import "context"

// Scoped stamps plugin identity onto every call. Chat and Embed reject a context
// without a plugin ID; this is how pluginhost supplies it.
func Scoped(s *Service, pluginID string) AI {
	return &scopedAI{s: s, pluginID: pluginID}
}

type scopedAI struct {
	s        *Service
	pluginID string
}

func (a *scopedAI) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	return a.s.Chat(WithPlugin(ctx, a.pluginID), req)
}

func (a *scopedAI) ChatStream(ctx context.Context, req ChatRequest) (Stream, error) {
	return a.s.ChatStream(WithPlugin(ctx, a.pluginID), req)
}

func (a *scopedAI) Embed(ctx context.Context, req EmbedRequest) (*EmbedResponse, error) {
	return a.s.Embed(WithPlugin(ctx, a.pluginID), req)
}
