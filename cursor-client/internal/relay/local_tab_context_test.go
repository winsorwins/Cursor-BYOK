package relay

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	aiserverv1 "github.com/burpheart/cursor-tap/cursor_proto/gen/aiserver/v1"
	"google.golang.org/protobuf/proto"
)

func TestBuildCppConfigPayloadEnablesTabContext(t *testing.T) {
	payload := buildCppConfigPayload()
	resp := &aiserverv1.CppConfigResponse{}
	if err := proto.Unmarshal(payload, resp); err != nil {
		t.Fatal(err)
	}
	if !resp.GetIsOn() || !resp.GetIsGhostText() {
		t.Fatalf("cpp config disabled: is_on=%v ghost=%v", resp.GetIsOn(), resp.GetIsGhostText())
	}
	if !resp.GetAllowsTabChunks() {
		t.Fatal("allows_tab_chunks = false, want true")
	}
	if resp.GetCppUrl() != localCppBackendURL {
		t.Fatalf("cpp_url = %q, want %q", resp.GetCppUrl(), localCppBackendURL)
	}
	if resp.GetTabContextRefreshDebounceMs() <= 0 || resp.GetTabContextRefreshEditorChangeDebounceMs() <= 0 {
		t.Fatalf("tab refresh debounce not configured: %d/%d", resp.GetTabContextRefreshDebounceMs(), resp.GetTabContextRefreshEditorChangeDebounceMs())
	}
}

func TestFileSyncUploadFeedsRefreshTabContext(t *testing.T) {
	g := NewGateway(Config{})
	upload := &aiserverv1.FSUploadFileRequest{
		Uuid:                  "workspace-1",
		RelativeWorkspacePath: "src/main.go",
		Contents:              "package main\nfunc main() {}\n",
		ModelVersion:          7,
	}
	uploadBody, err := proto.Marshal(upload)
	if err != nil {
		t.Fatal(err)
	}
	uploadReq := httptest.NewRequest(http.MethodPost, "https://tab.leokun.cn/aiserver.v1.FileSyncService/FSUploadFile", bytes.NewReader(uploadBody))
	uploadReq.Header.Set("Content-Type", "application/proto")
	if payload := g.buildFileSyncUploadPayload(uploadReq); payload == nil {
		t.Fatal("FSUploadFile payload is nil")
	}

	refresh := &aiserverv1.RefreshTabContextRequest{
		CurrentFile: &aiserverv1.CurrentFileInfo{
			RelativeWorkspacePath: "src/main.go",
			RelyOnFilesync:        true,
		},
	}
	refreshBody, err := proto.Marshal(refresh)
	if err != nil {
		t.Fatal(err)
	}
	refreshReq := httptest.NewRequest(http.MethodPost, "https://tab.leokun.cn/aiserver.v1.AiService/RefreshTabContext", bytes.NewReader(refreshBody))
	refreshReq.Header.Set("Content-Type", "application/proto")

	payload := g.buildRefreshTabContextPayload(refreshReq)
	resp := &aiserverv1.RefreshTabContextResponse{}
	if err := proto.Unmarshal(payload, resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.GetCodeResults()) != 1 {
		t.Fatalf("code results = %d, want 1", len(resp.GetCodeResults()))
	}
	block := resp.GetCodeResults()[0].GetCodeBlock()
	if block.GetRelativeWorkspacePath() != "src/main.go" {
		t.Fatalf("path = %q, want src/main.go", block.GetRelativeWorkspacePath())
	}
	if block.GetFileContents() != upload.GetContents() {
		t.Fatalf("file contents = %q, want uploaded contents", block.GetFileContents())
	}
}

func TestFileSyncCachePersistsAcrossGatewayRestart(t *testing.T) {
	stateDir := t.TempDir()
	g1 := NewGateway(Config{StateDir: stateDir})
	g1.storeFileSyncEntry("workspace-1", "src/main.go", "package main\n", "hash", 9)

	g2 := NewGateway(Config{StateDir: stateDir})
	contents, ok := g2.lookupFileSyncContent("", "src/main.go")
	if !ok {
		t.Fatal("expected persisted filesync content")
	}
	if contents != "package main\n" {
		t.Fatalf("contents = %q, want persisted content", contents)
	}
}

func TestFileSyncEnabledPayloadReturnsEnabled(t *testing.T) {
	resp := &aiserverv1.FSIsEnabledForUserResponse{}
	if err := proto.Unmarshal(buildFileSyncEnabledPayload(), resp); err != nil {
		t.Fatal(err)
	}
	if !resp.GetEnabled() {
		t.Fatal("filesync enabled = false, want true")
	}
}

func TestPromptDryRunUsesFileSyncCurrentFile(t *testing.T) {
	g := NewGateway(Config{})
	g.storeFileSyncEntry("workspace-1", "src/main.go", "package main\nfunc main() {}\n", "", 4)

	chatReq := &aiserverv1.StreamUnifiedChatRequest{
		Conversation: []*aiserverv1.ConversationMessage{
			{Text: "explain current file", Type: aiserverv1.ConversationMessage_MESSAGE_TYPE_HUMAN},
		},
		CurrentFile: &aiserverv1.CurrentFileInfo{
			RelativeWorkspacePath: "src/main.go",
			RelyOnFilesync:        true,
		},
	}
	body, err := proto.Marshal(chatReq)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh/aiserver.v1.ChatService/GetPromptDryRun", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/proto")

	payload := g.buildPromptDryRunPayload(req)
	resp := &aiserverv1.GetPromptDryRunResponse{}
	if err := proto.Unmarshal(payload, resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.GetCodeChunks()) == 0 || resp.GetCodeChunks()[0].GetRelativeWorkspacePath() != "src/main.go" {
		t.Fatalf("missing filesync code chunk: %#v", resp.GetCodeChunks())
	}
}
