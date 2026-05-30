package relay

import (
	"strings"

	agentv1 "github.com/burpheart/cursor-tap/cursor_proto/gen/agent/v1"
)

type cursorAgentMode string

const (
	cursorAgentModeAgent cursorAgentMode = "agent"
	cursorAgentModeAsk   cursorAgentMode = "ask"
	cursorAgentModePlan  cursorAgentMode = "plan"
)

var askModeAgentTools = map[string]bool{
	"AskQuestion":      true,
	"CallMcpTool":      true,
	"FetchMcpResource": true,
	"Glob":             true,
	"Grep":             true,
	"Ls":               true,
	"Read":             true,
	"ReadLints":        true,
	"Task":             true,
	"WebFetch":         true,
	"WebSearch":        true,
}

var planModeAgentTools = map[string]bool{
	"AskQuestion": true,
	"CreatePlan":  true,
	"Glob":        true,
	"Grep":        true,
	"Ls":          true,
	"Read":        true,
	"ReadLints":   true,
	"Task":        true,
	"WebFetch":    true,
	"WebSearch":   true,
}

func normalizeCursorAgentMode(mode string) cursorAgentMode {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "ask", "agent_mode_ask":
		return cursorAgentModeAsk
	case "plan", "agent_mode_plan":
		return cursorAgentModePlan
	case "agent", "edit", "execute", "agent_mode_agent":
		return cursorAgentModeAgent
	default:
		return ""
	}
}

func defaultCursorAgentMode(mode cursorAgentMode) cursorAgentMode {
	if mode == "" {
		return cursorAgentModeAgent
	}
	return mode
}

func cursorAgentModeFromProto(mode agentv1.AgentMode) cursorAgentMode {
	switch mode {
	case agentv1.AgentMode_AGENT_MODE_ASK:
		return cursorAgentModeAsk
	case agentv1.AgentMode_AGENT_MODE_PLAN:
		return cursorAgentModePlan
	case agentv1.AgentMode_AGENT_MODE_AGENT:
		return cursorAgentModeAgent
	default:
		return ""
	}
}

func (mode cursorAgentMode) protoAgentMode() agentv1.AgentMode {
	switch defaultCursorAgentMode(mode) {
	case cursorAgentModeAsk:
		return agentv1.AgentMode_AGENT_MODE_ASK
	case cursorAgentModePlan:
		return agentv1.AgentMode_AGENT_MODE_PLAN
	default:
		return agentv1.AgentMode_AGENT_MODE_AGENT
	}
}

func (mode cursorAgentMode) dirName() string {
	switch defaultCursorAgentMode(mode) {
	case cursorAgentModeAsk:
		return "ask"
	case cursorAgentModePlan:
		return "plan"
	default:
		return "agent"
	}
}

func (mode cursorAgentMode) displayName() string {
	switch defaultCursorAgentMode(mode) {
	case cursorAgentModeAsk:
		return "ask"
	case cursorAgentModePlan:
		return "plan"
	default:
		return "agent"
	}
}

func agentModeFromRunRequest(req *agentv1.AgentRunRequest) cursorAgentMode {
	if req == nil {
		return ""
	}
	if mode := agentModeFromConversationAction(req.GetAction()); mode != "" {
		return mode
	}
	if mode := cursorAgentModeFromProto(req.GetConversationState().GetMode()); mode != "" {
		return mode
	}
	return ""
}

func agentModeFromConversationAction(action *agentv1.ConversationAction) cursorAgentMode {
	if action == nil {
		return ""
	}
	if start := action.GetStartPlanAction(); start != nil {
		return cursorAgentModePlan
	}
	if exec := action.GetExecutePlanAction(); exec != nil {
		if mode := cursorAgentModeFromProto(exec.GetExecutionMode()); mode != "" {
			return mode
		}
		return cursorAgentModeAgent
	}
	if uma := action.GetUserMessageAction(); uma != nil {
		if mode := cursorAgentModeFromProto(uma.GetUserMessage().GetMode()); mode != "" {
			return mode
		}
	}
	return ""
}

func isAllowedAgentToolNameForMode(name string, mode cursorAgentMode) bool {
	canonical := canonicalAgentToolDefinitionName(name)
	if !isAllowedAgentToolName(canonical) {
		return false
	}
	switch defaultCursorAgentMode(mode) {
	case cursorAgentModeAsk:
		return askModeAgentTools[canonical]
	case cursorAgentModePlan:
		return planModeAgentTools[canonical]
	default:
		return true
	}
}
