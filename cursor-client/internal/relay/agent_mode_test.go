package relay

import (
	"encoding/binary"
	"strings"
	"testing"

	agentv1 "github.com/burpheart/cursor-tap/cursor_proto/gen/agent/v1"
	"google.golang.org/protobuf/proto"
)

func TestAgentModeFromConversationAction(t *testing.T) {
	askAction := &agentv1.ConversationAction{
		Action: &agentv1.ConversationAction_UserMessageAction{
			UserMessageAction: &agentv1.UserMessageAction{
				UserMessage: &agentv1.UserMessage{Text: "question", Mode: agentv1.AgentMode_AGENT_MODE_ASK},
			},
		},
	}
	if got := agentModeFromConversationAction(askAction); got != cursorAgentModeAsk {
		t.Fatalf("ask mode = %q, want ask", got)
	}

	planAction := &agentv1.ConversationAction{
		Action: &agentv1.ConversationAction_StartPlanAction{
			StartPlanAction: &agentv1.StartPlanAction{UserMessage: &agentv1.UserMessage{Text: "make a plan"}},
		},
	}
	if got := agentModeFromConversationAction(planAction); got != cursorAgentModePlan {
		t.Fatalf("start plan mode = %q, want plan", got)
	}

	executeAction := &agentv1.ConversationAction{
		Action: &agentv1.ConversationAction_ExecutePlanAction{
			ExecutePlanAction: &agentv1.ExecutePlanAction{},
		},
	}
	if got := agentModeFromConversationAction(executeAction); got != cursorAgentModeAgent {
		t.Fatalf("execute plan default mode = %q, want agent", got)
	}
}

func TestAgentProviderToolsAreModeAware(t *testing.T) {
	agentNames := providerToolNameSet(agentProviderTools("openai", "https://api.openai.test/v1/chat/completions", cursorAgentModeAgent))
	if !agentNames["StrReplace"] || !agentNames["Write"] {
		t.Fatalf("agent tools missing write tools: %#v", agentNames)
	}

	askNames := providerToolNameSet(agentProviderTools("openai", "https://api.openai.test/v1/chat/completions", cursorAgentModeAsk))
	if !askNames["Read"] || !askNames["Grep"] {
		t.Fatalf("ask tools missing read/search tools: %#v", askNames)
	}
	for _, name := range []string{"Delete", "StrReplace", "Write", "Shell"} {
		if askNames[name] {
			t.Fatalf("ask tools exposed mutating/risky tool %s: %#v", name, askNames)
		}
	}

	planNames := providerToolNameSet(agentProviderTools("openai", "https://api.openai.test/v1/chat/completions", cursorAgentModePlan))
	if !planNames["CreatePlan"] || !planNames["Read"] {
		t.Fatalf("plan tools missing CreatePlan/read tools: %#v", planNames)
	}
	for _, name := range []string{"Delete", "StrReplace", "Write", "Shell"} {
		if planNames[name] {
			t.Fatalf("plan tools exposed mutating/risky tool %s: %#v", name, planNames)
		}
	}
}

func TestAgentModeSystemPromptLoadsModeFiles(t *testing.T) {
	planMessages := withAgentToolSystemMessage([]chatMessage{{Role: "user", Content: "plan"}}, `D:\win\cursor`, cursorAgentModePlan)
	if len(planMessages) == 0 || planMessages[0].Role != "system" {
		t.Fatalf("missing plan system message: %#v", planMessages)
	}
	if !strings.Contains(planMessages[0].Content, "Plan mode is active") || !strings.Contains(planMessages[0].Content, "Plan mode is read-only") {
		t.Fatalf("plan prompt did not include plan mode reminders: %.200q", planMessages[0].Content)
	}

	askMessages := withAgentToolSystemMessage([]chatMessage{{Role: "user", Content: "ask"}}, "", cursorAgentModeAsk)
	if !strings.Contains(askMessages[0].Content, "Ask mode is read-only") {
		t.Fatalf("ask prompt did not include ask mode reminder: %.200q", askMessages[0].Content)
	}
}

func TestAgentConversationCheckpointFrameUsesRequestedMode(t *testing.T) {
	raw, err := buildAgentConversationCheckpointFrame(10, 200, "", cursorAgentModePlan)
	if err != nil {
		t.Fatalf("buildAgentConversationCheckpointFrame() error = %v", err)
	}
	if len(raw) < 5 {
		t.Fatalf("frame length = %d, want framed proto", len(raw))
	}
	length := int(binary.BigEndian.Uint32(raw[1:5]))
	if length != len(raw)-5 {
		t.Fatalf("frame payload length = %d, actual %d", length, len(raw)-5)
	}

	msg := &agentv1.AgentServerMessage{}
	if err := proto.Unmarshal(raw[5:], msg); err != nil {
		t.Fatalf("checkpoint proto decode failed: %v", err)
	}
	checkpoint := msg.GetConversationCheckpointUpdate()
	if checkpoint == nil {
		t.Fatalf("expected conversation checkpoint, got %T", msg.GetMessage())
	}
	if got := checkpoint.GetMode(); got != agentv1.AgentMode_AGENT_MODE_PLAN {
		t.Fatalf("mode = %s, want AGENT_MODE_PLAN", got)
	}
}

func TestReferenceConversationCheckpointPayloadUsesRequestedMode(t *testing.T) {
	g := NewGateway(Config{})
	req := unifiedChatRequest{
		RequestID:       "req-mode",
		AgentMode:       cursorAgentModeAsk,
		CurrentUserText: "hello",
		Messages:        []chatMessage{{Role: "user", Content: "hello"}},
	}
	payload, err := g.buildReferenceAgentConversationCheckpointPayload(req, "answer", nil, 10, 200)
	if err != nil {
		t.Fatalf("buildReferenceAgentConversationCheckpointPayload() error = %v", err)
	}
	if got := payload.State.GetMode(); got != agentv1.AgentMode_AGENT_MODE_ASK {
		t.Fatalf("state mode = %s, want AGENT_MODE_ASK", got)
	}
}

func providerToolNameSet(tools []any) map[string]bool {
	out := map[string]bool{}
	for _, raw := range tools {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if name, ok := item["name"].(string); ok {
			out[name] = true
			continue
		}
		if fn, ok := item["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok {
				out[name] = true
			}
		}
	}
	return out
}
