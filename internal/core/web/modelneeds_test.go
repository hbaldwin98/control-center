package web

import (
	"testing"

	"github.com/hbaldwin98/control-center/host"
	"github.com/hbaldwin98/control-center/internal/core/ai"
)

func TestBindModelNeeds(t *testing.T) {
	needs := []host.ModelNeed{
		{Name: "cheap-vision", Capabilities: []string{"chat", "vision"}, Purpose: "photos"},
		{Name: "grounded-price", Capabilities: []string{"chat", "grounding"}, Purpose: "price"},
		{Name: "cheap-chat", Capabilities: []string{"chat"}, Purpose: "chat"},
	}
	routes := []ai.RouteDescriptor{
		{
			LogicalName:  "cheap-vision",
			Capabilities: []string{"chat", "vision"},
			Healthy:      true,
			AttemptPlan:  []ai.AttemptDescriptor{{Provider: "or", Model: "gemini"}},
		},
		{
			LogicalName:  "grounded-price",
			Capabilities: []string{"chat"},
			Healthy:      true,
			AttemptPlan:  []ai.AttemptDescriptor{{Provider: "or", Model: "gpt"}},
		},
		{
			LogicalName:  "cheap-chat",
			Capabilities: []string{"chat"},
			Healthy:      false,
			LastError:    "credential revoked",
			AttemptPlan:  []ai.AttemptDescriptor{{Provider: "or", Model: "gpt"}},
		},
	}

	got := bindModelNeeds(needs, routes)
	if len(got) != 3 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].Status != needReady || got[0].Provider != "or" || got[0].Model != "gemini" {
		t.Fatalf("vision = %+v", got[0])
	}
	if got[1].Status != needCapabilityMismatch {
		t.Fatalf("price status = %s, want capability_mismatch", got[1].Status)
	}
	if got[2].Status != needUnhealthy || got[2].LastError != "credential revoked" {
		t.Fatalf("chat = %+v", got[2])
	}

	missing := bindModelNeeds(needs, nil)
	for _, v := range missing {
		if v.Status != needMissing {
			t.Fatalf("%s status = %s, want missing", v.Name, v.Status)
		}
	}
}

func TestUnionNeedMergesCapabilities(t *testing.T) {
	need, ok := unionNeed("cheap-chat", []host.Manifest{
		{Models: []host.ModelNeed{{Name: "cheap-chat", Capabilities: []string{"chat"}, Purpose: "a"}}},
		{Models: []host.ModelNeed{{Name: "cheap-chat", Capabilities: []string{"chat", "vision"}, Purpose: "b"}}},
	})
	if !ok {
		t.Fatal("expected a match")
	}
	if len(need.Capabilities) != 2 || need.Capabilities[0] != "chat" || need.Capabilities[1] != "vision" {
		t.Fatalf("caps = %v", need.Capabilities)
	}
	if _, ok := unionNeed("nope", nil); ok {
		t.Fatal("unknown name should miss")
	}
}

func TestDefaultRouteLimits(t *testing.T) {
	in, out := defaultRouteLimits([]string{"chat"})
	if in != defaultChatMaxInput || out != defaultChatMaxOutput {
		t.Fatalf("chat limits %d/%d", in, out)
	}
	in, out = defaultRouteLimits([]string{"chat", "vision"})
	if in != defaultVisionMaxInput {
		t.Fatalf("vision input %d", in)
	}
	in, out = defaultRouteLimits([]string{"embed"})
	if in != defaultEmbedMaxInput || out != defaultEmbedMaxOutput {
		t.Fatalf("embed limits %d/%d", in, out)
	}
}
