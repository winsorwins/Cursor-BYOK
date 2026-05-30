package relay

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	agentv1 "github.com/burpheart/cursor-tap/cursor_proto/gen/agent/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

const maxAgentToolTurns = 14

const (
	agentProviderTurnTimeout       = 5 * time.Minute
	agentFinalAnswerTimeout        = 5 * time.Minute
	agentToolResultLimit           = 8000
	agentFinalToolResultLimit      = 4000
	agentMaxToolExecutions         = 24
	agentEditReminderToolThreshold = 4
	agentMaxEditReminders          = 3
)

var allowedAgentTools = map[string]bool{
	"AskQuestion":          true,
	"CallMcpTool":          true,
	"CreatePlan":           true,
	"Delete":               true,
	"FetchMcpResource":     true,
	"ForceBackgroundShell": true,
	"Glob":                 true,
	"Grep":                 true,
	"Ls":                   true,
	"PatchEdit":            true,
	"Read":                 true,
	"ReadLints":            true,
	"Shell":                true,
	"StrReplace":           true,
	"SwitchMode":           true,
	"Task":                 true,
	"TodoWrite":            true,
	"WebFetch":             true,
	"WebSearch":            true,
	"Write":                true,
	"WriteShellStdin":      true,
}

var referenceAgentToolOrder = []string{
	"AskQuestion",
	"CallMcpTool",
	"CreatePlan",
	"Delete",
	"FetchMcpResource",
	"Glob",
	"Grep",
	"Read",
	"Ls",
	"ReadLints",
	"Shell",
	"WriteShellStdin",
	"ForceBackgroundShell",
	"StrReplace",
	"SwitchMode",
	"Task",
	"TodoWrite",
	"WebFetch",
	"WebSearch",
	"Write",
}

type agentToolDefinition struct {
	Name        string
	Description string
	Parameters  map[string]any
}

type agentToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type agentToolExecution struct {
	Call          agentToolCall
	Args          map[string]any
	ResultText    string
	StartedCall   *agentv1.ToolCall
	CompletedCall *agentv1.ToolCall
}

type agentExecResult struct {
	Message *agentv1.ExecClientMessage
	OK      bool
	Error   error
}

type agentProviderTurnResult struct {
	ToolCalls           []agentToolCall
	Text                string
	TextChars           int
	PromptTokens        int
	CompletionTokens    int
	CacheReadTokens     int
	CacheWriteTokens    int
	TokenDeltaSent      int
	UsageTokenDeltaSent int
}

var (
	agentToolDefinitionsMu     sync.Mutex
	agentToolDefinitionsByMode map[cursorAgentMode][]agentToolDefinition
	agentSystemPromptsMu       sync.Mutex
	agentSystemPromptsByMode   map[cursorAgentMode]string
)

func agentProviderTools(provider string, endpoint string, modes ...cursorAgentMode) []any {
	mode := cursorAgentModeAgent
	if len(modes) > 0 {
		mode = defaultCursorAgentMode(modes[0])
	}
	defs := loadAgentToolDefinitionsForMode(mode)
	out := make([]any, 0, len(defs))
	provider = strings.ToLower(strings.TrimSpace(provider))
	responses := provider == "openai" && strings.Contains(strings.ToLower(endpoint), "responses")
	for _, def := range defs {
		if def.Name == "" || !isAllowedAgentToolNameForMode(def.Name, mode) {
			continue
		}
		switch {
		case provider == "anthropic":
			out = append(out, map[string]any{
				"name":         def.Name,
				"description":  def.Description,
				"input_schema": def.Parameters,
			})
		case responses:
			out = append(out, map[string]any{
				"type":        "function",
				"name":        def.Name,
				"description": def.Description,
				"parameters":  def.Parameters,
			})
		default:
			out = append(out, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        def.Name,
					"description": def.Description,
					"parameters":  def.Parameters,
				},
			})
		}
	}
	return out
}

func loadAgentToolDefinitions() []agentToolDefinition {
	return loadAgentToolDefinitionsForMode(cursorAgentModeAgent)
}

func loadAgentToolDefinitionsForMode(mode cursorAgentMode) []agentToolDefinition {
	mode = defaultCursorAgentMode(mode)
	agentToolDefinitionsMu.Lock()
	if agentToolDefinitionsByMode != nil {
		if defs := agentToolDefinitionsByMode[mode]; len(defs) > 0 {
			agentToolDefinitionsMu.Unlock()
			return defs
		}
	}
	agentToolDefinitionsMu.Unlock()

	defs, err := readAgentToolDefinitionsFromDisk(mode)
	if err != nil {
		log.Printf("[Gateway] %s tools fallback: %v", mode.displayName(), err)
		defs = embeddedAgentToolDefinitions()
	}

	agentToolDefinitionsMu.Lock()
	if agentToolDefinitionsByMode == nil {
		agentToolDefinitionsByMode = map[cursorAgentMode][]agentToolDefinition{}
	}
	agentToolDefinitionsByMode[mode] = defs
	agentToolDefinitionsMu.Unlock()
	return defs
}

func readAgentToolDefinitionsFromDisk(mode cursorAgentMode) ([]agentToolDefinition, error) {
	path := findAgentToolsJSONPath(mode)
	if path == "" {
		return nil, fmt.Errorf("%s tools.json not found", defaultCursorAgentMode(mode).displayName())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Type     string `json:"type"`
		Function struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			Parameters  map[string]any `json:"parameters"`
		} `json:"function"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	defs := make([]agentToolDefinition, 0, len(raw))
	for _, item := range raw {
		name := canonicalAgentToolDefinitionName(item.Function.Name)
		if name == "" || !isAllowedAgentToolName(name) {
			continue
		}
		defs = append(defs, agentToolDefinition{
			Name:        name,
			Description: item.Function.Description,
			Parameters:  item.Function.Parameters,
		})
	}
	if len(defs) == 0 {
		return nil, fmt.Errorf("agent tools.json has no supported tools")
	}
	defs = alignAgentToolDefinitions(defs)
	log.Printf("[Gateway] loaded %d %s tool definitions from %s", len(defs), defaultCursorAgentMode(mode).displayName(), path)
	return defs, nil
}

func alignAgentToolDefinitions(defs []agentToolDefinition) []agentToolDefinition {
	byName := map[string]agentToolDefinition{}
	for _, def := range defs {
		name := canonicalAgentToolDefinitionName(def.Name)
		if name == "" || !isAllowedAgentToolName(name) {
			continue
		}
		def.Name = name
		byName[name] = def
	}
	for _, def := range syntheticAgentToolDefinitions() {
		name := canonicalAgentToolDefinitionName(def.Name)
		if name == "" || !isAllowedAgentToolName(name) {
			continue
		}
		def.Name = name
		if _, ok := byName[name]; !ok {
			byName[name] = def
		}
	}
	out := make([]agentToolDefinition, 0, len(referenceAgentToolOrder))
	for _, name := range referenceAgentToolOrder {
		if def, ok := byName[name]; ok {
			out = append(out, def)
		}
	}
	return out
}

func findAgentToolsJSONPath(modes ...cursorAgentMode) string {
	mode := cursorAgentModeAgent
	if len(modes) > 0 {
		mode = defaultCursorAgentMode(modes[0])
	}
	return findAgentModeFilePath(mode, "tools.json")
}

func findAgentModeFilePath(mode cursorAgentMode, filename string) string {
	rel := filepath.Join("cursor提示词与工具调用", "cursor_modes", defaultCursorAgentMode(mode).dirName(), filename)
	candidates := []string{}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(wd, rel),
			filepath.Join(wd, "..", rel),
			filepath.Join(wd, "..", "..", rel),
			filepath.Join(wd, "..", "..", "..", rel),
		)
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, rel),
			filepath.Join(dir, "..", rel),
			filepath.Join(dir, "..", "..", rel),
			filepath.Join(dir, "..", "..", "..", rel),
		)
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func embeddedAgentToolDefinitions() []agentToolDefinition {
	defs := []agentToolDefinition{
		{
			Name:        "Read",
			Description: "Read a local file. Use absolute paths when possible.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":   map[string]any{"type": "string"},
					"offset": map[string]any{"type": "integer"},
					"limit":  map[string]any{"type": "integer"},
				},
				"required": []any{"path"},
			},
		},
		{
			Name:        "Grep",
			Description: "Search text in files under a path or workspace.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern":    map[string]any{"type": "string"},
					"path":       map[string]any{"type": "string"},
					"glob":       map[string]any{"type": "string"},
					"head_limit": map[string]any{"type": "integer"},
					"-i":         map[string]any{"type": "boolean"},
				},
				"required": []any{"pattern"},
			},
		},
		{
			Name:        "Glob",
			Description: "Find files matching a glob pattern.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"glob_pattern":     map[string]any{"type": "string"},
					"target_directory": map[string]any{"type": "string"},
				},
				"required": []any{"glob_pattern"},
			},
		},
		{
			Name:        "Ls",
			Description: "List files and directories under a local path.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string"},
				},
				"required": []any{"path"},
			},
		},
		{
			Name:        "Shell",
			Description: "Run a conservative local shell command for inspection or tests.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command":           map[string]any{"type": "string"},
					"working_directory": map[string]any{"type": "string"},
					"block_until_ms":    map[string]any{"type": "integer"},
				},
				"required": []any{"command"},
			},
		},
	}
	return alignAgentToolDefinitions(defs)
}

func syntheticAgentToolDefinitions() []agentToolDefinition {
	return []agentToolDefinition{
		{
			Name:        "Ls",
			Description: "List files and directories under a local path.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "Absolute or workspace-relative directory path to list."},
				},
				"required": []any{"path"},
			},
		},
		{
			Name:        "PatchEdit",
			Description: "Performs exact string replacements in files. The old_string must uniquely identify the text unless replace_all is true.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":        map[string]any{"type": "string", "description": "Absolute path to the file to modify."},
					"old_string":  map[string]any{"type": "string", "description": "Exact text to replace."},
					"new_string":  map[string]any{"type": "string", "description": "Replacement text."},
					"replace_all": map[string]any{"type": "boolean", "description": "Replace every occurrence of old_string."},
				},
				"required": []any{"path", "old_string", "new_string"},
			},
		},
		{
			Name:        "WriteShellStdin",
			Description: "Writes characters to a background shell session.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"shell_id": map[string]any{"type": "number"},
					"chars":    map[string]any{"type": "string"},
				},
				"required": []any{"shell_id", "chars"},
			},
		},
		{
			Name:        "ForceBackgroundShell",
			Description: "Starts a long-running shell command in the background.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command":           map[string]any{"type": "string"},
					"working_directory": map[string]any{"type": "string"},
				},
				"required": []any{"command"},
			},
		},
	}
}

func withAgentToolSystemMessage(messages []chatMessage, workspaceRoot string, modes ...cursorAgentMode) []chatMessage {
	mode := cursorAgentModeAgent
	if len(modes) > 0 {
		mode = defaultCursorAgentMode(modes[0])
	}
	prompt := loadAgentSystemPrompt(mode)
	if prompt == "" {
		prompt = "You are running inside Cursor through a local BYOK proxy. Use the available tools to inspect local files instead of guessing."
	}
	prompt = strings.TrimSpace(prompt) + "\n\n" + agentLocalSystemReminder(mode, workspaceRoot)
	if workspaceRoot != "" {
		prompt += "\nCurrent workspace root: " + workspaceRoot
	}
	out := make([]chatMessage, 0, len(messages)+1)
	out = append(out, chatMessage{Role: "system", Content: prompt})
	out = append(out, messages...)
	return out
}

func loadAgentSystemPrompt(mode cursorAgentMode) string {
	mode = defaultCursorAgentMode(mode)
	agentSystemPromptsMu.Lock()
	if agentSystemPromptsByMode != nil {
		if prompt, ok := agentSystemPromptsByMode[mode]; ok {
			agentSystemPromptsMu.Unlock()
			return prompt
		}
	}
	agentSystemPromptsMu.Unlock()

	prompt, err := readAgentSystemPromptFromDisk(mode)
	if err != nil {
		log.Printf("[Gateway] %s system prompt fallback: %v", mode.displayName(), err)
		prompt = ""
	}

	agentSystemPromptsMu.Lock()
	if agentSystemPromptsByMode == nil {
		agentSystemPromptsByMode = map[cursorAgentMode]string{}
	}
	agentSystemPromptsByMode[mode] = prompt
	agentSystemPromptsMu.Unlock()
	return prompt
}

func readAgentSystemPromptFromDisk(mode cursorAgentMode) (string, error) {
	path := findAgentModeFilePath(mode, "system_prompt.txt")
	if path == "" {
		return "", fmt.Errorf("%s system_prompt.txt not found", defaultCursorAgentMode(mode).displayName())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	parts := []string{strings.TrimSpace(string(data))}
	if defaultCursorAgentMode(mode) == cursorAgentModePlan {
		if reminderPath := findAgentModeFilePath(mode, "system_reminder.txt"); reminderPath != "" {
			if reminder, err := os.ReadFile(reminderPath); err == nil && len(strings.TrimSpace(string(reminder))) > 0 {
				parts = append(parts, strings.TrimSpace(string(reminder)))
			}
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

func agentLocalSystemReminder(mode cursorAgentMode, workspaceRoot string) string {
	base := "Local BYOK proxy notes: use available local tools to inspect Cursor workspace context instead of guessing. When the user refers to the current/open file, use the visible_files or selected context and read that path before answering. Do not repeatedly read the same file range. Some Cursor UI-only tools may return an unavailable result from the local proxy; continue with available evidence instead of repeating the same unsupported call."
	switch defaultCursorAgentMode(mode) {
	case cursorAgentModeAsk:
		return base + " Ask mode is read-only: answer questions and inspect context, but do not edit files or run mutating commands."
	case cursorAgentModePlan:
		return base + " Plan mode is read-only: produce a concrete plan and do not edit files until the user executes the plan."
	default:
		return base + " Agent mode may modify files when the user asks for implementation, fixes, refactors, or optimization. For code-change requests, make the change with StrReplace for targeted edits or Write for whole-file/new-file changes before giving the final answer."
	}
}

func (g *Gateway) streamAgentProviderTurn(ctx context.Context, writer io.Writer, req unifiedChatRequest, adapter *ModelAdapter, endpoint string) (agentProviderTurnResult, error) {
	return g.streamAgentProviderTurnWithTimeout(ctx, writer, req, adapter, endpoint, agentProviderTurnTimeout)
}

func (g *Gateway) streamAgentProviderTurnWithTimeout(ctx context.Context, writer io.Writer, req unifiedChatRequest, adapter *ModelAdapter, endpoint string, timeout time.Duration) (agentProviderTurnResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = agentProviderTurnTimeout
	}
	turnCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	apiURL := adapter.APIURL()
	providerReq := buildProviderRequest(req, adapter, endpoint)
	toolsCount := 0
	if tools, ok := providerReq["tools"].([]any); ok {
		toolsCount = len(tools)
	}
	log.Printf("[Gateway] Agent provider turn start model=%s provider=%s mode=%s api=%s messages=%d tools=%d workspace=%q", req.ModelName, adapter.Type, defaultCursorAgentMode(req.AgentMode).displayName(), safeLogURL(apiURL), len(req.Messages), toolsCount, req.WorkspaceRoot)
	if err := adapter.ApplyExtraParams(providerReq); err != nil {
		return agentProviderTurnResult{}, err
	}
	body, err := json.Marshal(providerReq)
	if err != nil {
		return agentProviderTurnResult{}, err
	}
	log.Printf("[Gateway] Agent provider request body bytes=%d", len(body))

	apiReq, err := http.NewRequestWithContext(turnCtx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return agentProviderTurnResult{}, err
	}
	apiReq.Header.Set("Content-Type", "application/json")
	if adapter.Type == "anthropic" {
		apiReq.Header.Set("x-api-key", adapter.APIKey)
		apiReq.Header.Set("anthropic-version", "2023-06-01")
	} else {
		apiReq.Header.Set("Authorization", "Bearer "+adapter.APIKey)
	}

	client := &http.Client{Timeout: timeout + 5*time.Second}
	resp, err := client.Do(apiReq)
	if err != nil {
		if turnCtx.Err() != nil {
			return agentProviderTurnResult{}, fmt.Errorf("provider request timed out: %w", turnCtx.Err())
		}
		return agentProviderTurnResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return agentProviderTurnResult{}, fmt.Errorf("upstream returned %d: %s", resp.StatusCode, string(data))
	}

	parser := newAgentProviderStreamParser(adapter, endpoint)
	result := agentProviderTurnResult{}
	startedAt := time.Now()
	dataEvents := 0
	textChars := 0
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 4*1024*1024)
	eventName := ""
	for scanner.Scan() {
		if err := turnCtx.Err(); err != nil {
			return result, fmt.Errorf("provider stream timed out: %w", err)
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		dataEvents++
		text, done, usage, toolCalls := parser.Process(eventName, data)
		if usage.PromptTokens > 0 {
			result.PromptTokens = usage.PromptTokens
		}
		if usage.CompletionTokens > 0 {
			result.CompletionTokens = usage.CompletionTokens
		}
		if usage.CacheReadTokens > 0 {
			result.CacheReadTokens = usage.CacheReadTokens
		}
		if usage.CacheWriteTokens > 0 {
			result.CacheWriteTokens = usage.CacheWriteTokens
		}
		if text != "" {
			result.Text += text
			textChars += len(text)
			result.TextChars += len(text)
			if err := writeAgentServerFrame(writer, text); err != nil {
				return result, err
			}
			tokens := estimateTokens(text)
			if tokens <= 0 {
				tokens = 1
			}
			if err := writeAgentTokenDeltaFrame(writer, tokens); err != nil {
				return result, err
			}
			result.TokenDeltaSent += tokens
		}
		if len(toolCalls) > 0 {
			log.Printf("[Gateway] Agent provider emitted %d tool call(s): %s", len(toolCalls), agentToolCallNames(toolCalls))
			result.ToolCalls = append(result.ToolCalls, toolCalls...)
		}
		if done {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		if turnCtx.Err() != nil {
			return result, fmt.Errorf("provider stream timed out: %w", turnCtx.Err())
		}
		return result, err
	}
	if pending := parser.PendingToolCalls(); len(pending) > 0 {
		log.Printf("[Gateway] Agent provider pending %d tool call(s): %s", len(pending), agentToolCallNames(pending))
		result.ToolCalls = append(result.ToolCalls, pending...)
	}
	if missingTokens := missingAgentUsageTokenDelta(result.CompletionTokens, result.TokenDeltaSent); missingTokens > 0 {
		if err := writeAgentTokenDeltaFrame(writer, missingTokens); err != nil {
			return result, err
		}
		result.TokenDeltaSent += missingTokens
		result.UsageTokenDeltaSent = missingTokens
		log.Printf("[Gateway] Agent provider emitted usage token delta model=%s tokens=%d completionTokens=%d textTokenDelta=%d", req.ModelName, missingTokens, result.CompletionTokens, result.TokenDeltaSent-missingTokens)
	}
	log.Printf("[Gateway] Agent provider turn done model=%s events=%d textChars=%d toolCalls=%d promptTokens=%d completionTokens=%d cacheRead=%d cacheWrite=%d durationMs=%d", req.ModelName, dataEvents, textChars, len(result.ToolCalls), result.PromptTokens, result.CompletionTokens, result.CacheReadTokens, result.CacheWriteTokens, time.Since(startedAt).Milliseconds())
	return result, nil
}

func missingAgentUsageTokenDelta(completionTokens int, tokenDeltaSent int) int {
	if completionTokens <= tokenDeltaSent {
		return 0
	}
	return completionTokens - tokenDeltaSent
}

type agentProviderStreamParser struct {
	provider string
	endpoint string

	responsesCalls map[string]*agentToolCall
	chatCalls      map[int]*agentToolCall
	anthropicCalls map[int]*agentToolCall
	completed      map[string]bool
}

func newAgentProviderStreamParser(adapter *ModelAdapter, endpoint string) *agentProviderStreamParser {
	provider := ""
	if adapter != nil {
		provider = adapter.Type
	}
	return &agentProviderStreamParser{
		provider:       strings.ToLower(provider),
		endpoint:       endpoint,
		responsesCalls: map[string]*agentToolCall{},
		chatCalls:      map[int]*agentToolCall{},
		anthropicCalls: map[int]*agentToolCall{},
		completed:      map[string]bool{},
	}
}

func (p *agentProviderStreamParser) Process(eventName string, data string) (string, bool, streamUsage, []agentToolCall) {
	if p.provider == "anthropic" {
		return p.processAnthropic(eventName, data)
	}
	if strings.Contains(strings.ToLower(p.endpoint), "responses") {
		return p.processOpenAIResponses(eventName, data)
	}
	return p.processOpenAIChat(data)
}

func (p *agentProviderStreamParser) PendingToolCalls() []agentToolCall {
	var out []agentToolCall
	for _, call := range p.responsesCalls {
		if call != nil && !p.completed[callKey(*call)] && call.Name != "" {
			out = append(out, p.completeCall(*call))
		}
	}
	for _, call := range p.chatCalls {
		if call != nil && !p.completed[callKey(*call)] && call.Name != "" {
			out = append(out, p.completeCall(*call))
		}
	}
	for _, call := range p.anthropicCalls {
		if call != nil && !p.completed[callKey(*call)] && call.Name != "" {
			out = append(out, p.completeCall(*call))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (p *agentProviderStreamParser) processOpenAIResponses(eventName string, data string) (string, bool, streamUsage, []agentToolCall) {
	var event struct {
		Type        string `json:"type"`
		Delta       string `json:"delta"`
		ItemID      string `json:"item_id"`
		OutputIndex int    `json:"output_index"`
		Item        struct {
			ID        string `json:"id"`
			Type      string `json:"type"`
			Name      string `json:"name"`
			CallID    string `json:"call_id"`
			Arguments string `json:"arguments"`
		} `json:"item"`
		Response struct {
			Usage struct {
				InputTokens        int `json:"input_tokens"`
				OutputTokens       int `json:"output_tokens"`
				InputTokensDetails struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"input_tokens_details"`
			} `json:"usage"`
		} `json:"response"`
	}
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return "", false, streamUsage{}, nil
	}
	typeName := event.Type
	if typeName == "" {
		typeName = eventName
	}
	usage := streamUsage{PromptTokens: event.Response.Usage.InputTokens, CompletionTokens: event.Response.Usage.OutputTokens, CacheReadTokens: event.Response.Usage.InputTokensDetails.CachedTokens}
	switch typeName {
	case "response.output_text.delta", "response.refusal.delta":
		return event.Delta, false, usage, nil
	case "response.output_item.added":
		if event.Item.Type == "function_call" {
			call := &agentToolCall{ID: firstNonEmpty(event.Item.CallID, event.Item.ID), Name: canonicalAgentToolDefinitionName(event.Item.Name), Arguments: event.Item.Arguments}
			p.responsesCalls[p.responsesKey(event.ItemID, event.Item.ID, event.OutputIndex)] = call
		}
	case "response.function_call_arguments.delta":
		if call := p.responsesCalls[p.responsesKey(event.ItemID, "", event.OutputIndex)]; call != nil {
			call.Arguments += event.Delta
		}
	case "response.function_call_arguments.done":
		if call := p.responsesCalls[p.responsesKey(event.ItemID, "", event.OutputIndex)]; call != nil && event.Delta != "" {
			call.Arguments = event.Delta
		}
	case "response.output_item.done":
		if event.Item.Type == "function_call" {
			call := &agentToolCall{ID: firstNonEmpty(event.Item.CallID, event.Item.ID), Name: canonicalAgentToolDefinitionName(event.Item.Name), Arguments: event.Item.Arguments}
			return "", false, usage, []agentToolCall{p.completeCall(*call)}
		}
	case "response.completed", "response.done":
		return "", true, usage, nil
	}
	return "", false, usage, nil
}

func (p *agentProviderStreamParser) responsesKey(itemID string, fallbackID string, index int) string {
	if itemID != "" {
		return itemID
	}
	if fallbackID != "" {
		return fallbackID
	}
	return fmt.Sprintf("index:%d", index)
}

func (p *agentProviderStreamParser) processOpenAIChat(data string) (string, bool, streamUsage, []agentToolCall) {
	var event struct {
		Choices []struct {
			Delta struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					Index    int    `json:"index"`
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens        int `json:"prompt_tokens"`
			CompletionTokens    int `json:"completion_tokens"`
			PromptTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return "", false, streamUsage{}, nil
	}
	usage := streamUsage{PromptTokens: event.Usage.PromptTokens, CompletionTokens: event.Usage.CompletionTokens, CacheReadTokens: event.Usage.PromptTokensDetails.CachedTokens}
	if len(event.Choices) == 0 {
		return "", false, usage, nil
	}
	choice := event.Choices[0]
	for _, delta := range choice.Delta.ToolCalls {
		call := p.chatCalls[delta.Index]
		if call == nil {
			call = &agentToolCall{ID: delta.ID}
			p.chatCalls[delta.Index] = call
		}
		if delta.ID != "" {
			call.ID = delta.ID
		}
		if delta.Function.Name != "" {
			call.Name = canonicalAgentToolDefinitionName(delta.Function.Name)
		}
		call.Arguments += delta.Function.Arguments
	}
	if choice.FinishReason == "tool_calls" {
		return choice.Delta.Content, false, usage, p.PendingToolCalls()
	}
	return choice.Delta.Content, choice.FinishReason == "stop", usage, nil
}

func (p *agentProviderStreamParser) processAnthropic(eventName string, data string) (string, bool, streamUsage, []agentToolCall) {
	var event struct {
		Type         string `json:"type"`
		Index        int    `json:"index"`
		ContentBlock struct {
			Type  string         `json:"type"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content_block"`
		Delta struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return "", false, streamUsage{}, nil
	}
	typeName := event.Type
	if typeName == "" {
		typeName = eventName
	}
	usage := streamUsage{PromptTokens: event.Usage.InputTokens, CompletionTokens: event.Usage.OutputTokens, CacheReadTokens: event.Usage.CacheReadInputTokens, CacheWriteTokens: event.Usage.CacheCreationInputTokens}
	switch typeName {
	case "content_block_start":
		if event.ContentBlock.Type == "tool_use" {
			args := ""
			if event.ContentBlock.Input != nil {
				if data, err := json.Marshal(event.ContentBlock.Input); err == nil {
					args = string(data)
				}
			}
			p.anthropicCalls[event.Index] = &agentToolCall{ID: event.ContentBlock.ID, Name: canonicalAgentToolDefinitionName(event.ContentBlock.Name), Arguments: args}
		}
	case "content_block_delta":
		if event.Delta.Type == "text_delta" {
			return event.Delta.Text, false, usage, nil
		}
		if event.Delta.Type == "input_json_delta" {
			if call := p.anthropicCalls[event.Index]; call != nil {
				call.Arguments += event.Delta.PartialJSON
			}
		}
	case "content_block_stop":
		if call := p.anthropicCalls[event.Index]; call != nil {
			return "", false, usage, []agentToolCall{p.completeCall(*call)}
		}
	case "message_stop":
		return "", true, usage, nil
	}
	return "", false, usage, nil
}

func (p *agentProviderStreamParser) completeCall(call agentToolCall) agentToolCall {
	if call.ID == "" {
		call.ID = "byok_tool_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	call.Name = canonicalAgentToolDefinitionName(call.Name)
	p.completed[callKey(call)] = true
	return call
}

func callKey(call agentToolCall) string {
	if call.ID != "" {
		return call.ID
	}
	return call.Name + ":" + call.Arguments
}

func (g *Gateway) executeAgentTool(ctx context.Context, writer io.Writer, call agentToolCall, workspaceRoot string) agentToolExecution {
	name := normalizeAgentToolName(call.Name)
	args := parseToolArgs(call.Arguments)
	if call.ID != "" {
		args["tool_call_id"] = call.ID
	}
	args = normalizeAgentToolArgs(name, args, workspaceRoot)
	call.Name = name
	started := agentToolCallProto(name, args, nil)
	resultText := "unsupported tool: " + name
	var completed *agentv1.ToolCall
	source := "local"
	log.Printf("[Gateway] Agent tool start name=%s callID=%s args=%s workspace=%q", name, call.ID, truncateForLog(formatAgentToolArgs(args), 800), workspaceRoot)

	switch name {
	case "Read":
		if execResult := g.executeCursorTool(ctx, writer, call, args, workspaceRoot); execResult.OK {
			source = "cursor-exec"
			resultText, completed = completedReadFromExec(call, args, execResult.Message, workspaceRoot)
		} else {
			log.Printf("[Gateway] Agent tool cursor exec fallback name=%s callID=%s error=%v", name, call.ID, execResult.Error)
			resultText, completed = g.executeReadTool(args, workspaceRoot)
		}
	case "Grep":
		if execResult := g.executeCursorTool(ctx, writer, call, args, workspaceRoot); execResult.OK {
			source = "cursor-exec"
			resultText, completed = completedGrepFromExec(call, args, execResult.Message, workspaceRoot)
		} else {
			log.Printf("[Gateway] Agent tool cursor exec fallback name=%s callID=%s error=%v", name, call.ID, execResult.Error)
			resultText, completed = executeGrepTool(args, workspaceRoot)
		}
	case "Ls":
		if execResult := g.executeCursorTool(ctx, writer, call, args, workspaceRoot); execResult.OK {
			source = "cursor-exec"
			resultText, completed = completedLsFromExec(call, args, execResult.Message, workspaceRoot)
		} else {
			log.Printf("[Gateway] Agent tool cursor exec fallback name=%s callID=%s error=%v", name, call.ID, execResult.Error)
			resultText, completed = executeLsTool(args, workspaceRoot)
		}
	case "Glob":
		resultText, completed = executeGlobTool(args, workspaceRoot)
	case "Shell":
		if execResult := g.executeCursorTool(ctx, writer, call, args, workspaceRoot); execResult.OK {
			source = "cursor-exec"
			resultText, completed = completedShellFromExec(call, args, execResult.Message, workspaceRoot)
		} else {
			log.Printf("[Gateway] Agent tool cursor exec fallback name=%s callID=%s error=%v", name, call.ID, execResult.Error)
			resultText, completed = executeShellTool(args, workspaceRoot)
		}
	case "ForceBackgroundShell":
		if execResult := g.executeCursorTool(ctx, writer, call, args, workspaceRoot); execResult.OK {
			source = "cursor-exec"
			resultText, completed = completedBackgroundShellFromExec(call, args, execResult.Message, workspaceRoot)
		} else {
			log.Printf("[Gateway] Agent tool cursor exec fallback name=%s callID=%s error=%v", name, call.ID, execResult.Error)
			resultText, completed = executeShellTool(args, workspaceRoot)
		}
	case "WriteShellStdin":
		if execResult := g.executeCursorTool(ctx, writer, call, args, workspaceRoot); execResult.OK {
			source = "cursor-exec"
			resultText, completed = completedWriteShellStdinFromExec(call, args, execResult.Message)
		} else {
			resultText = "background shell stdin is unavailable: " + execResult.Error.Error()
			completed = agentToolCallProto(name, args, resultText)
		}
	case "PatchEdit":
		resultText, completed = g.executePatchEditTool(args, workspaceRoot)
	case "Write":
		if execResult := g.executeCursorTool(ctx, writer, call, args, workspaceRoot); execResult.OK {
			source = "cursor-exec"
			resultText, completed = completedWriteFromExec(call, args, execResult.Message, workspaceRoot)
		} else {
			log.Printf("[Gateway] Agent tool cursor exec fallback name=%s callID=%s error=%v", name, call.ID, execResult.Error)
			resultText, completed = g.executeWriteTool(args, workspaceRoot)
		}
	case "Delete":
		if execResult := g.executeCursorTool(ctx, writer, call, args, workspaceRoot); execResult.OK {
			source = "cursor-exec"
			resultText, completed = completedDeleteFromExec(call, args, execResult.Message, workspaceRoot)
		} else {
			log.Printf("[Gateway] Agent tool cursor exec fallback name=%s callID=%s error=%v", name, call.ID, execResult.Error)
			resultText, completed = executeDeleteTool(args, workspaceRoot)
		}
	case "ReadLints":
		if execResult := g.executeCursorTool(ctx, writer, call, args, workspaceRoot); execResult.OK {
			source = "cursor-exec"
			resultText, completed = completedReadLintsFromExec(call, args, execResult.Message, workspaceRoot)
		} else {
			log.Printf("[Gateway] Agent tool cursor exec fallback name=%s callID=%s error=%v", name, call.ID, execResult.Error)
			resultText, completed = executeReadLintsTool(args, workspaceRoot)
		}
	case "WebFetch":
		if execResult := g.executeCursorTool(ctx, writer, call, args, workspaceRoot); execResult.OK {
			source = "cursor-exec"
			resultText, completed = completedWebFetchFromExec(call, args, execResult.Message)
		} else {
			log.Printf("[Gateway] Agent tool cursor exec fallback name=%s callID=%s error=%v", name, call.ID, execResult.Error)
			resultText, completed = executeWebFetchTool(args)
		}
	case "TodoWrite":
		resultText, completed = executeTodoWriteTool(args)
	case "CreatePlan":
		resultText, completed = executeCreatePlanTool(args, workspaceRoot)
	case "AskQuestion", "CallMcpTool", "FetchMcpResource", "SwitchMode", "Task", "WebSearch":
		resultText, completed = executeUnavailableAgentTool(name, args)
	default:
		completed = agentToolCallProto(name, args, resultText)
	}
	log.Printf("[Gateway] Agent tool done name=%s callID=%s source=%s resultChars=%d", name, call.ID, source, len(resultText))

	return agentToolExecution{
		Call:          call,
		Args:          args,
		ResultText:    resultText,
		StartedCall:   started,
		CompletedCall: completed,
	}
}

func parseToolArgs(raw string) map[string]any {
	args := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return args
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		args["_raw"] = raw
	}
	return args
}

func appendAgentToolResultMessage(messages []chatMessage, exec agentToolExecution) []chatMessage {
	output := truncateTextForModel(exec.ResultText, agentToolResultLimit)
	return append(messages, chatMessage{ToolResult: &chatToolResult{
		ID:        exec.Call.ID,
		Name:      providerVisibleAgentToolName(exec.Call.Name),
		Arguments: formatAgentToolArgs(exec.Args),
		Output:    output,
	}})
}

func appendAgentFinalAnswerInstruction(messages []chatMessage) []chatMessage {
	return append(messages, chatMessage{Role: "user", Content: "Use the tool results above to answer the user's request now in Chinese. Do not call any more tools. Summarize the project clearly and concisely based only on the collected results."})
}

func appendAgentEditRequiredInstruction(messages []chatMessage) []chatMessage {
	return append(messages, chatMessage{Role: "user", Content: "The user asked for a code change, but no edit/write tool has completed yet. Continue the task now: use StrReplace for targeted edits or Write only when replacing/creating a whole file. Do not provide a final answer until you have made the required file change or a tool reports a concrete edit failure."})
}

func agentRequestNeedsEdit(messages []chatMessage) bool {
	content := strings.ToLower(lastUserMessageContent(messages))
	if strings.TrimSpace(content) == "" {
		return false
	}
	keywords := []string{
		"优化", "修改", "改一下", "改成", "修复", "解决", "实现", "添加", "新增", "删除", "重构", "调整", "完善", "更新", "替换", "改代码", "报错", "bug",
		"optimize", "modify", "change", "fix", "repair", "implement", "add", "remove", "delete", "refactor", "update", "replace", "edit", "write code", "bug",
	}
	for _, keyword := range keywords {
		if strings.Contains(content, keyword) {
			return true
		}
	}
	return false
}

func lastUserMessageContent(messages []chatMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].ToolResult == nil && messages[i].Role == "user" {
			return messages[i].Content
		}
	}
	return ""
}

func agentToolChangesFiles(name string) bool {
	switch normalizeAgentToolName(name) {
	case "PatchEdit", "Write", "Delete":
		return true
	default:
		return false
	}
}

func agentToolResultLooksSuccessfulEdit(exec agentToolExecution) bool {
	if !agentToolChangesFiles(exec.Call.Name) {
		return false
	}
	text := strings.ToLower(exec.ResultText)
	failureMarkers := []string{
		"error", "failed", "rejected", "denied", "not found", "must differ", "required", "matched ", "unavailable", "permission", "cannot", "can't",
		"失败", "错误", "拒绝", "未找到", "权限", "不可用",
	}
	for _, marker := range failureMarkers {
		if strings.Contains(text, marker) {
			return false
		}
	}
	return strings.TrimSpace(exec.ResultText) != ""
}

func truncateToolResultForModel(text string) string {
	return truncateTextForModel(text, agentToolResultLimit)
}

func truncateTextForModel(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "\n...[tool result truncated]"
}

func compactAgentMessagesForFinal(messages []chatMessage) []chatMessage {
	out := make([]chatMessage, 0, len(messages))
	for _, msg := range messages {
		if msg.ToolResult == nil {
			out = append(out, msg)
			continue
		}
		toolResult := *msg.ToolResult
		toolResult.Output = truncateTextForModel(toolResult.Output, agentFinalToolResultLimit)
		msg.ToolResult = &toolResult
		out = append(out, msg)
	}
	return out
}

func (g *Gateway) executeCursorTool(ctx context.Context, writer io.Writer, call agentToolCall, args map[string]any, workspaceRoot string) agentExecResult {
	execSeq, execID, execMsg, err := g.cursorExecServerMessage(call, args, workspaceRoot)
	if err != nil {
		return agentExecResult{Error: err}
	}
	log.Printf("[Gateway] Agent cursor exec start id=%d execID=%s tool=%s callID=%s", execSeq, execID, call.Name, call.ID)
	ch, keys := g.registerAgentExec(execSeq, execID)
	defer g.unregisterAgentExec(keys)
	if err := writeAgentExecServerFrame(writer, execMsg); err != nil {
		return agentExecResult{Error: err}
	}
	log.Printf("[Gateway] Agent cursor exec sent id=%d execID=%s tool=%s", execSeq, execID, call.Name)
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	select {
	case msg := <-ch:
		if msg == nil {
			return agentExecResult{Error: fmt.Errorf("empty exec result")}
		}
		log.Printf("[Gateway] Agent cursor exec result id=%d execID=%s kind=%s", msg.GetId(), msg.GetExecId(), agentExecClientMessageKind(msg))
		return agentExecResult{Message: msg, OK: true}
	case <-ctx.Done():
		log.Printf("[Gateway] Agent cursor exec timeout id=%d execID=%s tool=%s error=%v", execSeq, execID, call.Name, ctx.Err())
		return agentExecResult{Error: ctx.Err()}
	}
}

func (g *Gateway) cursorExecServerMessage(call agentToolCall, args map[string]any, workspaceRoot string) (uint32, string, *agentv1.ExecServerMessage, error) {
	name := normalizeAgentToolName(call.Name)
	g.mu.Lock()
	g.agentExecSeq++
	id := g.agentExecSeq
	g.mu.Unlock()
	execID := "byok_exec_" + call.ID
	if call.ID == "" {
		execID = fmt.Sprintf("byok_exec_%d", id)
	}
	msg := &agentv1.ExecServerMessage{Id: id, ExecId: execID}
	switch name {
	case "Read":
		resolved, err := resolveToolPath(argString(args, "path"), workspaceRoot)
		if err != nil {
			return 0, "", nil, err
		}
		msg.Message = &agentv1.ExecServerMessage_ReadArgs{ReadArgs: &agentv1.ReadArgs{Path: resolved, ToolCallId: call.ID}}
	case "Grep":
		resolved, err := resolveToolPath(firstNonEmpty(argString(args, "path"), workspaceRoot), workspaceRoot)
		if err != nil {
			return 0, "", nil, err
		}
		grepArgs := grepToolArgsFromMap(args)
		grepArgs.Path = proto.String(resolved)
		grepArgs.ToolCallId = call.ID
		msg.Message = &agentv1.ExecServerMessage_GrepArgs{GrepArgs: grepArgs}
	case "Ls":
		resolved, err := resolveToolPath(firstNonEmpty(argString(args, "path"), argString(args, "target_directory"), workspaceRoot), workspaceRoot)
		if err != nil {
			return 0, "", nil, err
		}
		msg.Message = &agentv1.ExecServerMessage_LsArgs{LsArgs: &agentv1.LsArgs{Path: resolved, ToolCallId: call.ID, Ignore: defaultAgentLsIgnore(), TimeoutMs: proto.Uint32(10000)}}
	case "Shell":
		workingDir, err := resolveToolPath(firstNonEmpty(argString(args, "working_directory"), argString(args, "cwd"), workspaceRoot), workspaceRoot)
		if err != nil {
			return 0, "", nil, err
		}
		command := argString(args, "command")
		if command == "" {
			return 0, "", nil, fmt.Errorf("command is required")
		}
		if err := validateShellCommand(command); err != nil {
			return 0, "", nil, err
		}
		timeout := int32(argInt(args, "block_until_ms", 30000))
		if timeout <= 0 || timeout > 120000 {
			timeout = 30000
		}
		msg.Message = &agentv1.ExecServerMessage_ShellArgs{ShellArgs: &agentv1.ShellArgs{Command: command, WorkingDirectory: workingDir, Timeout: timeout, ToolCallId: call.ID, TimeoutBehavior: agentv1.TimeoutBehavior_TIMEOUT_BEHAVIOR_CANCEL}}
	case "ForceBackgroundShell":
		workingDir, err := resolveToolPath(firstNonEmpty(argString(args, "working_directory"), argString(args, "cwd"), workspaceRoot), workspaceRoot)
		if err != nil {
			return 0, "", nil, err
		}
		command := argString(args, "command")
		if command == "" {
			return 0, "", nil, fmt.Errorf("command is required")
		}
		if err := validateShellCommand(command); err != nil {
			return 0, "", nil, err
		}
		msg.Message = &agentv1.ExecServerMessage_BackgroundShellSpawnArgs{BackgroundShellSpawnArgs: &agentv1.BackgroundShellSpawnArgs{Command: command, WorkingDirectory: workingDir, ToolCallId: call.ID, EnableWriteShellStdinTool: true}}
	case "WriteShellStdin":
		msg.Message = &agentv1.ExecServerMessage_WriteShellStdinArgs{WriteShellStdinArgs: writeShellStdinArgsFromMap(args)}
	case "Write":
		resolved, err := resolveToolPath(argString(args, "path"), workspaceRoot)
		if err != nil {
			return 0, "", nil, err
		}
		msg.Message = &agentv1.ExecServerMessage_WriteArgs{WriteArgs: &agentv1.WriteArgs{Path: resolved, FileText: firstNonEmpty(argString(args, "contents"), argString(args, "file_text"), argString(args, "content")), ToolCallId: call.ID, ReturnFileContentAfterWrite: argBool(args, "return_file_content_after_write", true)}}
	case "Delete":
		resolved, err := resolveToolPath(argString(args, "path"), workspaceRoot)
		if err != nil {
			return 0, "", nil, err
		}
		msg.Message = &agentv1.ExecServerMessage_DeleteArgs{DeleteArgs: &agentv1.DeleteArgs{Path: resolved, ToolCallId: call.ID}}
	case "ReadLints":
		paths := readLintsArgsFromMap(args, workspaceRoot).Paths
		path := workspaceRoot
		if len(paths) > 0 {
			path = paths[0]
		}
		resolved, err := resolveToolPath(path, workspaceRoot)
		if err != nil {
			return 0, "", nil, err
		}
		msg.Message = &agentv1.ExecServerMessage_DiagnosticsArgs{DiagnosticsArgs: &agentv1.DiagnosticsArgs{Path: resolved, ToolCallId: call.ID}}
	case "WebFetch":
		urlValue := argString(args, "url")
		if urlValue == "" {
			return 0, "", nil, fmt.Errorf("url is required")
		}
		msg.Message = &agentv1.ExecServerMessage_FetchArgs{FetchArgs: &agentv1.FetchArgs{Url: urlValue, ToolCallId: call.ID}}
	default:
		return 0, "", nil, fmt.Errorf("tool %s cannot use cursor exec", name)
	}
	return id, execID, msg, nil
}

func (g *Gateway) registerAgentExec(id uint32, execID string) (chan *agentv1.ExecClientMessage, []string) {
	ch := make(chan *agentv1.ExecClientMessage, 1)
	keys := []string{}
	if id > 0 {
		keys = append(keys, agentExecIDKey(id))
	}
	if execID != "" {
		keys = append(keys, agentExecStringKey(execID))
	}
	g.mu.Lock()
	if g.agentExecs == nil {
		g.agentExecs = make(map[string]chan *agentv1.ExecClientMessage)
	}
	for _, key := range keys {
		g.agentExecs[key] = ch
	}
	g.mu.Unlock()
	return ch, keys
}

func (g *Gateway) unregisterAgentExec(keys []string) {
	g.mu.Lock()
	for _, key := range keys {
		delete(g.agentExecs, key)
	}
	g.mu.Unlock()
}

func (g *Gateway) completeAgentExec(msg *agentv1.ExecClientMessage) {
	if msg == nil {
		return
	}
	execID := msg.GetExecId()
	msgID := msg.GetId()
	match := ""
	g.mu.RLock()
	var ch chan *agentv1.ExecClientMessage
	if execID != "" {
		match = agentExecStringKey(execID)
		ch = g.agentExecs[match]
	}
	if ch == nil && msgID > 0 {
		match = agentExecIDKey(msgID)
		ch = g.agentExecs[match]
	}
	if ch != nil {
		// matched by id or exec_id
	}
	if ch == nil {
		for key, candidate := range g.agentExecs {
			match = key
			ch = candidate
			break
		}
	}
	g.mu.RUnlock()
	if ch == nil {
		log.Printf("[Gateway] Agent cursor exec client message ignored id=%d execID=%s kind=%s", msgID, execID, agentExecClientMessageKind(msg))
		return
	}
	log.Printf("[Gateway] Agent cursor exec client message id=%d execID=%s kind=%s match=%s", msgID, execID, agentExecClientMessageKind(msg), match)
	select {
	case ch <- msg:
	default:
	}
}

func agentExecIDKey(id uint32) string {
	return fmt.Sprintf("id:%d", id)
}

func agentExecStringKey(execID string) string {
	return "exec:" + execID
}

func completedReadFromExec(call agentToolCall, args map[string]any, msg *agentv1.ExecClientMessage, workspaceRoot string) (string, *agentv1.ToolCall) {
	toolArgs := readToolArgsFromMap(args)
	if resolved, err := resolveToolPath(toolArgs.Path, workspaceRoot); err == nil {
		toolArgs.Path = resolved
	}
	readResult := msg.GetReadResult()
	if readResult == nil {
		text := execClientMessageText(msg)
		result := &agentv1.ReadToolResult{Result: &agentv1.ReadToolResult_Error{Error: &agentv1.ReadToolError{ErrorMessage: text}}}
		return text, &agentv1.ToolCall{Tool: &agentv1.ToolCall_ReadToolCall{ReadToolCall: &agentv1.ReadToolCall{Args: toolArgs, Result: result}}}
	}
	if success := readResult.GetSuccess(); success != nil {
		content := success.GetContent()
		if content == "" && len(success.GetData()) > 0 {
			content = string(success.GetData())
		}
		if content == "" {
			content = "read completed"
		}
		toolSuccess := &agentv1.ReadToolSuccess{Path: success.GetPath(), TotalLines: uint32(success.GetTotalLines()), FileSize: uint32(success.GetFileSize()), ExceededLimit: success.GetTruncated(), Output: &agentv1.ReadToolSuccess_Content{Content: content}}
		result := &agentv1.ReadToolResult{Result: &agentv1.ReadToolResult_Success{Success: toolSuccess}}
		return content, &agentv1.ToolCall{Tool: &agentv1.ToolCall_ReadToolCall{ReadToolCall: &agentv1.ReadToolCall{Args: toolArgs, Result: result}}}
	}
	text := execClientMessageText(msg)
	result := &agentv1.ReadToolResult{Result: &agentv1.ReadToolResult_Error{Error: &agentv1.ReadToolError{ErrorMessage: text}}}
	return text, &agentv1.ToolCall{Tool: &agentv1.ToolCall_ReadToolCall{ReadToolCall: &agentv1.ReadToolCall{Args: toolArgs, Result: result}}}
}

func completedGrepFromExec(call agentToolCall, args map[string]any, msg *agentv1.ExecClientMessage, workspaceRoot string) (string, *agentv1.ToolCall) {
	toolArgs := grepToolArgsFromMap(args)
	if resolved, err := resolveToolPath(firstNonEmpty(toolArgs.GetPath(), workspaceRoot), workspaceRoot); err == nil {
		toolArgs.Path = proto.String(resolved)
	}
	result := msg.GetGrepResult()
	if result == nil {
		text := execClientMessageText(msg)
		grepResult := &agentv1.GrepResult{Result: &agentv1.GrepResult_Error{Error: &agentv1.GrepError{Error: text}}}
		return text, &agentv1.ToolCall{Tool: &agentv1.ToolCall_GrepToolCall{GrepToolCall: &agentv1.GrepToolCall{Args: toolArgs, Result: grepResult}}}
	}
	text := grepResultText(result)
	return text, &agentv1.ToolCall{Tool: &agentv1.ToolCall_GrepToolCall{GrepToolCall: &agentv1.GrepToolCall{Args: toolArgs, Result: result}}}
}

func completedLsFromExec(call agentToolCall, args map[string]any, msg *agentv1.ExecClientMessage, workspaceRoot string) (string, *agentv1.ToolCall) {
	toolArgs := lsToolArgsFromMap(args, workspaceRoot)
	result := msg.GetLsResult()
	if result == nil {
		text := execClientMessageText(msg)
		result = &agentv1.LsResult{Result: &agentv1.LsResult_Error{Error: &agentv1.LsError{Path: toolArgs.Path, Error: text}}}
	}
	return lsResultText(result), &agentv1.ToolCall{Tool: &agentv1.ToolCall_LsToolCall{LsToolCall: &agentv1.LsToolCall{Args: toolArgs, Result: result}}}
}

func completedShellFromExec(call agentToolCall, args map[string]any, msg *agentv1.ExecClientMessage, workspaceRoot string) (string, *agentv1.ToolCall) {
	workingDir, _ := resolveToolPath(firstNonEmpty(argString(args, "working_directory"), argString(args, "cwd"), workspaceRoot), workspaceRoot)
	toolArgs := &agentv1.ShellArgs{Command: argString(args, "command"), WorkingDirectory: workingDir, Timeout: int32(argInt(args, "block_until_ms", 30000)), ToolCallId: call.ID, TimeoutBehavior: agentv1.TimeoutBehavior_TIMEOUT_BEHAVIOR_CANCEL}
	result := msg.GetShellResult()
	if result == nil {
		text := execClientMessageText(msg)
		result = &agentv1.ShellResult{Result: &agentv1.ShellResult_Failure{Failure: &agentv1.ShellFailure{Command: toolArgs.Command, WorkingDirectory: workingDir, ExitCode: 1, InterleavedOutput: proto.String(text)}}}
	}
	return shellResultText(result), &agentv1.ToolCall{Tool: &agentv1.ToolCall_ShellToolCall{ShellToolCall: &agentv1.ShellToolCall{Args: toolArgs, Result: result}}}
}

func completedBackgroundShellFromExec(call agentToolCall, args map[string]any, msg *agentv1.ExecClientMessage, workspaceRoot string) (string, *agentv1.ToolCall) {
	workingDir, _ := resolveToolPath(firstNonEmpty(argString(args, "working_directory"), argString(args, "cwd"), workspaceRoot), workspaceRoot)
	toolArgs := &agentv1.ShellArgs{Command: argString(args, "command"), WorkingDirectory: workingDir, Timeout: 0, ToolCallId: call.ID, TimeoutBehavior: agentv1.TimeoutBehavior_TIMEOUT_BEHAVIOR_BACKGROUND}
	text := execClientMessageText(msg)
	if result := msg.GetBackgroundShellSpawnResult(); result != nil {
		text = backgroundShellSpawnResultText(result)
	}
	result := &agentv1.ShellResult{IsBackground: proto.Bool(true), Result: &agentv1.ShellResult_Success{Success: &agentv1.ShellSuccess{Command: toolArgs.Command, WorkingDirectory: workingDir, ExitCode: 0, InterleavedOutput: proto.String(text)}}}
	return text, &agentv1.ToolCall{Tool: &agentv1.ToolCall_ShellToolCall{ShellToolCall: &agentv1.ShellToolCall{Args: toolArgs, Result: result}}}
}

func completedWriteShellStdinFromExec(call agentToolCall, args map[string]any, msg *agentv1.ExecClientMessage) (string, *agentv1.ToolCall) {
	toolArgs := writeShellStdinArgsFromMap(args)
	result := msg.GetWriteShellStdinResult()
	if result == nil {
		text := execClientMessageText(msg)
		result = &agentv1.WriteShellStdinResult{Result: &agentv1.WriteShellStdinResult_Error{Error: &agentv1.WriteShellStdinError{Error: text}}}
	}
	return writeShellStdinResultText(result), &agentv1.ToolCall{Tool: &agentv1.ToolCall_WriteShellStdinToolCall{WriteShellStdinToolCall: &agentv1.WriteShellStdinToolCall{Args: toolArgs, Result: result}}}
}

func completedWriteFromExec(call agentToolCall, args map[string]any, msg *agentv1.ExecClientMessage, workspaceRoot string) (string, *agentv1.ToolCall) {
	_ = call
	toolArgs := editArgsFromMap(args, workspaceRoot)
	result := msg.GetWriteResult()
	if result == nil {
		text := execClientMessageText(msg)
		editResult := &agentv1.EditResult{Result: &agentv1.EditResult_Error{Error: &agentv1.EditError{Path: toolArgs.Path, Error: text, ModelVisibleError: proto.String(text)}}}
		return text, &agentv1.ToolCall{Tool: &agentv1.ToolCall_EditToolCall{EditToolCall: &agentv1.EditToolCall{Args: toolArgs, Result: editResult}}}
	}
	text := writeResultText(result)
	editResult := editResultFromWriteResult(toolArgs.Path, result, text)
	return text, &agentv1.ToolCall{Tool: &agentv1.ToolCall_EditToolCall{EditToolCall: &agentv1.EditToolCall{Args: toolArgs, Result: editResult}}}
}

func completedDeleteFromExec(call agentToolCall, args map[string]any, msg *agentv1.ExecClientMessage, workspaceRoot string) (string, *agentv1.ToolCall) {
	toolArgs := deleteArgsFromMap(args, workspaceRoot)
	result := msg.GetDeleteResult()
	if result == nil {
		text := execClientMessageText(msg)
		result = &agentv1.DeleteResult{Result: &agentv1.DeleteResult_Error{Error: &agentv1.DeleteError{Path: toolArgs.Path, Error: text}}}
	}
	return deleteResultText(result), &agentv1.ToolCall{Tool: &agentv1.ToolCall_DeleteToolCall{DeleteToolCall: &agentv1.DeleteToolCall{Args: toolArgs, Result: result}}}
}

func completedReadLintsFromExec(call agentToolCall, args map[string]any, msg *agentv1.ExecClientMessage, workspaceRoot string) (string, *agentv1.ToolCall) {
	toolArgs := readLintsArgsFromMap(args, workspaceRoot)
	if result := msg.GetDiagnosticsResult(); result != nil {
		readResult := readLintsResultFromDiagnostics(toolArgs.Paths, result)
		return readLintsResultText(readResult), &agentv1.ToolCall{Tool: &agentv1.ToolCall_ReadLintsToolCall{ReadLintsToolCall: &agentv1.ReadLintsToolCall{Args: toolArgs, Result: readResult}}}
	}
	text := execClientMessageText(msg)
	result := &agentv1.ReadLintsToolResult{Result: &agentv1.ReadLintsToolResult_Error{Error: &agentv1.ReadLintsToolError{ErrorMessage: text}}}
	return text, &agentv1.ToolCall{Tool: &agentv1.ToolCall_ReadLintsToolCall{ReadLintsToolCall: &agentv1.ReadLintsToolCall{Args: toolArgs, Result: result}}}
}

func completedWebFetchFromExec(call agentToolCall, args map[string]any, msg *agentv1.ExecClientMessage) (string, *agentv1.ToolCall) {
	toolArgs := webFetchArgsFromMap(args)
	if result := msg.GetFetchResult(); result != nil {
		webResult := webFetchResultFromFetch(result)
		return webFetchResultText(webResult), &agentv1.ToolCall{Tool: &agentv1.ToolCall_WebFetchToolCall{WebFetchToolCall: &agentv1.WebFetchToolCall{Args: toolArgs, Result: webResult}}}
	}
	text := execClientMessageText(msg)
	result := &agentv1.WebFetchResult{Result: &agentv1.WebFetchResult_Error{Error: &agentv1.WebFetchError{Url: toolArgs.Url, Error: text}}}
	return text, &agentv1.ToolCall{Tool: &agentv1.ToolCall_WebFetchToolCall{WebFetchToolCall: &agentv1.WebFetchToolCall{Args: toolArgs, Result: result}}}
}

func execClientMessageText(msg *agentv1.ExecClientMessage) string {
	if msg == nil {
		return "empty exec result"
	}
	if result := msg.GetReadResult(); result != nil {
		if success := result.GetSuccess(); success != nil {
			if success.GetContent() != "" {
				return success.GetContent()
			}
			if len(success.GetData()) > 0 {
				return string(success.GetData())
			}
			return "read completed"
		}
		if err := result.GetError(); err != nil {
			return err.GetError()
		}
		if rejected := result.GetRejected(); rejected != nil {
			return rejected.GetReason()
		}
		if missing := result.GetFileNotFound(); missing != nil {
			return "file not found: " + missing.GetPath()
		}
		if invalid := result.GetInvalidFile(); invalid != nil {
			return invalid.GetReason()
		}
	}
	if result := msg.GetGrepResult(); result != nil {
		return grepResultText(result)
	}
	if result := msg.GetLsResult(); result != nil {
		return lsResultText(result)
	}
	if result := msg.GetShellResult(); result != nil {
		return shellResultText(result)
	}
	return "exec result received"
}

func lsResultText(result *agentv1.LsResult) string {
	if result == nil {
		return "empty ls result"
	}
	if err := result.GetError(); err != nil {
		return err.GetError()
	}
	if rejected := result.GetRejected(); rejected != nil {
		return rejected.GetReason()
	}
	root := (*agentv1.LsDirectoryTreeNode)(nil)
	if success := result.GetSuccess(); success != nil {
		root = success.GetDirectoryTreeRoot()
	} else if timeout := result.GetTimeout(); timeout != nil {
		root = timeout.GetDirectoryTreeRoot()
	}
	if root == nil {
		return "directory listed"
	}
	lines := []string{}
	appendLsNodeLines(&lines, root, 0, 400)
	if len(lines) == 0 {
		return firstNonEmpty(root.GetAbsPath(), "empty directory")
	}
	return strings.Join(lines, "\n")
}

func appendLsNodeLines(lines *[]string, node *agentv1.LsDirectoryTreeNode, depth int, limit int) {
	if node == nil || len(*lines) >= limit {
		return
	}
	indent := strings.Repeat("  ", depth)
	name := filepath.Base(node.GetAbsPath())
	if name == "." || name == string(filepath.Separator) || name == "" {
		name = node.GetAbsPath()
	}
	if name != "" {
		*lines = append(*lines, indent+name+"/")
	}
	for _, dir := range node.GetChildrenDirs() {
		appendLsNodeLines(lines, dir, depth+1, limit)
		if len(*lines) >= limit {
			return
		}
	}
	for _, file := range node.GetChildrenFiles() {
		if len(*lines) >= limit {
			return
		}
		*lines = append(*lines, strings.Repeat("  ", depth+1)+file.GetName())
	}
}

func grepResultText(result *agentv1.GrepResult) string {
	if result == nil {
		return "empty grep result"
	}
	if err := result.GetError(); err != nil {
		return err.GetError()
	}
	success := result.GetSuccess()
	if success == nil {
		return "grep completed"
	}
	lines := []string{}
	addUnion := func(union *agentv1.GrepUnionResult) {
		if union == nil {
			return
		}
		if files := union.GetFiles(); files != nil {
			lines = append(lines, files.GetFiles()...)
		}
		if content := union.GetContent(); content != nil {
			for _, fileMatch := range content.GetMatches() {
				for _, match := range fileMatch.GetMatches() {
					lines = append(lines, fmt.Sprintf("%s:%d:%s", fileMatch.GetFile(), match.GetLineNumber(), match.GetContent()))
				}
			}
		}
	}
	addUnion(success.GetActiveEditorResult())
	for _, union := range success.GetWorkspaceResults() {
		addUnion(union)
	}
	if len(lines) == 0 {
		return "no matches"
	}
	return strings.Join(lines, "\n")
}

func shellResultText(result *agentv1.ShellResult) string {
	if result == nil {
		return "empty shell result"
	}
	if success := result.GetSuccess(); success != nil {
		return firstNonEmpty(success.GetInterleavedOutput(), strings.TrimRight(success.GetStdout()+success.GetStderr(), "\r\n"), "command completed successfully")
	}
	if failure := result.GetFailure(); failure != nil {
		return firstNonEmpty(failure.GetInterleavedOutput(), strings.TrimRight(failure.GetStdout()+failure.GetStderr(), "\r\n"), fmt.Sprintf("command failed with exit code %d", failure.GetExitCode()))
	}
	if timeout := result.GetTimeout(); timeout != nil {
		return fmt.Sprintf("command timed out after %d ms", timeout.GetTimeoutMs())
	}
	if rejected := result.GetRejected(); rejected != nil {
		return rejected.GetReason()
	}
	if spawn := result.GetSpawnError(); spawn != nil {
		return spawn.GetError()
	}
	if denied := result.GetPermissionDenied(); denied != nil {
		return denied.GetError()
	}
	return "shell completed"
}

func backgroundShellSpawnResultText(result *agentv1.BackgroundShellSpawnResult) string {
	if result == nil {
		return "empty background shell result"
	}
	if success := result.GetSuccess(); success != nil {
		return fmt.Sprintf("background shell started with id %d", success.GetShellId())
	}
	if err := result.GetError(); err != nil {
		return err.GetError()
	}
	if rejected := result.GetRejected(); rejected != nil {
		return rejected.GetReason()
	}
	if denied := result.GetPermissionDenied(); denied != nil {
		return denied.GetError()
	}
	return "background shell started"
}

func writeShellStdinResultText(result *agentv1.WriteShellStdinResult) string {
	if result == nil {
		return "empty stdin result"
	}
	if success := result.GetSuccess(); success != nil {
		return fmt.Sprintf("wrote stdin to shell %d", success.GetShellId())
	}
	if err := result.GetError(); err != nil {
		return err.GetError()
	}
	return "stdin written"
}

func writeResultText(result *agentv1.WriteResult) string {
	if result == nil {
		return "empty write result"
	}
	if success := result.GetSuccess(); success != nil {
		return fmt.Sprintf("wrote %s (%d bytes)", success.GetPath(), success.GetFileSize())
	}
	if err := result.GetError(); err != nil {
		return err.GetError()
	}
	if rejected := result.GetRejected(); rejected != nil {
		return rejected.GetReason()
	}
	if denied := result.GetPermissionDenied(); denied != nil {
		return denied.GetError()
	}
	if noSpace := result.GetNoSpace(); noSpace != nil {
		return "no space left writing " + noSpace.GetPath()
	}
	return "write completed"
}

func deleteResultText(result *agentv1.DeleteResult) string {
	if result == nil {
		return "empty delete result"
	}
	if success := result.GetSuccess(); success != nil {
		return "deleted file: " + success.GetPath()
	}
	if missing := result.GetFileNotFound(); missing != nil {
		return "file not found: " + missing.GetPath()
	}
	if notFile := result.GetNotFile(); notFile != nil {
		return fmt.Sprintf("path is not a file: %s (%s)", notFile.GetPath(), notFile.GetActualType())
	}
	if err := result.GetError(); err != nil {
		return err.GetError()
	}
	if rejected := result.GetRejected(); rejected != nil {
		return rejected.GetReason()
	}
	if denied := result.GetPermissionDenied(); denied != nil {
		return denied.GetClientVisibleError()
	}
	return "delete completed"
}

func readLintsResultText(result *agentv1.ReadLintsToolResult) string {
	if result == nil {
		return "empty diagnostics result"
	}
	if err := result.GetError(); err != nil {
		return err.GetErrorMessage()
	}
	success := result.GetSuccess()
	if success == nil || success.GetTotalDiagnostics() == 0 {
		return "no diagnostics"
	}
	lines := []string{}
	for _, file := range success.GetFileDiagnostics() {
		for _, diag := range file.GetDiagnostics() {
			lines = append(lines, fmt.Sprintf("%s: %s", file.GetPath(), diag.GetMessage()))
		}
	}
	if len(lines) == 0 {
		return "no diagnostics"
	}
	return strings.Join(lines, "\n")
}

func webFetchResultText(result *agentv1.WebFetchResult) string {
	if result == nil {
		return "empty web fetch result"
	}
	if success := result.GetSuccess(); success != nil {
		return success.GetMarkdown()
	}
	if err := result.GetError(); err != nil {
		return err.GetError()
	}
	if rejected := result.GetRejected(); rejected != nil {
		return rejected.GetReason()
	}
	return "web fetch completed"
}

func fetchResultText(result *agentv1.FetchResult) string {
	if result == nil {
		return "empty fetch result"
	}
	if success := result.GetSuccess(); success != nil {
		return success.GetContent()
	}
	if err := result.GetError(); err != nil {
		return err.GetError()
	}
	return "fetch completed"
}

func safeLogURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.RawQuery = ""
	u.User = nil
	return u.String()
}

func truncateForLog(value string, limit int) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

func agentToolCallNames(calls []agentToolCall) string {
	parts := make([]string, 0, len(calls))
	for _, call := range calls {
		name := providerVisibleAgentToolName(call.Name)
		if call.ID != "" {
			parts = append(parts, fmt.Sprintf("%s(%s)", name, call.ID))
			continue
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, ",")
}

func agentExecClientMessageKind(msg *agentv1.ExecClientMessage) string {
	if msg == nil {
		return "empty"
	}
	switch {
	case msg.GetReadResult() != nil:
		return "read_result"
	case msg.GetGrepResult() != nil:
		return "grep_result"
	case msg.GetShellResult() != nil:
		return "shell_result"
	case msg.GetShellStream() != nil:
		return "shell_stream"
	case msg.GetWriteResult() != nil:
		return "write_result"
	case msg.GetDeleteResult() != nil:
		return "delete_result"
	case msg.GetLsResult() != nil:
		return "ls_result"
	case msg.GetDiagnosticsResult() != nil:
		return "diagnostics_result"
	case msg.GetRequestContextResult() != nil:
		return "request_context_result"
	case msg.GetMcpResult() != nil:
		return "mcp_result"
	case msg.GetBackgroundShellSpawnResult() != nil:
		return "background_shell_spawn_result"
	case msg.GetListMcpResourcesExecResult() != nil:
		return "list_mcp_resources_exec_result"
	case msg.GetReadMcpResourceExecResult() != nil:
		return "read_mcp_resource_exec_result"
	case msg.GetFetchResult() != nil:
		return "fetch_result"
	case msg.GetRecordScreenResult() != nil:
		return "record_screen_result"
	case msg.GetComputerUseResult() != nil:
		return "computer_use_result"
	case msg.GetWriteShellStdinResult() != nil:
		return "write_shell_stdin_result"
	case msg.GetExecuteHookResult() != nil:
		return "execute_hook_result"
	case msg.GetMessage() != nil:
		return fmt.Sprintf("%T", msg.GetMessage())
	default:
		return "unknown"
	}
}

func formatAgentToolArgs(args map[string]any) string {
	data, err := json.Marshal(args)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func normalizeAgentToolArgs(name string, args map[string]any, workspaceRoot string) map[string]any {
	if args == nil {
		args = map[string]any{}
	}
	workspaceRoot = normalizeWorkspaceRoot(workspaceRoot)
	switch normalizeAgentToolName(name) {
	case "Read":
		if resolved, err := resolveToolPath(argString(args, "path"), workspaceRoot); err == nil && resolved != "" {
			args["path"] = resolved
		}
	case "Ls":
		pathValue := firstNonEmpty(argString(args, "path"), argString(args, "target_directory"), workspaceRoot)
		if resolved, err := resolveToolPath(pathValue, workspaceRoot); err == nil && resolved != "" {
			if workspaceRoot != "" && !directoryExists(resolved) {
				resolved = workspaceRoot
			}
			args["path"] = resolved
			delete(args, "target_directory")
		}
	case "Grep":
		pathValue := firstNonEmpty(argString(args, "path"), workspaceRoot)
		if resolved, err := resolveToolPath(pathValue, workspaceRoot); err == nil && resolved != "" {
			if workspaceRoot != "" && !pathExists(resolved) {
				resolved = workspaceRoot
			}
			args["path"] = resolved
		}
	case "Glob":
		target := firstNonEmpty(argString(args, "target_directory"), workspaceRoot)
		if resolved, err := resolveToolPath(target, workspaceRoot); err == nil && resolved != "" {
			if workspaceRoot != "" && !directoryExists(resolved) {
				resolved = workspaceRoot
			}
			args["target_directory"] = resolved
		}
	case "Shell":
		workingDir := firstNonEmpty(argString(args, "working_directory"), argString(args, "cwd"), workspaceRoot)
		if resolved, err := resolveToolPath(workingDir, workspaceRoot); err == nil && resolved != "" {
			if workspaceRoot != "" && !directoryExists(resolved) {
				resolved = workspaceRoot
			}
			args["working_directory"] = resolved
			delete(args, "cwd")
		}
	}
	return args
}

func (g *Gateway) readAgentFileContent(resolved string, workspaceRoot string) ([]byte, string, error) {
	// Try ContextStore first with multiple path formats
	if g.contextStore != nil {
		// Try absolute path
		if content, source, found := g.contextStore.GetFileContent(resolved); found {
			log.Printf("[Gateway] Read file from ContextStore: path=%s source=%s", resolved, source)
			return []byte(content), "contextstore", nil
		}

		// Try relative path (relative to workspace root)
		relativePath := relativeFileSyncPath(resolved, workspaceRoot)
		if relativePath != resolved {
			if content, source, found := g.contextStore.GetFileContent(relativePath); found {
				log.Printf("[Gateway] Read file from ContextStore (relative): path=%s source=%s", relativePath, source)
				return []byte(content), "contextstore", nil
			}
		}

		// Try slash-normalized path
		slashPath := filepath.ToSlash(resolved)
		if slashPath != resolved {
			if content, source, found := g.contextStore.GetFileContent(slashPath); found {
				log.Printf("[Gateway] Read file from ContextStore (slash): path=%s source=%s", slashPath, source)
				return []byte(content), "contextstore", nil
			}
		}
	}

	// Try FileSync cache
	if content, ok := g.lookupFileSyncContent("", relativeFileSyncPath(resolved, workspaceRoot)); ok {
		return []byte(content), "filesync", nil
	}
	if content, ok := g.lookupFileSyncContent("", filepath.ToSlash(resolved)); ok {
		return []byte(content), "filesync", nil
	}

	// Fall back to disk
	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, "", err
	}
	return data, "disk", nil
}

func relativeFileSyncPath(path string, workspaceRoot string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = filepath.Clean(filepath.FromSlash(path))
	workspaceRoot = normalizeWorkspaceRoot(workspaceRoot)
	if workspaceRoot != "" {
		if rel, err := filepath.Rel(workspaceRoot, path); err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(path)
}

func (g *Gateway) executeReadTool(args map[string]any, workspaceRoot string) (string, *agentv1.ToolCall) {
	path := argString(args, "path")
	resolved, err := resolveToolPath(path, workspaceRoot)
	toolArgs := readToolArgsFromMap(args)
	toolArgs.Path = resolved
	if err != nil {
		result := &agentv1.ReadToolResult{Result: &agentv1.ReadToolResult_Error{Error: &agentv1.ReadToolError{ErrorMessage: err.Error()}}}
		return err.Error(), &agentv1.ToolCall{Tool: &agentv1.ToolCall_ReadToolCall{ReadToolCall: &agentv1.ReadToolCall{Args: toolArgs, Result: result}}}
	}
	data, _, err := g.readAgentFileContent(resolved, workspaceRoot)
	if err != nil {
		result := &agentv1.ReadToolResult{Result: &agentv1.ReadToolResult_Error{Error: &agentv1.ReadToolError{ErrorMessage: err.Error()}}}
		return err.Error(), &agentv1.ToolCall{Tool: &agentv1.ToolCall_ReadToolCall{ReadToolCall: &agentv1.ReadToolCall{Args: toolArgs, Result: result}}}
	}
	content := string(data)
	lines := splitLines(content)
	start := int(argInt(args, "offset", 1))
	if start <= 0 {
		start = 1
	}
	limit := int(argInt(args, "limit", 500))
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	end := start + limit - 1
	if end > len(lines) {
		end = len(lines)
	}
	if start > len(lines) && len(lines) > 0 {
		start = len(lines)
	}
	selected := ""
	if len(lines) > 0 && start <= end {
		selected = strings.Join(lines[start-1:end], "\n")
	}
	includeLines := true
	if value, ok := args["include_line_numbers"]; ok {
		includeLines = argBoolValue(value, true)
	}
	modelText := selected
	if includeLines {
		modelText = addLineNumbers(selected, start)
	}
	exceeded := end < len(lines)
	result := &agentv1.ReadToolResult{Result: &agentv1.ReadToolResult_Success{Success: &agentv1.ReadToolSuccess{
		IsEmpty:            len(data) == 0,
		ExceededLimit:      exceeded,
		TotalLines:         uint32(len(lines)),
		FileSize:           uint32(len(data)),
		Path:               resolved,
		ReadRange:          &agentv1.ReadRange{StartLine: uint32(start), EndLine: uint32(end)},
		IncludeLineNumbers: proto.Bool(includeLines),
		Output:             &agentv1.ReadToolSuccess_Content{Content: modelText},
	}}}
	return modelText, &agentv1.ToolCall{Tool: &agentv1.ToolCall_ReadToolCall{ReadToolCall: &agentv1.ReadToolCall{Args: toolArgs, Result: result}}}
}

func executeGlobTool(args map[string]any, workspaceRoot string) (string, *agentv1.ToolCall) {
	pattern := firstNonEmpty(argString(args, "glob_pattern"), argString(args, "pattern"))
	target := firstNonEmpty(argString(args, "target_directory"), workspaceRoot)
	resolved, err := resolveToolPath(target, workspaceRoot)
	toolArgs := &agentv1.GlobToolArgs{GlobPattern: pattern, TargetDirectory: proto.String(resolved)}
	if pattern == "" {
		err = fmt.Errorf("glob_pattern is required")
	}
	if err != nil {
		result := &agentv1.GlobToolResult{Result: &agentv1.GlobToolResult_Error{Error: &agentv1.GlobToolError{Error: err.Error()}}}
		return err.Error(), &agentv1.ToolCall{Tool: &agentv1.ToolCall_GlobToolCall{GlobToolCall: &agentv1.GlobToolCall{Args: toolArgs, Result: result}}}
	}
	files, truncated, err := globFiles(resolved, pattern, 300)
	if err != nil {
		result := &agentv1.GlobToolResult{Result: &agentv1.GlobToolResult_Error{Error: &agentv1.GlobToolError{Error: err.Error()}}}
		return err.Error(), &agentv1.ToolCall{Tool: &agentv1.ToolCall_GlobToolCall{GlobToolCall: &agentv1.GlobToolCall{Args: toolArgs, Result: result}}}
	}
	text := strings.Join(files, "\n")
	if text == "" {
		text = "no files matched"
	}
	result := &agentv1.GlobToolResult{Result: &agentv1.GlobToolResult_Success{Success: &agentv1.GlobToolSuccess{
		Pattern:         pattern,
		Path:            resolved,
		Files:           files,
		TotalFiles:      int32(len(files)),
		ClientTruncated: truncated,
	}}}
	return text, &agentv1.ToolCall{Tool: &agentv1.ToolCall_GlobToolCall{GlobToolCall: &agentv1.GlobToolCall{Args: toolArgs, Result: result}}}
}

func executeLsTool(args map[string]any, workspaceRoot string) (string, *agentv1.ToolCall) {
	toolArgs := lsToolArgsFromMap(args, workspaceRoot)
	entries, err := os.ReadDir(toolArgs.Path)
	if err != nil {
		result := &agentv1.LsResult{Result: &agentv1.LsResult_Error{Error: &agentv1.LsError{Path: toolArgs.Path, Error: err.Error()}}}
		return err.Error(), &agentv1.ToolCall{Tool: &agentv1.ToolCall_LsToolCall{LsToolCall: &agentv1.LsToolCall{Args: toolArgs, Result: result}}}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})
	root := &agentv1.LsDirectoryTreeNode{AbsPath: toolArgs.Path, ChildrenWereProcessed: true, FullSubtreeExtensionCounts: map[string]int32{}}
	lines := []string{filepath.Base(toolArgs.Path) + "/"}
	for i, entry := range entries {
		if i >= 300 {
			lines = append(lines, "...[directory listing truncated]")
			break
		}
		name := entry.Name()
		if entry.IsDir() {
			root.ChildrenDirs = append(root.ChildrenDirs, &agentv1.LsDirectoryTreeNode{AbsPath: filepath.Join(toolArgs.Path, name), ChildrenWereProcessed: false})
			lines = append(lines, "  "+name+"/")
			continue
		}
		root.ChildrenFiles = append(root.ChildrenFiles, &agentv1.LsDirectoryTreeNode_File{Name: name})
		root.NumFiles++
		ext := strings.ToLower(filepath.Ext(name))
		if ext == "" {
			ext = "[no extension]"
		}
		root.FullSubtreeExtensionCounts[ext]++
		lines = append(lines, "  "+name)
	}
	result := &agentv1.LsResult{Result: &agentv1.LsResult_Success{Success: &agentv1.LsSuccess{DirectoryTreeRoot: root}}}
	return strings.Join(lines, "\n"), &agentv1.ToolCall{Tool: &agentv1.ToolCall_LsToolCall{LsToolCall: &agentv1.LsToolCall{Args: toolArgs, Result: result}}}
}

func executeGrepTool(args map[string]any, workspaceRoot string) (string, *agentv1.ToolCall) {
	pattern := argString(args, "pattern")
	pathValue := firstNonEmpty(argString(args, "path"), workspaceRoot)
	resolved, err := resolveToolPath(pathValue, workspaceRoot)
	globPattern := argString(args, "glob")
	caseInsensitive := argBool(args, "case_insensitive", false) || argBool(args, "-i", false)
	headLimit := int(argInt(args, "head_limit", 50))
	if headLimit <= 0 || headLimit > 200 {
		headLimit = 50
	}
	toolArgs := grepToolArgsFromMap(args)
	toolArgs.Pattern = pattern
	if resolved != "" {
		toolArgs.Path = proto.String(resolved)
	}
	if pattern == "" {
		err = fmt.Errorf("pattern is required")
	}
	if err != nil {
		result := &agentv1.GrepResult{Result: &agentv1.GrepResult_Error{Error: &agentv1.GrepError{Error: err.Error()}}}
		return err.Error(), &agentv1.ToolCall{Tool: &agentv1.ToolCall_GrepToolCall{GrepToolCall: &agentv1.GrepToolCall{Args: toolArgs, Result: result}}}
	}
	matches, totalLines, truncated, err := grepFiles(resolved, pattern, globPattern, caseInsensitive, headLimit)
	if err != nil {
		result := &agentv1.GrepResult{Result: &agentv1.GrepResult_Error{Error: &agentv1.GrepError{Error: err.Error()}}}
		return err.Error(), &agentv1.ToolCall{Tool: &agentv1.ToolCall_GrepToolCall{GrepToolCall: &agentv1.GrepToolCall{Args: toolArgs, Result: result}}}
	}
	textLines := []string{}
	fileMatches := map[string][]*agentv1.GrepContentMatch{}
	for _, match := range matches {
		textLines = append(textLines, fmt.Sprintf("%s:%d:%s", match.File, match.LineNumber, match.Content))
		fileMatches[match.File] = append(fileMatches[match.File], &agentv1.GrepContentMatch{LineNumber: int32(match.LineNumber), Content: match.Content})
	}
	if len(textLines) == 0 {
		textLines = append(textLines, "no matches")
	}
	contentMatches := make([]*agentv1.GrepFileMatch, 0, len(fileMatches))
	for file, lines := range fileMatches {
		contentMatches = append(contentMatches, &agentv1.GrepFileMatch{File: file, Matches: lines})
	}
	sort.Slice(contentMatches, func(i, j int) bool { return contentMatches[i].File < contentMatches[j].File })
	union := &agentv1.GrepUnionResult{Result: &agentv1.GrepUnionResult_Content{Content: &agentv1.GrepContentResult{
		Matches:           contentMatches,
		TotalLines:        int32(totalLines),
		TotalMatchedLines: int32(len(matches)),
		ClientTruncated:   truncated,
	}}}
	result := &agentv1.GrepResult{Result: &agentv1.GrepResult_Success{Success: &agentv1.GrepSuccess{
		Pattern:            pattern,
		Path:               resolved,
		OutputMode:         "content",
		WorkspaceResults:   map[string]*agentv1.GrepUnionResult{resolved: union},
		ActiveEditorResult: union,
	}}}
	return strings.Join(textLines, "\n"), &agentv1.ToolCall{Tool: &agentv1.ToolCall_GrepToolCall{GrepToolCall: &agentv1.GrepToolCall{Args: toolArgs, Result: result}}}
}

func executeShellTool(args map[string]any, workspaceRoot string) (string, *agentv1.ToolCall) {
	command := argString(args, "command")
	workingDir := firstNonEmpty(argString(args, "working_directory"), argString(args, "cwd"), workspaceRoot)
	resolved, err := resolveToolPath(workingDir, workspaceRoot)
	if err != nil && workingDir == "" {
		resolved = workspaceRoot
		err = nil
	}
	toolArgs := &agentv1.ShellArgs{Command: command, WorkingDirectory: resolved, Timeout: int32(argInt(args, "block_until_ms", 30000)), TimeoutBehavior: agentv1.TimeoutBehavior_TIMEOUT_BEHAVIOR_CANCEL}
	if toolArgs.Timeout <= 0 || toolArgs.Timeout > 120000 {
		toolArgs.Timeout = 30000
	}
	if command == "" {
		err = fmt.Errorf("command is required")
	}
	if err == nil {
		err = validateShellCommand(command)
	}
	if err != nil {
		result := &agentv1.ShellResult{Result: &agentv1.ShellResult_Rejected{Rejected: &agentv1.ShellRejected{Command: command, WorkingDirectory: resolved, Reason: err.Error()}}}
		return err.Error(), &agentv1.ToolCall{Tool: &agentv1.ToolCall_ShellToolCall{ShellToolCall: &agentv1.ShellToolCall{Args: toolArgs, Result: result}}}
	}

	startedAt := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(toolArgs.Timeout)*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", command)
	cmd.Dir = resolved
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	executionMs := int32(time.Since(startedAt).Milliseconds())
	interleaved := strings.TrimRight(stdout.String()+stderr.String(), "\r\n")
	if ctx.Err() == context.DeadlineExceeded {
		result := &agentv1.ShellResult{Result: &agentv1.ShellResult_Timeout{Timeout: &agentv1.ShellTimeout{Command: command, WorkingDirectory: resolved, TimeoutMs: toolArgs.Timeout}}}
		return "command timed out", &agentv1.ToolCall{Tool: &agentv1.ToolCall_ShellToolCall{ShellToolCall: &agentv1.ShellToolCall{Args: toolArgs, Result: result}}}
	}
	if runErr != nil {
		exitCode := int32(1)
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = int32(exitErr.ExitCode())
		}
		result := &agentv1.ShellResult{Result: &agentv1.ShellResult_Failure{Failure: &agentv1.ShellFailure{Command: command, WorkingDirectory: resolved, ExitCode: exitCode, Stdout: stdout.String(), Stderr: stderr.String(), ExecutionTime: executionMs, InterleavedOutput: proto.String(interleaved)}}}
		return interleaved, &agentv1.ToolCall{Tool: &agentv1.ToolCall_ShellToolCall{ShellToolCall: &agentv1.ShellToolCall{Args: toolArgs, Result: result}}}
	}
	result := &agentv1.ShellResult{IsBackground: proto.Bool(false), Result: &agentv1.ShellResult_Success{Success: &agentv1.ShellSuccess{Command: command, WorkingDirectory: resolved, ExitCode: 0, Stdout: stdout.String(), Stderr: stderr.String(), ExecutionTime: executionMs, InterleavedOutput: proto.String(interleaved)}}}
	if interleaved == "" {
		interleaved = "command completed successfully"
	}
	return interleaved, &agentv1.ToolCall{Tool: &agentv1.ToolCall_ShellToolCall{ShellToolCall: &agentv1.ShellToolCall{Args: toolArgs, Result: result}}}
}

func (g *Gateway) executePatchEditTool(args map[string]any, workspaceRoot string) (string, *agentv1.ToolCall) {
	path := argString(args, "path")
	resolved, err := resolveToolPath(path, workspaceRoot)
	toolArgs := editArgsFromMap(args, workspaceRoot)
	toolArgs.Path = resolved
	oldString := argString(args, "old_string")
	newString := argString(args, "new_string")
	replaceAll := argBool(args, "replace_all", false)
	if err != nil {
		result := &agentv1.EditResult{Result: &agentv1.EditResult_Error{Error: &agentv1.EditError{Path: resolved, Error: err.Error(), ModelVisibleError: proto.String(err.Error())}}}
		return err.Error(), &agentv1.ToolCall{Tool: &agentv1.ToolCall_EditToolCall{EditToolCall: &agentv1.EditToolCall{Args: toolArgs, Result: result}}}
	}
	if oldString == "" {
		err = fmt.Errorf("old_string is required")
	} else if oldString == newString {
		err = fmt.Errorf("new_string must differ from old_string")
	}
	if err != nil {
		result := &agentv1.EditResult{Result: &agentv1.EditResult_Rejected{Rejected: &agentv1.EditRejected{Path: resolved, Reason: err.Error()}}}
		return err.Error(), &agentv1.ToolCall{Tool: &agentv1.ToolCall_EditToolCall{EditToolCall: &agentv1.EditToolCall{Args: toolArgs, Result: result}}}
	}
	beforeBytes, _, err := g.readAgentFileContent(resolved, workspaceRoot)
	if err != nil {
		result := &agentv1.EditResult{Result: &agentv1.EditResult_Error{Error: &agentv1.EditError{Path: resolved, Error: err.Error(), ModelVisibleError: proto.String(err.Error())}}}
		return err.Error(), &agentv1.ToolCall{Tool: &agentv1.ToolCall_EditToolCall{EditToolCall: &agentv1.EditToolCall{Args: toolArgs, Result: result}}}
	}
	before := string(beforeBytes)
	count := strings.Count(before, oldString)
	if count == 0 {
		err = fmt.Errorf("old_string was not found")
	} else if count > 1 && !replaceAll {
		err = fmt.Errorf("old_string matched %d times; provide more context or set replace_all", count)
	}
	if err != nil {
		result := &agentv1.EditResult{Result: &agentv1.EditResult_Rejected{Rejected: &agentv1.EditRejected{Path: resolved, Reason: err.Error()}}}
		return err.Error(), &agentv1.ToolCall{Tool: &agentv1.ToolCall_EditToolCall{EditToolCall: &agentv1.EditToolCall{Args: toolArgs, Result: result}}}
	}
	after := strings.Replace(before, oldString, newString, 1)
	if replaceAll {
		after = strings.ReplaceAll(before, oldString, newString)
	}
	if err := os.WriteFile(resolved, []byte(after), 0644); err != nil {
		result := &agentv1.EditResult{Result: &agentv1.EditResult_Error{Error: &agentv1.EditError{Path: resolved, Error: err.Error(), ModelVisibleError: proto.String(err.Error())}}}
		return err.Error(), &agentv1.ToolCall{Tool: &agentv1.ToolCall_EditToolCall{EditToolCall: &agentv1.EditToolCall{Args: toolArgs, Result: result}}}
	}
	g.storeFileSyncEntry("", relativeFileSyncPath(resolved, workspaceRoot), after, sha256Text(after), 0)
	linesAdded, linesRemoved := lineDelta(oldString, newString)
	message := fmt.Sprintf("replaced %d occurrence(s) in %s", countIfReplaceAll(count, replaceAll), resolved)
	result := &agentv1.EditResult{Result: &agentv1.EditResult_Success{Success: &agentv1.EditSuccess{Path: resolved, LinesAdded: proto.Int32(int32(linesAdded)), LinesRemoved: proto.Int32(int32(linesRemoved)), BeforeFullFileContent: proto.String(before), AfterFullFileContent: after, Message: proto.String(message)}}}
	return message, &agentv1.ToolCall{Tool: &agentv1.ToolCall_EditToolCall{EditToolCall: &agentv1.EditToolCall{Args: toolArgs, Result: result}}}
}

func (g *Gateway) executeWriteTool(args map[string]any, workspaceRoot string) (string, *agentv1.ToolCall) {
	resolved, err := resolveToolPath(argString(args, "path"), workspaceRoot)
	toolArgs := editArgsFromMap(args, workspaceRoot)
	toolArgs.Path = resolved
	contents := firstNonEmpty(argString(args, "contents"), argString(args, "file_text"), argString(args, "content"))
	if err == nil {
		err = os.MkdirAll(filepath.Dir(resolved), 0755)
	}
	if err == nil {
		err = os.WriteFile(resolved, []byte(contents), 0644)
	}
	if err != nil {
		result := &agentv1.EditResult{Result: &agentv1.EditResult_Error{Error: &agentv1.EditError{Path: resolved, Error: err.Error(), ModelVisibleError: proto.String(err.Error())}}}
		return err.Error(), &agentv1.ToolCall{Tool: &agentv1.ToolCall_EditToolCall{EditToolCall: &agentv1.EditToolCall{Args: toolArgs, Result: result}}}
	}
	g.storeFileSyncEntry("", relativeFileSyncPath(resolved, workspaceRoot), contents, sha256Text(contents), 0)
	message := fmt.Sprintf("wrote %s (%d bytes)", resolved, len(contents))
	result := &agentv1.EditResult{Result: &agentv1.EditResult_Success{Success: &agentv1.EditSuccess{Path: resolved, LinesAdded: proto.Int32(int32(len(splitLines(contents)))), AfterFullFileContent: contents, Message: proto.String(message)}}}
	return message, &agentv1.ToolCall{Tool: &agentv1.ToolCall_EditToolCall{EditToolCall: &agentv1.EditToolCall{Args: toolArgs, Result: result}}}
}

func executeDeleteTool(args map[string]any, workspaceRoot string) (string, *agentv1.ToolCall) {
	toolArgs := deleteArgsFromMap(args, workspaceRoot)
	info, err := os.Stat(toolArgs.Path)
	if err != nil {
		result := &agentv1.DeleteResult{Result: &agentv1.DeleteResult_FileNotFound{FileNotFound: &agentv1.DeleteFileNotFound{Path: toolArgs.Path}}}
		return "file not found: " + toolArgs.Path, &agentv1.ToolCall{Tool: &agentv1.ToolCall_DeleteToolCall{DeleteToolCall: &agentv1.DeleteToolCall{Args: toolArgs, Result: result}}}
	}
	if info.IsDir() {
		result := &agentv1.DeleteResult{Result: &agentv1.DeleteResult_NotFile{NotFile: &agentv1.DeleteNotFile{Path: toolArgs.Path, ActualType: "directory"}}}
		return "delete rejected: path is a directory", &agentv1.ToolCall{Tool: &agentv1.ToolCall_DeleteToolCall{DeleteToolCall: &agentv1.DeleteToolCall{Args: toolArgs, Result: result}}}
	}
	prev, _ := os.ReadFile(toolArgs.Path)
	if err := os.Remove(toolArgs.Path); err != nil {
		result := &agentv1.DeleteResult{Result: &agentv1.DeleteResult_Error{Error: &agentv1.DeleteError{Path: toolArgs.Path, Error: err.Error()}}}
		return err.Error(), &agentv1.ToolCall{Tool: &agentv1.ToolCall_DeleteToolCall{DeleteToolCall: &agentv1.DeleteToolCall{Args: toolArgs, Result: result}}}
	}
	text := "deleted file: " + toolArgs.Path
	result := &agentv1.DeleteResult{Result: &agentv1.DeleteResult_Success{Success: &agentv1.DeleteSuccess{Path: toolArgs.Path, DeletedFile: toolArgs.Path, FileSize: info.Size(), PrevContent: string(prev)}}}
	return text, &agentv1.ToolCall{Tool: &agentv1.ToolCall_DeleteToolCall{DeleteToolCall: &agentv1.DeleteToolCall{Args: toolArgs, Result: result}}}
}

func executeReadLintsTool(args map[string]any, workspaceRoot string) (string, *agentv1.ToolCall) {
	toolArgs := readLintsArgsFromMap(args, workspaceRoot)
	files := make([]*agentv1.FileDiagnostics, 0, len(toolArgs.Paths))
	for _, path := range toolArgs.Paths {
		files = append(files, &agentv1.FileDiagnostics{Path: path})
	}
	result := &agentv1.ReadLintsToolResult{Result: &agentv1.ReadLintsToolResult_Success{Success: &agentv1.ReadLintsToolSuccess{FileDiagnostics: files, TotalFiles: int32(len(files)), TotalDiagnostics: 0}}}
	return readLintsResultText(result), &agentv1.ToolCall{Tool: &agentv1.ToolCall_ReadLintsToolCall{ReadLintsToolCall: &agentv1.ReadLintsToolCall{Args: toolArgs, Result: result}}}
}

func executeWebFetchTool(args map[string]any) (string, *agentv1.ToolCall) {
	toolArgs := webFetchArgsFromMap(args)
	if toolArgs.Url == "" {
		result := &agentv1.WebFetchResult{Result: &agentv1.WebFetchResult_Error{Error: &agentv1.WebFetchError{Error: "url is required"}}}
		return "url is required", &agentv1.ToolCall{Tool: &agentv1.ToolCall_WebFetchToolCall{WebFetchToolCall: &agentv1.WebFetchToolCall{Args: toolArgs, Result: result}}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, toolArgs.Url, nil)
	if err != nil {
		result := &agentv1.WebFetchResult{Result: &agentv1.WebFetchResult_Error{Error: &agentv1.WebFetchError{Url: toolArgs.Url, Error: err.Error()}}}
		return err.Error(), &agentv1.ToolCall{Tool: &agentv1.ToolCall_WebFetchToolCall{WebFetchToolCall: &agentv1.WebFetchToolCall{Args: toolArgs, Result: result}}}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		result := &agentv1.WebFetchResult{Result: &agentv1.WebFetchResult_Error{Error: &agentv1.WebFetchError{Url: toolArgs.Url, Error: err.Error()}}}
		return err.Error(), &agentv1.ToolCall{Tool: &agentv1.ToolCall_WebFetchToolCall{WebFetchToolCall: &agentv1.WebFetchToolCall{Args: toolArgs, Result: result}}}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 200000))
	markdown := string(body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		text := fmt.Sprintf("fetch failed with status %d: %s", resp.StatusCode, truncateForLog(markdown, 1000))
		result := &agentv1.WebFetchResult{Result: &agentv1.WebFetchResult_Error{Error: &agentv1.WebFetchError{Url: toolArgs.Url, Error: text}}}
		return text, &agentv1.ToolCall{Tool: &agentv1.ToolCall_WebFetchToolCall{WebFetchToolCall: &agentv1.WebFetchToolCall{Args: toolArgs, Result: result}}}
	}
	result := &agentv1.WebFetchResult{Result: &agentv1.WebFetchResult_Success{Success: &agentv1.WebFetchSuccess{Url: toolArgs.Url, Markdown: markdown}}}
	return markdown, &agentv1.ToolCall{Tool: &agentv1.ToolCall_WebFetchToolCall{WebFetchToolCall: &agentv1.WebFetchToolCall{Args: toolArgs, Result: result}}}
}

func executeTodoWriteTool(args map[string]any) (string, *agentv1.ToolCall) {
	toolArgs := updateTodosArgsFromMap(args)
	result := &agentv1.UpdateTodosResult{Result: &agentv1.UpdateTodosResult_Success{Success: &agentv1.UpdateTodosSuccess{Todos: toolArgs.Todos, TotalCount: int32(len(toolArgs.Todos)), WasMerge: toolArgs.Merge}}}
	return fmt.Sprintf("updated %d todo(s)", len(toolArgs.Todos)), &agentv1.ToolCall{Tool: &agentv1.ToolCall_UpdateTodosToolCall{UpdateTodosToolCall: &agentv1.UpdateTodosToolCall{Args: toolArgs, Result: result}}}
}

func executeCreatePlanTool(args map[string]any, workspaceRoot string) (string, *agentv1.ToolCall) {
	toolArgs := createPlanArgsFromMap(args)
	planURI := createPlanURI(toolArgs.GetName(), workspaceRoot)
	result := &agentv1.CreatePlanResult{
		PlanUri: planURI,
		Result:  &agentv1.CreatePlanResult_Success{Success: &agentv1.CreatePlanSuccess{}},
	}
	text := "plan created"
	if toolArgs.GetName() != "" {
		text += ": " + toolArgs.GetName()
	}
	if planURI != "" {
		text += " (" + planURI + ")"
	}
	return text, &agentv1.ToolCall{Tool: &agentv1.ToolCall_CreatePlanToolCall{CreatePlanToolCall: &agentv1.CreatePlanToolCall{Args: toolArgs, Result: result}}}
}

func executeUnavailableAgentTool(name string, args map[string]any) (string, *agentv1.ToolCall) {
	text := fmt.Sprintf("%s is not available in the local BYOK proxy; continue using available local tools.", name)
	return text, agentToolCallProto(name, args, text)
}

func validateShellCommand(command string) error {
	lower := strings.ToLower(command)
	blocked := []string{"remove-item", " rm ", " rm\t", "del ", "erase ", "rmdir", "rd /", "format ", "shutdown", "stop-process", "git reset", "git checkout --", "set-executionpolicy"}
	for _, item := range blocked {
		if strings.Contains(lower, item) || strings.HasPrefix(lower, strings.TrimSpace(item)+" ") {
			return fmt.Errorf("shell command rejected by local safety policy")
		}
	}
	return nil
}

func agentToolCallProto(name string, args map[string]any, result any) *agentv1.ToolCall {
	switch normalizeAgentToolName(name) {
	case "Read":
		call := &agentv1.ReadToolCall{Args: readToolArgsFromMap(args)}
		if r, ok := result.(*agentv1.ReadToolResult); ok {
			call.Result = r
		}
		return &agentv1.ToolCall{Tool: &agentv1.ToolCall_ReadToolCall{ReadToolCall: call}}
	case "Grep":
		call := &agentv1.GrepToolCall{Args: grepToolArgsFromMap(args)}
		if r, ok := result.(*agentv1.GrepResult); ok {
			call.Result = r
		}
		return &agentv1.ToolCall{Tool: &agentv1.ToolCall_GrepToolCall{GrepToolCall: call}}
	case "Ls":
		call := &agentv1.LsToolCall{Args: lsToolArgsFromMap(args, "")}
		if r, ok := result.(*agentv1.LsResult); ok {
			call.Result = r
		}
		return &agentv1.ToolCall{Tool: &agentv1.ToolCall_LsToolCall{LsToolCall: call}}
	case "Glob":
		pattern := firstNonEmpty(argString(args, "glob_pattern"), argString(args, "pattern"))
		call := &agentv1.GlobToolCall{Args: &agentv1.GlobToolArgs{GlobPattern: pattern}}
		if dir := argString(args, "target_directory"); dir != "" {
			call.Args.TargetDirectory = proto.String(dir)
		}
		if r, ok := result.(*agentv1.GlobToolResult); ok {
			call.Result = r
		}
		return &agentv1.ToolCall{Tool: &agentv1.ToolCall_GlobToolCall{GlobToolCall: call}}
	case "Shell":
		call := &agentv1.ShellToolCall{Args: &agentv1.ShellArgs{Command: argString(args, "command"), WorkingDirectory: firstNonEmpty(argString(args, "working_directory"), argString(args, "cwd")), Timeout: int32(argInt(args, "block_until_ms", 30000))}}
		if r, ok := result.(*agentv1.ShellResult); ok {
			call.Result = r
		}
		return &agentv1.ToolCall{Tool: &agentv1.ToolCall_ShellToolCall{ShellToolCall: call}}
	case "PatchEdit":
		call := &agentv1.EditToolCall{Args: editArgsFromMap(args, "")}
		if r, ok := result.(*agentv1.EditResult); ok {
			call.Result = r
		}
		return &agentv1.ToolCall{Tool: &agentv1.ToolCall_EditToolCall{EditToolCall: call}}
	case "Delete":
		call := &agentv1.DeleteToolCall{Args: deleteArgsFromMap(args, "")}
		if r, ok := result.(*agentv1.DeleteResult); ok {
			call.Result = r
		}
		return &agentv1.ToolCall{Tool: &agentv1.ToolCall_DeleteToolCall{DeleteToolCall: call}}
	case "ReadLints":
		call := &agentv1.ReadLintsToolCall{Args: readLintsArgsFromMap(args, "")}
		if r, ok := result.(*agentv1.ReadLintsToolResult); ok {
			call.Result = r
		}
		return &agentv1.ToolCall{Tool: &agentv1.ToolCall_ReadLintsToolCall{ReadLintsToolCall: call}}
	case "WebFetch":
		call := &agentv1.WebFetchToolCall{Args: webFetchArgsFromMap(args)}
		if r, ok := result.(*agentv1.WebFetchResult); ok {
			call.Result = r
		}
		return &agentv1.ToolCall{Tool: &agentv1.ToolCall_WebFetchToolCall{WebFetchToolCall: call}}
	case "TodoWrite":
		call := &agentv1.UpdateTodosToolCall{Args: updateTodosArgsFromMap(args)}
		if r, ok := result.(*agentv1.UpdateTodosResult); ok {
			call.Result = r
		}
		return &agentv1.ToolCall{Tool: &agentv1.ToolCall_UpdateTodosToolCall{UpdateTodosToolCall: call}}
	case "CreatePlan":
		call := &agentv1.CreatePlanToolCall{Args: createPlanArgsFromMap(args)}
		if r, ok := result.(*agentv1.CreatePlanResult); ok {
			call.Result = r
		}
		return &agentv1.ToolCall{Tool: &agentv1.ToolCall_CreatePlanToolCall{CreatePlanToolCall: call}}
	case "WriteShellStdin":
		call := &agentv1.WriteShellStdinToolCall{Args: writeShellStdinArgsFromMap(args)}
		if r, ok := result.(*agentv1.WriteShellStdinResult); ok {
			call.Result = r
		}
		return &agentv1.ToolCall{Tool: &agentv1.ToolCall_WriteShellStdinToolCall{WriteShellStdinToolCall: call}}
	case "Write":
		return &agentv1.ToolCall{Tool: &agentv1.ToolCall_EditToolCall{EditToolCall: &agentv1.EditToolCall{Args: editArgsFromMap(args, "")}}}
	case "WebSearch":
		return &agentv1.ToolCall{Tool: &agentv1.ToolCall_WebSearchToolCall{WebSearchToolCall: &agentv1.WebSearchToolCall{Args: &agentv1.WebSearchArgs{SearchTerm: firstNonEmpty(argString(args, "search_term"), argString(args, "query"))}}}}
	case "Task":
		return &agentv1.ToolCall{Tool: &agentv1.ToolCall_TaskToolCall{TaskToolCall: &agentv1.TaskToolCall{Args: &agentv1.TaskArgs{Description: argString(args, "description"), Prompt: argString(args, "prompt")}}}}
	case "SwitchMode":
		return &agentv1.ToolCall{Tool: &agentv1.ToolCall_SwitchModeToolCall{SwitchModeToolCall: &agentv1.SwitchModeToolCall{Args: &agentv1.SwitchModeArgs{TargetModeId: argString(args, "target_mode_id")}}}}
	case "AskQuestion":
		return &agentv1.ToolCall{Tool: &agentv1.ToolCall_AskQuestionToolCall{AskQuestionToolCall: &agentv1.AskQuestionToolCall{Args: &agentv1.AskQuestionArgs{Title: argString(args, "title")}}}}
	case "CallMcpTool":
		return &agentv1.ToolCall{Tool: &agentv1.ToolCall_McpToolCall{McpToolCall: &agentv1.McpToolCall{Args: mcpArgsFromMap(args)}}}
	case "FetchMcpResource":
		toolArgs := &agentv1.ReadMcpResourceExecArgs{Server: argString(args, "server"), Uri: argString(args, "uri")}
		if downloadPath := firstNonEmpty(argString(args, "download_path"), argString(args, "downloadPath")); downloadPath != "" {
			toolArgs.DownloadPath = proto.String(downloadPath)
		}
		return &agentv1.ToolCall{Tool: &agentv1.ToolCall_ReadMcpResourceToolCall{ReadMcpResourceToolCall: &agentv1.ReadMcpResourceToolCall{Args: toolArgs}}}
	default:
		return &agentv1.ToolCall{}
	}
}

func readToolArgsFromMap(args map[string]any) *agentv1.ReadToolArgs {
	out := &agentv1.ReadToolArgs{Path: argString(args, "path")}
	if v, ok := optionalInt32(args, "offset"); ok {
		out.Offset = proto.Int32(v)
	}
	if v, ok := optionalInt32(args, "limit"); ok {
		out.Limit = proto.Int32(v)
	}
	if value, ok := args["include_line_numbers"]; ok {
		out.IncludeLineNumbers = proto.Bool(argBoolValue(value, false))
	}
	return out
}

func grepToolArgsFromMap(args map[string]any) *agentv1.GrepArgs {
	out := &agentv1.GrepArgs{Pattern: argString(args, "pattern")}
	if v := argString(args, "path"); v != "" {
		out.Path = proto.String(v)
	}
	if v := argString(args, "glob"); v != "" {
		out.Glob = proto.String(v)
	}
	if v := argString(args, "output_mode"); v != "" {
		out.OutputMode = proto.String(v)
	}
	if v, ok := optionalInt32(args, "head_limit"); ok {
		out.HeadLimit = proto.Int32(v)
	}
	if value, ok := args["case_insensitive"]; ok {
		out.CaseInsensitive = proto.Bool(argBoolValue(value, false))
	} else if value, ok := args["-i"]; ok {
		out.CaseInsensitive = proto.Bool(argBoolValue(value, false))
	}
	return out
}

func lsToolArgsFromMap(args map[string]any, workspaceRoot string) *agentv1.LsArgs {
	pathValue := firstNonEmpty(argString(args, "path"), argString(args, "target_directory"), workspaceRoot)
	resolved, err := resolveToolPath(pathValue, workspaceRoot)
	if err != nil || resolved == "" {
		resolved = pathValue
	}
	out := &agentv1.LsArgs{Path: resolved, Ignore: defaultAgentLsIgnore(), TimeoutMs: proto.Uint32(10000)}
	if v := argString(args, "tool_call_id"); v != "" {
		out.ToolCallId = v
	}
	return out
}

func writeShellStdinArgsFromMap(args map[string]any) *agentv1.WriteShellStdinArgs {
	return &agentv1.WriteShellStdinArgs{
		ShellId: uint32(argInt(args, "shell_id", argInt(args, "shellId", 0))),
		Chars:   firstNonEmpty(argString(args, "chars"), argString(args, "input"), argString(args, "stdin")),
	}
}

func editArgsFromMap(args map[string]any, workspaceRoot string) *agentv1.EditArgs {
	pathValue := argString(args, "path")
	resolved, err := resolveToolPath(pathValue, workspaceRoot)
	if err != nil || resolved == "" {
		resolved = pathValue
	}
	streamContent := firstNonEmpty(
		argString(args, "stream_content"),
		argString(args, "streamContent"),
		argString(args, "new_string"),
		argString(args, "contents"),
		argString(args, "file_text"),
		argString(args, "content"),
	)
	out := &agentv1.EditArgs{Path: resolved}
	if streamContent != "" {
		out.StreamContent = proto.String(streamContent)
	}
	return out
}

func deleteArgsFromMap(args map[string]any, workspaceRoot string) *agentv1.DeleteArgs {
	pathValue := argString(args, "path")
	resolved, err := resolveToolPath(pathValue, workspaceRoot)
	if err != nil || resolved == "" {
		resolved = pathValue
	}
	return &agentv1.DeleteArgs{Path: resolved, ToolCallId: argString(args, "tool_call_id")}
}

func readLintsArgsFromMap(args map[string]any, workspaceRoot string) *agentv1.ReadLintsToolArgs {
	paths := []string{}
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		resolved, err := resolveToolPath(path, workspaceRoot)
		if err != nil || resolved == "" {
			resolved = path
		}
		paths = append(paths, resolved)
	}
	if raw, ok := args["paths"]; ok {
		switch v := raw.(type) {
		case []any:
			for _, item := range v {
				add(strings.TrimSpace(fmt.Sprint(item)))
			}
		case []string:
			for _, item := range v {
				add(item)
			}
		case string:
			for _, item := range strings.Split(v, ",") {
				add(item)
			}
		}
	}
	add(firstNonEmpty(argString(args, "path"), argString(args, "file")))
	if len(paths) == 0 && workspaceRoot != "" {
		add(workspaceRoot)
	}
	return &agentv1.ReadLintsToolArgs{Paths: dedupeStrings(paths)}
}

func webFetchArgsFromMap(args map[string]any) *agentv1.WebFetchArgs {
	return &agentv1.WebFetchArgs{Url: firstNonEmpty(argString(args, "url"), argString(args, "uri")), ToolCallId: argString(args, "tool_call_id")}
}

func updateTodosArgsFromMap(args map[string]any) *agentv1.UpdateTodosArgs {
	out := &agentv1.UpdateTodosArgs{Merge: argBool(args, "merge", false)}
	raw, ok := args["todos"]
	if !ok || raw == nil {
		return out
	}
	items, ok := raw.([]any)
	if !ok {
		return out
	}
	now := time.Now().UnixMilli()
	for i, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			content := strings.TrimSpace(fmt.Sprint(item))
			if content == "" {
				continue
			}
			out.Todos = append(out.Todos, &agentv1.TodoItem{Id: fmt.Sprintf("todo-%d", i+1), Content: content, Status: agentv1.TodoStatus_TODO_STATUS_PENDING, CreatedAt: now, UpdatedAt: now})
			continue
		}
		id := firstNonEmpty(argString(m, "id"), fmt.Sprintf("todo-%d", i+1))
		createdAt := argInt(m, "created_at", now)
		updatedAt := argInt(m, "updated_at", now)
		out.Todos = append(out.Todos, &agentv1.TodoItem{
			Id:           id,
			Content:      firstNonEmpty(argString(m, "content"), argString(m, "text"), argString(m, "title")),
			Status:       todoStatusFromString(argString(m, "status")),
			CreatedAt:    createdAt,
			UpdatedAt:    updatedAt,
			Dependencies: stringSliceFromAny(m["dependencies"]),
		})
	}
	return out
}

func createPlanArgsFromMap(args map[string]any) *agentv1.CreatePlanArgs {
	out := &agentv1.CreatePlanArgs{
		Name:      firstNonEmpty(argString(args, "name"), "Plan"),
		Overview:  argString(args, "overview"),
		Plan:      argString(args, "plan"),
		IsProject: argBool(args, "is_project", false),
	}
	now := time.Now().UnixMilli()
	out.Todos = todoItemsFromAny(args["todos"], now)
	if rawPhases, ok := args["phases"].([]any); ok {
		for _, raw := range rawPhases {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			phase := &agentv1.Phase{
				Name:  firstNonEmpty(argString(m, "name"), argString(m, "title")),
				Todos: todoItemsFromAny(m["todos"], now),
			}
			if phase.Name != "" || len(phase.Todos) > 0 {
				out.Phases = append(out.Phases, phase)
			}
		}
	}
	return out
}

func todoItemsFromAny(raw any, now int64) []*agentv1.TodoItem {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]*agentv1.TodoItem, 0, len(items))
	for i, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			content := strings.TrimSpace(fmt.Sprint(item))
			if content == "" {
				continue
			}
			out = append(out, &agentv1.TodoItem{Id: fmt.Sprintf("todo-%d", i+1), Content: content, Status: agentv1.TodoStatus_TODO_STATUS_PENDING, CreatedAt: now, UpdatedAt: now})
			continue
		}
		out = append(out, &agentv1.TodoItem{
			Id:           firstNonEmpty(argString(m, "id"), fmt.Sprintf("todo-%d", i+1)),
			Content:      firstNonEmpty(argString(m, "content"), argString(m, "text"), argString(m, "title")),
			Status:       todoStatusFromString(argString(m, "status")),
			CreatedAt:    argInt(m, "created_at", now),
			UpdatedAt:    argInt(m, "updated_at", now),
			Dependencies: stringSliceFromAny(m["dependencies"]),
		})
	}
	return out
}

func createPlanURI(name string, workspaceRoot string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "plan"
	}
	slug := strings.ToLower(name)
	slug = regexp.MustCompile(`[^a-z0-9._-]+`).ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-.")
	if slug == "" {
		slug = "plan"
	}
	base := normalizeWorkspaceRoot(workspaceRoot)
	if base == "" {
		base = os.TempDir()
	}
	return workspaceURI(filepath.Join(base, ".cursor", "plans", slug+".md"))
}

func mcpArgsFromMap(args map[string]any) *agentv1.McpArgs {
	out := &agentv1.McpArgs{
		Name:               firstNonEmpty(argString(args, "name"), argString(args, "server"), argString(args, "server_name")),
		ToolCallId:         argString(args, "tool_call_id"),
		ProviderIdentifier: firstNonEmpty(argString(args, "provider_identifier"), argString(args, "provider"), argString(args, "server")),
		ToolName:           firstNonEmpty(argString(args, "tool_name"), argString(args, "tool"), argString(args, "name")),
		Args:               map[string]*structpb.Value{},
	}
	raw := args["args"]
	if raw == nil {
		raw = args["arguments"]
	}
	if m, ok := raw.(map[string]any); ok {
		for key, value := range m {
			pbValue, err := structpb.NewValue(value)
			if err != nil {
				pbValue = structpb.NewStringValue(fmt.Sprint(value))
			}
			out.Args[key] = pbValue
		}
	}
	return out
}

func editResultFromWriteResult(path string, result *agentv1.WriteResult, text string) *agentv1.EditResult {
	if result == nil {
		return &agentv1.EditResult{Result: &agentv1.EditResult_Error{Error: &agentv1.EditError{Path: path, Error: text, ModelVisibleError: proto.String(text)}}}
	}
	if success := result.GetSuccess(); success != nil {
		content := success.GetFileContentAfterWrite()
		message := firstNonEmpty(text, fmt.Sprintf("wrote %s (%d bytes)", success.GetPath(), success.GetFileSize()))
		return &agentv1.EditResult{Result: &agentv1.EditResult_Success{Success: &agentv1.EditSuccess{Path: firstNonEmpty(success.GetPath(), path), LinesAdded: proto.Int32(success.GetLinesCreated()), AfterFullFileContent: content, Message: proto.String(message)}}}
	}
	if denied := result.GetPermissionDenied(); denied != nil {
		return &agentv1.EditResult{Result: &agentv1.EditResult_WritePermissionDenied{WritePermissionDenied: &agentv1.EditWritePermissionDenied{Path: firstNonEmpty(denied.GetPath(), path)}}}
	}
	if rejected := result.GetRejected(); rejected != nil {
		return &agentv1.EditResult{Result: &agentv1.EditResult_Rejected{Rejected: &agentv1.EditRejected{Path: firstNonEmpty(rejected.GetPath(), path), Reason: rejected.GetReason()}}}
	}
	if err := result.GetError(); err != nil {
		return &agentv1.EditResult{Result: &agentv1.EditResult_Error{Error: &agentv1.EditError{Path: firstNonEmpty(err.GetPath(), path), Error: err.GetError(), ModelVisibleError: proto.String(err.GetError())}}}
	}
	if noSpace := result.GetNoSpace(); noSpace != nil {
		message := "no space left writing " + firstNonEmpty(noSpace.GetPath(), path)
		return &agentv1.EditResult{Result: &agentv1.EditResult_Error{Error: &agentv1.EditError{Path: firstNonEmpty(noSpace.GetPath(), path), Error: message, ModelVisibleError: proto.String(message)}}}
	}
	return &agentv1.EditResult{Result: &agentv1.EditResult_Error{Error: &agentv1.EditError{Path: path, Error: text, ModelVisibleError: proto.String(text)}}}
}

func readLintsResultFromDiagnostics(paths []string, result *agentv1.DiagnosticsResult) *agentv1.ReadLintsToolResult {
	if result == nil {
		return &agentv1.ReadLintsToolResult{Result: &agentv1.ReadLintsToolResult_Error{Error: &agentv1.ReadLintsToolError{ErrorMessage: "empty diagnostics result"}}}
	}
	if success := result.GetSuccess(); success != nil {
		path := firstNonEmpty(success.GetPath(), firstString(paths))
		items := make([]*agentv1.DiagnosticItem, 0, len(success.GetDiagnostics()))
		for _, diag := range success.GetDiagnostics() {
			if diag == nil {
				continue
			}
			items = append(items, &agentv1.DiagnosticItem{Severity: diag.GetSeverity(), Range: diagnosticRangeFromRange(diag.GetRange()), Message: diag.GetMessage(), Source: diag.GetSource(), Code: diag.GetCode(), IsStale: diag.GetIsStale()})
		}
		files := []*agentv1.FileDiagnostics{{Path: path, Diagnostics: items, DiagnosticsCount: int32(len(items))}}
		return &agentv1.ReadLintsToolResult{Result: &agentv1.ReadLintsToolResult_Success{Success: &agentv1.ReadLintsToolSuccess{FileDiagnostics: files, TotalFiles: int32(len(files)), TotalDiagnostics: success.GetTotalDiagnostics()}}}
	}
	text := "diagnostics failed"
	if err := result.GetError(); err != nil {
		text = err.GetError()
	} else if rejected := result.GetRejected(); rejected != nil {
		text = rejected.GetReason()
	} else if missing := result.GetFileNotFound(); missing != nil {
		text = "file not found: " + missing.GetPath()
	} else if denied := result.GetPermissionDenied(); denied != nil {
		text = "permission denied: " + denied.GetPath()
	}
	return &agentv1.ReadLintsToolResult{Result: &agentv1.ReadLintsToolResult_Error{Error: &agentv1.ReadLintsToolError{ErrorMessage: text}}}
}

func webFetchResultFromFetch(result *agentv1.FetchResult) *agentv1.WebFetchResult {
	if result == nil {
		return &agentv1.WebFetchResult{Result: &agentv1.WebFetchResult_Error{Error: &agentv1.WebFetchError{Error: "empty fetch result"}}}
	}
	if success := result.GetSuccess(); success != nil {
		return &agentv1.WebFetchResult{Result: &agentv1.WebFetchResult_Success{Success: &agentv1.WebFetchSuccess{Url: success.GetUrl(), Markdown: success.GetContent()}}}
	}
	if err := result.GetError(); err != nil {
		return &agentv1.WebFetchResult{Result: &agentv1.WebFetchResult_Error{Error: &agentv1.WebFetchError{Error: err.GetError()}}}
	}
	return &agentv1.WebFetchResult{Result: &agentv1.WebFetchResult_Error{Error: &agentv1.WebFetchError{Error: "fetch failed"}}}
}

func lineDelta(oldString string, newString string) (int, int) {
	oldLines := len(splitLines(oldString))
	newLines := len(splitLines(newString))
	if oldString == "" {
		oldLines = 0
	}
	if newString == "" {
		newLines = 0
	}
	return maxInt(newLines-oldLines, 0), maxInt(oldLines-newLines, 0)
}

func countIfReplaceAll(count int, replaceAll bool) int {
	if replaceAll {
		return count
	}
	if count > 0 {
		return 1
	}
	return 0
}

func dedupeStrings(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out
}

func firstString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func stringSliceFromAny(value any) []string {
	switch v := value.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" {
				out = append(out, text)
			}
		}
		return out
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			if text := strings.TrimSpace(part); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func todoStatusFromString(value string) agentv1.TodoStatus {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "pending", "todo", "open":
		return agentv1.TodoStatus_TODO_STATUS_PENDING
	case "in_progress", "in-progress", "doing", "active":
		return agentv1.TodoStatus_TODO_STATUS_IN_PROGRESS
	case "completed", "complete", "done":
		return agentv1.TodoStatus_TODO_STATUS_COMPLETED
	case "cancelled", "canceled", "cancel":
		return agentv1.TodoStatus_TODO_STATUS_CANCELLED
	default:
		return agentv1.TodoStatus_TODO_STATUS_UNSPECIFIED
	}
}

func diagnosticRangeFromRange(r *agentv1.Range) *agentv1.DiagnosticRange {
	if r == nil {
		return nil
	}
	return &agentv1.DiagnosticRange{Start: r.GetStart(), End: r.GetEnd()}
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

func defaultAgentLsIgnore() []string {
	return []string{".git", "node_modules", ".next", "dist", "build", ".cache", "vendor"}
}

func writeAgentPartialToolCallFrame(w io.Writer, callID string, modelCallID string, toolCall *agentv1.ToolCall, argsDelta string) error {
	return writeAgentInteractionUpdateFrame(w, &agentv1.InteractionUpdate{Message: &agentv1.InteractionUpdate_PartialToolCall{PartialToolCall: &agentv1.PartialToolCallUpdate{CallId: callID, ToolCall: toolCall, ArgsTextDelta: argsDelta, ModelCallId: modelCallID}}})
}

func writeAgentToolCallStartedFrame(w io.Writer, callID string, modelCallID string, toolCall *agentv1.ToolCall) error {
	return writeAgentInteractionUpdateFrame(w, &agentv1.InteractionUpdate{Message: &agentv1.InteractionUpdate_ToolCallStarted{ToolCallStarted: &agentv1.ToolCallStartedUpdate{CallId: callID, ToolCall: toolCall, ModelCallId: modelCallID}}})
}

func writeAgentToolCallCompletedFrame(w io.Writer, callID string, modelCallID string, toolCall *agentv1.ToolCall) error {
	return writeAgentInteractionUpdateFrame(w, &agentv1.InteractionUpdate{Message: &agentv1.InteractionUpdate_ToolCallCompleted{ToolCallCompleted: &agentv1.ToolCallCompletedUpdate{CallId: callID, ToolCall: toolCall, ModelCallId: modelCallID}}})
}

func workspaceRootFromAgentRunRequest(req *agentv1.AgentRunRequest) string {
	if req == nil {
		return ""
	}
	if root := workspaceRootFromConversationState(req.GetConversationState()); root != "" {
		return root
	}
	return workspaceRootFromConversationAction(req.GetAction())
}

func workspaceRootFromConversationAction(action *agentv1.ConversationAction) string {
	if action == nil {
		return ""
	}
	if uma := action.GetUserMessageAction(); uma != nil {
		if root := workspaceRootFromRequestContext(uma.GetRequestContext()); root != "" {
			return root
		}
		if msg := uma.GetUserMessage(); msg != nil {
			if root := workspaceRootFromSelectedContext(msg.GetSelectedContext()); root != "" {
				return root
			}
		}
	}
	if start := action.GetStartPlanAction(); start != nil {
		return workspaceRootFromRequestContext(start.GetRequestContext())
	}
	if exec := action.GetExecutePlanAction(); exec != nil {
		return workspaceRootFromRequestContext(exec.GetRequestContext())
	}
	if resume := action.GetResumeAction(); resume != nil {
		return workspaceRootFromRequestContext(resume.GetRequestContext())
	}
	return ""
}

func workspaceRootFromConversationState(state *agentv1.ConversationStateStructure) string {
	if state == nil {
		return ""
	}
	candidates := []string{}
	for _, uri := range state.GetPreviousWorkspaceUris() {
		candidates = append(candidates, uri)
	}
	return bestWorkspaceRoot(candidates...)
}

func workspaceRootFromRequestContext(ctx *agentv1.RequestContext) string {
	if ctx == nil {
		return ""
	}
	candidates := []string{}
	if env := ctx.GetEnv(); env != nil {
		for _, p := range env.GetWorkspacePaths() {
			candidates = append(candidates, p)
		}
		// Cursor's project_folder can point at ~/.cursor/projects/<slug>, so keep it
		// after the real workspace candidates.
		candidates = append(candidates, env.GetProjectFolder())
	}
	for _, repo := range ctx.GetRepositoryInfo() {
		candidates = append(candidates, repo.GetWorkspaceUri())
	}
	for _, repo := range ctx.GetGitRepos() {
		candidates = append(candidates, repo.GetPath())
	}
	if opts := ctx.GetMcpFileSystemOptions(); opts != nil {
		candidates = append(candidates, opts.GetWorkspaceProjectDir())
	}
	return bestWorkspaceRoot(candidates...)
}

func workspaceRootFromSelectedContext(ctx *agentv1.SelectedContext) string {
	if ctx == nil {
		return ""
	}
	candidates := []string{}
	for _, folder := range ctx.GetFolders() {
		candidates = append(candidates, folder.GetPath())
	}
	for _, file := range ctx.GetFiles() {
		if file.GetPath() != "" {
			candidates = append(candidates, filepath.Dir(file.GetPath()))
		}
	}
	return bestWorkspaceRoot(candidates...)
}

func bestWorkspaceRoot(values ...string) string {
	fallback := ""
	for _, value := range values {
		root := normalizeWorkspaceRoot(value)
		if root == "" {
			continue
		}
		if fallback == "" {
			fallback = root
		}
		if !isCursorInternalProjectPath(root) && directoryExists(root) {
			return root
		}
	}
	return fallback
}

func normalizeWorkspaceRoot(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if path := fileURIToPath(value); path != "" {
		value = path
	}
	value = filepath.FromSlash(value)
	if !filepath.IsAbs(value) {
		return value
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return value
	}
	if mapped := resolveCursorInternalProjectPath(abs); mapped != "" {
		return mapped
	}
	return abs
}

func fileURIToPath(value string) string {
	if value == "" || !strings.HasPrefix(strings.ToLower(value), "file:") {
		return ""
	}
	u, err := url.Parse(value)
	if err != nil {
		return ""
	}
	path := u.Path
	if u.Host != "" {
		path = "//" + u.Host + path
	}
	if decoded, err := url.PathUnescape(path); err == nil {
		path = decoded
	}
	if len(path) >= 3 && path[0] == '/' && path[2] == ':' {
		path = path[1:]
	}
	return filepath.FromSlash(path)
}

func resolveToolPath(path string, workspaceRoot string) (string, error) {
	path = strings.TrimSpace(path)
	workspaceRoot = normalizeWorkspaceRoot(workspaceRoot)
	if path == "" {
		if workspaceRoot == "" {
			return "", fmt.Errorf("path is required")
		}
		path = workspaceRoot
	}
	path = filepath.FromSlash(path)
	if !filepath.IsAbs(path) {
		if workspaceRoot == "" {
			if wd, err := os.Getwd(); err == nil {
				workspaceRoot = wd
			}
		}
		path = filepath.Join(workspaceRoot, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if mapped := resolveCursorInternalProjectPath(abs); mapped != "" {
		return filepath.Abs(mapped)
	}
	return abs, nil
}

func resolveCursorInternalProjectPath(path string) string {
	projectName, rel, ok := cursorInternalProjectParts(path)
	if !ok || projectName == "" {
		return ""
	}
	projectSlug := strings.ToLower(projectName)
	for _, candidate := range cursorWorkspaceCandidates() {
		if cursorProjectSlug(candidate) != projectSlug {
			continue
		}
		if rel == "" || rel == "." {
			return candidate
		}
		return filepath.Join(candidate, rel)
	}
	return ""
}

func isCursorInternalProjectPath(path string) bool {
	_, _, ok := cursorInternalProjectParts(path)
	return ok
}

func cursorInternalProjectParts(path string) (string, string, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", "", false
	}
	path = filepath.FromSlash(path)
	if !filepath.IsAbs(path) {
		return "", "", false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", "", false
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", "", false
	}
	base := filepath.Join(home, ".cursor", "projects")
	rel, err := filepath.Rel(base, abs)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return "", "", false
	}
	remainder := ""
	if len(parts) > 1 {
		remainder = filepath.Join(parts[1:]...)
	}
	return parts[0], remainder, true
}

func cursorWorkspaceCandidates() []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(value string) {
		for _, candidate := range workspaceCandidatePaths(value) {
			key := strings.ToLower(candidate)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, candidate)
		}
	}

	if appData := os.Getenv("APPDATA"); appData != "" {
		workspaceStorage := filepath.Join(appData, "Cursor", "User", "workspaceStorage")
		entries, err := os.ReadDir(workspaceStorage)
		if err == nil {
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				collectWorkspaceCandidatesFromJSONFile(filepath.Join(workspaceStorage, entry.Name(), "workspace.json"), add)
			}
		}
		collectWorkspaceCandidatesFromJSONFile(filepath.Join(appData, "Cursor", "User", "globalStorage", "storage.json"), add)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		collectWorkspaceCandidatesFromJSONFile(filepath.Join(home, ".cursor", "ide_state.json"), add)
		collectWorkspaceCandidatesFromJSONFile(filepath.Join(home, ".cursor", "unified_repo_list.json"), add)
	}
	return out
}

func collectWorkspaceCandidatesFromJSONFile(path string, add func(string)) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return
	}
	collectWorkspaceCandidateStrings(value, add)
}

func collectWorkspaceCandidateStrings(value any, add func(string)) {
	switch v := value.(type) {
	case string:
		add(v)
	case []any:
		for _, item := range v {
			collectWorkspaceCandidateStrings(item, add)
		}
	case map[string]any:
		for key, item := range v {
			add(key)
			collectWorkspaceCandidateStrings(item, add)
		}
	}
}

func workspaceCandidatePaths(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if path := fileURIToPath(value); path != "" {
		value = path
	}
	value = filepath.FromSlash(value)
	if !filepath.IsAbs(value) {
		return nil
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return nil
	}
	if isCursorInternalProjectPath(abs) {
		return nil
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil
	}
	if !info.IsDir() {
		abs = filepath.Dir(abs)
	}
	paths := []string{}
	for {
		if abs == "" || abs == "." || isCursorInternalProjectPath(abs) {
			break
		}
		if directoryExists(abs) {
			paths = append(paths, abs)
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			break
		}
		abs = parent
	}
	return paths
}

func cursorProjectSlug(path string) string {
	path = filepath.FromSlash(strings.TrimSpace(path))
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	volume := strings.TrimSuffix(filepath.VolumeName(path), ":")
	parts := []string{}
	appendASCIIWords := func(value string) {
		var b strings.Builder
		flush := func() {
			if b.Len() == 0 {
				return
			}
			parts = append(parts, strings.ToLower(b.String()))
			b.Reset()
		}
		for _, r := range value {
			switch {
			case r >= 'a' && r <= 'z':
				b.WriteRune(r)
			case r >= 'A' && r <= 'Z':
				b.WriteRune(r + ('a' - 'A'))
			case r >= '0' && r <= '9':
				b.WriteRune(r)
			default:
				flush()
			}
		}
		flush()
	}
	appendASCIIWords(volume)
	trimmed := strings.TrimPrefix(path, filepath.VolumeName(path))
	for _, part := range strings.FieldsFunc(trimmed, func(r rune) bool { return r == '\\' || r == '/' }) {
		appendASCIIWords(part)
	}
	return strings.Join(parts, "-")
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func splitLines(content string) []string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.TrimSuffix(content, "\n")
	if content == "" {
		return nil
	}
	return strings.Split(content, "\n")
}

func addLineNumbers(content string, start int) string {
	lines := splitLines(content)
	if len(lines) == 0 {
		return ""
	}
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = fmt.Sprintf("%6d|%s", start+i, line)
	}
	return strings.Join(out, "\n")
}

func globFiles(root string, pattern string, limit int) ([]string, bool, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, false, err
	}
	if !info.IsDir() {
		root = filepath.Dir(root)
	}
	matcher, err := globMatcher(pattern)
	if err != nil {
		return nil, false, err
	}
	files := []string{}
	truncated := false
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && shouldSkipDir(d.Name()) && path != root {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if matcher(rel) || matcher(filepath.Base(path)) {
			files = append(files, path)
			if len(files) >= limit {
				truncated = true
				return io.EOF
			}
		}
		return nil
	})
	if err == io.EOF {
		err = nil
	}
	sort.Strings(files)
	return files, truncated, err
}

type grepMatch struct {
	File       string
	LineNumber int
	Content    string
}

func grepFiles(root string, pattern string, globPattern string, caseInsensitive bool, limit int) ([]grepMatch, int, bool, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, 0, false, err
	}
	flags := ""
	if caseInsensitive {
		flags = "(?i)"
	}
	re, err := regexp.Compile(flags + pattern)
	if err != nil {
		re = regexp.MustCompile(flags + regexp.QuoteMeta(pattern))
	}
	var matcher func(string) bool
	if globPattern != "" {
		matcher, err = globMatcher(globPattern)
		if err != nil {
			return nil, 0, false, err
		}
	}
	matches := []grepMatch{}
	totalLines := 0
	truncated := false
	searchFile := func(path string) error {
		if isLikelyBinary(path) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		lines := splitLines(string(data))
		totalLines += len(lines)
		for i, line := range lines {
			if re.MatchString(line) {
				matches = append(matches, grepMatch{File: path, LineNumber: i + 1, Content: line})
				if len(matches) >= limit {
					truncated = true
					return io.EOF
				}
			}
		}
		return nil
	}
	if !info.IsDir() {
		if matcher != nil && !matcher(filepath.ToSlash(filepath.Base(root))) {
			return nil, 0, false, nil
		}
		return matches, totalLines, truncated, searchFile(root)
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && shouldSkipDir(d.Name()) && path != root {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		if matcher != nil {
			rel, _ := filepath.Rel(root, path)
			if !matcher(filepath.ToSlash(rel)) && !matcher(filepath.Base(path)) {
				return nil
			}
		}
		return searchFile(path)
	})
	if err == io.EOF {
		err = nil
	}
	return matches, totalLines, truncated, err
}

func globMatcher(pattern string) (func(string) bool, error) {
	pattern = filepath.ToSlash(strings.TrimSpace(pattern))
	if pattern == "" {
		return nil, fmt.Errorf("glob pattern is required")
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		ch := pattern[i]
		switch ch {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '/', '\\':
			b.WriteString("/")
		default:
			b.WriteString(regexp.QuoteMeta(string(ch)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, err
	}
	return func(path string) bool { return re.MatchString(filepath.ToSlash(path)) }, nil
}

func shouldSkipDir(name string) bool {
	switch strings.ToLower(name) {
	case ".git", "node_modules", ".next", "dist", "build", ".cache", "vendor":
		return true
	default:
		return false
	}
}

func isLikelyBinary(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".exe", ".dll", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".pdf", ".zip", ".7z", ".rar", ".tar", ".gz", ".db", ".sqlite":
		return true
	default:
		return false
	}
}

func canonicalAgentToolDefinitionName(name string) string {
	if normalizeAgentToolName(name) == "PatchEdit" {
		return "StrReplace"
	}
	return normalizeAgentToolName(name)
}

func isAllowedAgentToolName(name string) bool {
	return allowedAgentTools[canonicalAgentToolDefinitionName(name)] || allowedAgentTools[normalizeAgentToolName(name)]
}

func providerVisibleAgentToolName(name string) string {
	return canonicalAgentToolDefinitionName(name)
}

func normalizeAgentToolName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "read", "read_file", "readfile":
		return "Read"
	case "grep", "search", "search_files":
		return "Grep"
	case "glob", "find_files":
		return "Glob"
	case "shell", "bash", "run_command", "terminal":
		return "Shell"
	case "ls", "list", "list_directory":
		return "Ls"
	case "strreplace", "str_replace", "edit", "patch", "patch_edit", "patchedit":
		return "PatchEdit"
	case "read_lints", "readlints", "diagnostics":
		return "ReadLints"
	case "web_fetch", "webfetch", "fetch":
		return "WebFetch"
	case "write_shell_stdin", "writeshellstdin":
		return "WriteShellStdin"
	case "force_background_shell", "forcebackgroundshell", "background_shell":
		return "ForceBackgroundShell"
	case "todo_write", "todowrite", "update_todos", "updatetodos":
		return "TodoWrite"
	case "web_search", "websearch":
		return "WebSearch"
	case "ask_question", "askquestion":
		return "AskQuestion"
	case "create_plan", "createplan":
		return "CreatePlan"
	case "call_mcp_tool", "callmcptool":
		return "CallMcpTool"
	case "fetch_mcp_resource", "fetchmcpresource":
		return "FetchMcpResource"
	case "switch_mode", "switchmode":
		return "SwitchMode"
	case "task":
		return "Task"
	case "write":
		return "Write"
	case "delete":
		return "Delete"
	default:
		return strings.TrimSpace(name)
	}
}

func argString(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	value, ok := args[key]
	if !ok || value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func argInt(args map[string]any, key string, fallback int64) int64 {
	if args == nil {
		return fallback
	}
	value, ok := args[key]
	if !ok || value == nil {
		return fallback
	}
	switch v := value.(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n
		}
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return n
		}
	}
	return fallback
}

func optionalInt32(args map[string]any, key string) (int32, bool) {
	if _, ok := args[key]; !ok {
		return 0, false
	}
	return int32(argInt(args, key, 0)), true
}

func argBool(args map[string]any, key string, fallback bool) bool {
	if args == nil {
		return fallback
	}
	value, ok := args[key]
	if !ok {
		return fallback
	}
	return argBoolValue(value, fallback)
}

func argBoolValue(value any, fallback bool) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(v))
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
