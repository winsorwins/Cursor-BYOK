package relay

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/burpheart/cursor-tap/cursor_proto/gen/agent/v1"
	"google.golang.org/protobuf/proto"
)

func TestCursorProjectSlugSkipsNonASCIIPathParts(t *testing.T) {
	path := filepath.Join("D:\\", "win", "win项目", "开发模板", "new项目")

	if got, want := cursorProjectSlug(path), "d-win-win-new"; got != want {
		t.Fatalf("cursorProjectSlug() = %q, want %q", got, want)
	}
}

func TestNormalizeAgentToolArgsFallsBackToWorkspaceForMissingDirectory(t *testing.T) {
	workspace := t.TempDir()
	missing := filepath.Join(t.TempDir(), "missing-workspace")
	args := normalizeAgentToolArgs("Glob", map[string]any{
		"glob_pattern":     "**/*",
		"target_directory": missing,
	}, workspace)

	if got := argString(args, "target_directory"); got != workspace {
		t.Fatalf("target_directory = %q, want %q", got, workspace)
	}
}

func TestBestWorkspaceRootPrefersRealWorkspaceOverInternalProject(t *testing.T) {
	workspace := t.TempDir()
	internal := filepath.Join(t.TempDir(), ".cursor", "projects", "d-win-win-new")

	if got := bestWorkspaceRoot(internal, workspace); got != workspace {
		t.Fatalf("bestWorkspaceRoot() = %q, want %q", got, workspace)
	}
}

func TestOpenAIResponsesInputPreservesStructuredToolResult(t *testing.T) {
	messages := []chatMessage{
		{Role: "user", Content: "inspect"},
		{ToolResult: &chatToolResult{ID: "call_1", Name: "Read", Arguments: `{"path":"main.go"}`, Output: "package main"}},
	}

	input := openAIResponsesInput(messages)
	if len(input) != 3 {
		t.Fatalf("input length = %d, want 3", len(input))
	}
	call, _ := json.Marshal(input[1])
	if !containsBytes(call, []byte(`"type":"function_call"`)) || !containsBytes(call, []byte(`"call_id":"call_1"`)) {
		t.Fatalf("missing function_call item: %s", call)
	}
	result, _ := json.Marshal(input[2])
	if !containsBytes(result, []byte(`"type":"function_call_output"`)) || !containsBytes(result, []byte(`package main`)) {
		t.Fatalf("missing function_call_output item: %s", result)
	}
}

func TestCompactAgentMessagesForFinalTruncatesToolResults(t *testing.T) {
	longOutput := strings.Repeat("x", agentFinalToolResultLimit+100)
	messages := []chatMessage{
		{Role: "user", Content: "inspect"},
		{ToolResult: &chatToolResult{ID: "call_1", Name: "Read", Arguments: `{"path":"main.go"}`, Output: longOutput}},
	}

	compact := compactAgentMessagesForFinal(messages)
	if got := compact[1].ToolResult.Output; len(got) >= len(longOutput) {
		t.Fatalf("compact output length = %d, want less than %d", len(got), len(longOutput))
	}
	if !containsBytes([]byte(compact[1].ToolResult.Output), []byte("...[tool result truncated]")) {
		t.Fatalf("expected truncation marker, got %q", compact[1].ToolResult.Output)
	}
	if messages[1].ToolResult.Output != longOutput {
		t.Fatal("compactAgentMessagesForFinal mutated the original tool result")
	}
}

func TestAgentToolNamesExposeStrReplaceButExecuteAsPatchEdit(t *testing.T) {
	if got := canonicalAgentToolDefinitionName("PatchEdit"); got != "StrReplace" {
		t.Fatalf("canonicalAgentToolDefinitionName(PatchEdit) = %q, want StrReplace", got)
	}
	if got := canonicalAgentToolDefinitionName("StrReplace"); got != "StrReplace" {
		t.Fatalf("canonicalAgentToolDefinitionName(StrReplace) = %q, want StrReplace", got)
	}
	if got := normalizeAgentToolName("StrReplace"); got != "PatchEdit" {
		t.Fatalf("normalizeAgentToolName(StrReplace) = %q, want PatchEdit", got)
	}
}

func TestAgentRequestNeedsEditForChineseOptimizeRequest(t *testing.T) {
	messages := []chatMessage{{Role: "user", Content: "帮我优化这个文件里的代码"}}
	if !agentRequestNeedsEdit(messages) {
		t.Fatal("expected optimize request to require an edit")
	}
}

func TestAgentToolResultLooksSuccessfulEdit(t *testing.T) {
	success := agentToolExecution{Call: agentToolCall{Name: "StrReplace"}, ResultText: "replaced 1 occurrence(s) in main.go"}
	if !agentToolResultLooksSuccessfulEdit(success) {
		t.Fatal("expected successful StrReplace result")
	}
	failure := agentToolExecution{Call: agentToolCall{Name: "StrReplace"}, ResultText: "old_string was not found"}
	if agentToolResultLooksSuccessfulEdit(failure) {
		t.Fatal("expected failed StrReplace result")
	}
}

func TestExecuteReadToolPrefersFileSyncCache(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "main.go")
	if err := os.WriteFile(path, []byte("package main\n// disk\n"), 0644); err != nil {
		t.Fatal(err)
	}
	g := NewGateway(Config{})
	g.storeFileSyncEntry("", "main.go", "package main\n// filesync\n", "", 2)

	text, _ := g.executeReadTool(map[string]any{"path": path, "include_line_numbers": false}, workspace)
	if !strings.Contains(text, "filesync") {
		t.Fatalf("read text = %q, want filesync cache content", text)
	}
	if strings.Contains(text, "disk") {
		t.Fatalf("read text used disk content: %q", text)
	}
}

func TestExecutePatchEditToolUsesFileSyncCache(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "main.go")
	if err := os.WriteFile(path, []byte("package main\n// disk\n"), 0644); err != nil {
		t.Fatal(err)
	}
	g := NewGateway(Config{})
	g.storeFileSyncEntry("", "main.go", "package main\n// filesync\n", "", 2)

	text, _ := g.executePatchEditTool(map[string]any{
		"path":       path,
		"old_string": "// filesync",
		"new_string": "// edited",
	}, workspace)
	if !strings.Contains(text, "replaced 1 occurrence") {
		t.Fatalf("patch text = %q, want success", text)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); !strings.Contains(got, "// edited") || strings.Contains(got, "// disk") {
		t.Fatalf("patched file = %q, want edit applied to filesync content", got)
	}
	cached, ok := g.lookupFileSyncContent("", "main.go")
	if !ok || !strings.Contains(cached, "// edited") {
		t.Fatalf("filesync cache = %q ok=%v, want edited content", cached, ok)
	}
}

func TestExecuteWriteToolRefreshesFileSyncCache(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "main.go")
	g := NewGateway(Config{})
	g.storeFileSyncEntry("", "main.go", "old cache", "", 1)

	text, _ := g.executeWriteTool(map[string]any{
		"path":     path,
		"contents": "new content",
	}, workspace)
	if !strings.Contains(text, "wrote") {
		t.Fatalf("write text = %q, want success", text)
	}
	cached, ok := g.lookupFileSyncContent("", "main.go")
	if !ok || cached != "new content" {
		t.Fatalf("filesync cache = %q ok=%v, want new content", cached, ok)
	}
}

func TestSelectedContextPromptIncludesVisibleFileSyncContent(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "main.go")
	rel := "main.go"
	g := NewGateway(Config{})
	g.storeFileSyncEntry("", rel, "package main\nfunc visible() {}\n", "", 3)

	ctx := &agentv1.SelectedContext{
		InvocationContext: &agentv1.InvocationContext{
			Data: &agentv1.InvocationContext_IdeState_{
				IdeState: &agentv1.InvocationContext_IdeState{
					VisibleFiles: []*agentv1.InvocationContext_IdeState_File{
						{Path: path, RelativePath: &rel, TotalLines: 2},
					},
				},
			},
		},
	}
	prompt := g.selectedContextPrompt(ctx, workspace)
	if !strings.Contains(prompt, "func visible") {
		t.Fatalf("selected context prompt = %q, want cached visible file contents", prompt)
	}
}

func TestMissingAgentUsageTokenDelta(t *testing.T) {
	if got := missingAgentUsageTokenDelta(120, 0); got != 120 {
		t.Fatalf("missingAgentUsageTokenDelta(120, 0) = %d, want 120", got)
	}
	if got := missingAgentUsageTokenDelta(120, 45); got != 75 {
		t.Fatalf("missingAgentUsageTokenDelta(120, 45) = %d, want 75", got)
	}
	if got := missingAgentUsageTokenDelta(120, 120); got != 0 {
		t.Fatalf("missingAgentUsageTokenDelta(120, 120) = %d, want 0", got)
	}
	if got := missingAgentUsageTokenDelta(120, 130); got != 0 {
		t.Fatalf("missingAgentUsageTokenDelta(120, 130) = %d, want 0", got)
	}
}

func TestProviderUsageParsesCacheTokens(t *testing.T) {
	_, _, usage := openAIResponsesStreamText("response.completed", `{"type":"response.completed","response":{"usage":{"input_tokens":100,"output_tokens":20,"input_tokens_details":{"cached_tokens":45}}}}`)
	if usage.PromptTokens != 100 || usage.CompletionTokens != 20 || usage.CacheReadTokens != 45 {
		t.Fatalf("openai usage = %#v", usage)
	}

	_, _, usage = anthropicStreamText("message_delta", `{"type":"message_delta","usage":{"input_tokens":120,"output_tokens":30,"cache_read_input_tokens":70,"cache_creation_input_tokens":11}}`)
	if usage.PromptTokens != 120 || usage.CompletionTokens != 30 || usage.CacheReadTokens != 70 || usage.CacheWriteTokens != 11 {
		t.Fatalf("anthropic usage = %#v", usage)
	}
}

func TestAgentConversationCheckpointFrameIncludesTokenDetails(t *testing.T) {
	raw, err := buildAgentConversationCheckpointFrame(1234, 200000, `D:\win\cursor`)
	if err != nil {
		t.Fatalf("buildAgentConversationCheckpointFrame() error = %v", err)
	}
	if len(raw) < 5 {
		t.Fatalf("frame length = %d, want framed proto", len(raw))
	}
	if raw[0] != 0 {
		t.Fatalf("frame flag = %d, want 0", raw[0])
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
	if got := checkpoint.GetTokenDetails().GetUsedTokens(); got != 1234 {
		t.Fatalf("used tokens = %d, want 1234", got)
	}
	if got := checkpoint.GetTokenDetails().GetMaxTokens(); got != 200000 {
		t.Fatalf("max tokens = %d, want 200000", got)
	}
	if got := checkpoint.GetMode(); got != agentv1.AgentMode_AGENT_MODE_AGENT {
		t.Fatalf("mode = %s, want AGENT_MODE_AGENT", got)
	}
	if len(checkpoint.GetPreviousWorkspaceUris()) == 0 {
		t.Fatal("expected workspace URI")
	}
}

func TestReferenceAgentConversationCheckpointPayloadIncludesKVRefs(t *testing.T) {
	g := NewGateway(Config{})
	req := unifiedChatRequest{
		RequestID:       "req-test",
		WorkspaceRoot:   `D:\win\cursor`,
		CurrentUserText: "hello",
		Messages: []chatMessage{
			{Role: "system", Content: "system prompt"},
			{Role: "user", Content: "hello"},
		},
	}

	payload, err := g.buildReferenceAgentConversationCheckpointPayload(req, "answer", []string{`D:\win\cursor\main.go`}, 1234, 200000)
	if err != nil {
		t.Fatalf("buildReferenceAgentConversationCheckpointPayload() error = %v", err)
	}
	if payload.State.GetTokenDetails().GetUsedTokens() != 1234 {
		t.Fatalf("used tokens = %d, want 1234", payload.State.GetTokenDetails().GetUsedTokens())
	}
	if len(payload.State.GetRootPromptMessagesJson()) == 0 {
		t.Fatal("expected root prompt blob ref")
	}
	if len(payload.State.GetTurns()) == 0 {
		t.Fatal("expected turn blob ref")
	}
	if len(payload.State.GetReadPaths()) == 0 {
		t.Fatal("expected read paths")
	}
	if len(payload.Blobs) < 4 {
		t.Fatalf("blob count = %d, want at least 4", len(payload.Blobs))
	}

	turnID := payload.State.GetTurns()[len(payload.State.GetTurns())-1]
	var turnBlob []byte
	for _, blob := range payload.Blobs {
		if bytes.Equal(blob.ID, turnID) {
			turnBlob = blob.Data
			break
		}
	}
	if len(turnBlob) == 0 {
		t.Fatal("turn blob data not found")
	}
	var turn agentv1.ConversationTurnStructure
	if err := proto.Unmarshal(turnBlob, &turn); err != nil {
		t.Fatalf("turn blob decode failed: %v", err)
	}
	agentTurn := turn.GetAgentConversationTurn()
	if agentTurn == nil {
		t.Fatalf("expected agent conversation turn, got %T", turn.GetTurn())
	}
	if len(agentTurn.GetUserMessage()) == 0 || len(agentTurn.GetSteps()) == 0 {
		t.Fatalf("expected user and assistant step refs, user=%d steps=%d", len(agentTurn.GetUserMessage()), len(agentTurn.GetSteps()))
	}
}

func TestAgentConversationCheckpointPayloadRestoresMessagesFromLocalKV(t *testing.T) {
	g := NewGateway(Config{})
	req := unifiedChatRequest{
		RequestID:       "req-restore",
		WorkspaceRoot:   `D:\win\cursor`,
		CurrentUserText: "hello",
		Messages: []chatMessage{
			{Role: "system", Content: "system prompt"},
			{Role: "user", Content: "hello"},
		},
	}
	payload, err := g.buildReferenceAgentConversationCheckpointPayload(req, "answer", nil, 100, 200000)
	if err != nil {
		t.Fatalf("buildReferenceAgentConversationCheckpointPayload() error = %v", err)
	}
	for _, blob := range payload.Blobs {
		g.storeAgentKVBlob(blob)
	}

	messages, stats := g.restoreAgentConversationMessages(context.Background(), nil, payload.State, req.WorkspaceRoot)
	if stats.MissingBlobs != 0 {
		t.Fatalf("missing blobs = %d, want 0", stats.MissingBlobs)
	}
	if len(messages) != 2 {
		t.Fatalf("restored messages = %d, want 2: %#v", len(messages), messages)
	}
	if messages[0].Role != "user" || messages[0].Content != "hello" {
		t.Fatalf("restored user = %#v", messages[0])
	}
	if messages[1].Role != "assistant" || messages[1].Content != "answer" {
		t.Fatalf("restored assistant = %#v", messages[1])
	}
}

func TestAgentKVBlobPersistsAcrossGatewayRestart(t *testing.T) {
	stateDir := t.TempDir()
	blob := agentKVBlob{
		Label: "user_message",
		ID:    []byte("test-blob-id"),
		Data:  []byte("persisted message"),
	}
	g1 := NewGateway(Config{StateDir: stateDir})
	g1.storeAgentKVBlob(blob)

	g2 := NewGateway(Config{StateDir: stateDir})
	data, ok := g2.agentKVBlobData(blob.ID)
	if !ok {
		t.Fatal("expected persisted blob to be restored")
	}
	if string(data) != string(blob.Data) {
		t.Fatalf("persisted blob = %q, want %q", string(data), string(blob.Data))
	}
}

func TestAgentConversationStatePersistsAcrossGatewayRestart(t *testing.T) {
	stateDir := t.TempDir()
	state := &agentv1.ConversationStateStructure{
		TokenDetails: &agentv1.ConversationTokenDetails{UsedTokens: 321, MaxTokens: 200000},
	}
	g1 := NewGateway(Config{StateDir: stateDir})
	g1.storeAgentConversationState("req-persist", state)

	g2 := NewGateway(Config{StateDir: stateDir})
	restored := g2.agentConversationState("req-persist")
	if restored == nil {
		t.Fatal("expected persisted conversation state")
	}
	if got := restored.GetTokenDetails().GetUsedTokens(); got != 321 {
		t.Fatalf("used tokens = %d, want 321", got)
	}
}

func TestMergeAgentRestoredMessagesKeepsOnlyLatestCurrentUser(t *testing.T) {
	restored := []chatMessage{
		{Role: "user", Content: "你好"},
		{Role: "assistant", Content: "你好，我在。"},
	}
	current := []chatMessage{
		{Role: "user", Content: "你好"},
		{Role: "user", Content: "我刚才问你什么"},
	}

	merged := mergeAgentRestoredMessages(restored, current)
	if len(merged) != 3 {
		t.Fatalf("merged messages = %d, want 3: %#v", len(merged), merged)
	}
	if got := merged[2].Content; got != "我刚才问你什么" {
		t.Fatalf("latest current message = %q", got)
	}
}

func TestAgentContextUsedTokensWithPriorDoesNotReset(t *testing.T) {
	got := agentContextUsedTokensWithPrior(1000, 100, 50, []chatMessage{{Role: "user", Content: "hello"}})
	if got <= 1000 {
		t.Fatalf("used tokens = %d, want above prior", got)
	}
	if got := agentContextUsedTokensWithPrior(1000, 2000, 50, nil); got != 2050 {
		t.Fatalf("used tokens = %d, want current total 2050", got)
	}
}

func TestAgentExecCanMatchCursorResultByNumericID(t *testing.T) {
	g := NewGateway(Config{})
	ch, keys := g.registerAgentExec(7, "byok_exec_call_1")
	defer g.unregisterAgentExec(keys)

	g.completeAgentExec(&agentv1.ExecClientMessage{Id: 7, Message: &agentv1.ExecClientMessage_ReadResult{ReadResult: &agentv1.ReadResult{}}})

	select {
	case msg := <-ch:
		if msg.GetId() != 7 {
			t.Fatalf("matched id = %d, want 7", msg.GetId())
		}
	case <-time.After(time.Second):
		t.Fatal("expected exec result to match by numeric id")
	}
}

func TestCursorExecServerMessageBuildsLsArgs(t *testing.T) {
	workspace := t.TempDir()
	g := NewGateway(Config{})

	_, _, msg, err := g.cursorExecServerMessage(agentToolCall{ID: "call_ls", Name: "Ls"}, map[string]any{"path": workspace}, workspace)
	if err != nil {
		t.Fatalf("cursorExecServerMessage() error = %v", err)
	}
	ls := msg.GetLsArgs()
	if ls == nil {
		t.Fatalf("expected Ls args, got %T", msg.GetMessage())
	}
	if ls.GetPath() != workspace {
		t.Fatalf("ls path = %q, want %q", ls.GetPath(), workspace)
	}
	if ls.GetToolCallId() != "call_ls" {
		t.Fatalf("tool call id = %q, want call_ls", ls.GetToolCallId())
	}
	if len(ls.GetIgnore()) == 0 {
		t.Fatal("expected default ignore patterns")
	}
}
