package relay

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	aiserverv1 "github.com/burpheart/cursor-tap/cursor_proto/gen/aiserver/v1"
	"google.golang.org/protobuf/proto"
)

func TestBuildCountTokensPayloadEstimatesContextItems(t *testing.T) {
	g := NewGateway(Config{})
	countReq := &aiserverv1.CountTokensRequest{
		ContextItems: []*aiserverv1.ContextItem{
			{
				Item: &aiserverv1.ContextItem_FileChunk_{
					FileChunk: &aiserverv1.ContextItem_FileChunk{
						RelativeWorkspacePath: "main.go",
						ChunkContents:         "package main\nfunc main() {}",
						StartLineNumber:       1,
					},
				},
			},
		},
	}
	body, err := proto.Marshal(countReq)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh/aiserver.v1.AiService/CountTokens", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/proto")

	payload := g.buildCountTokensPayload(req)
	resp := &aiserverv1.CountTokensResponse{}
	if err := proto.Unmarshal(payload, resp); err != nil {
		t.Fatal(err)
	}
	if resp.GetCount() <= 0 {
		t.Fatalf("count = %d, want positive", resp.GetCount())
	}
	if len(resp.GetTokenDetails()) != 1 {
		t.Fatalf("token details = %d, want 1", len(resp.GetTokenDetails()))
	}
	if detail := resp.GetTokenDetails()[0]; detail.GetRelativeWorkspacePath() != "main.go" || detail.GetLineCount() != 2 {
		t.Fatalf("unexpected token detail: %#v", detail)
	}
}

func TestBuildPromptDryRunPayloadReturnsContextTokenCounts(t *testing.T) {
	adapter := &ModelAdapter{
		Type:          "openai",
		ModelID:       "gpt-context",
		ContextWindow: 123456,
	}
	g := NewGateway(Config{ModelAdapters: []*ModelAdapter{adapter}})
	modelName := adapter.CursorModelName()
	intent := aiserverv1.ConversationMessage_CodeChunk_INTENT_CODE_SELECTION
	chatReq := &aiserverv1.StreamUnifiedChatRequest{
		ModelDetails: &aiserverv1.ModelDetails{ModelName: &modelName},
		Conversation: []*aiserverv1.ConversationMessage{
			{
				Text: "optimize this file",
				Type: aiserverv1.ConversationMessage_MESSAGE_TYPE_HUMAN,
				AttachedCodeChunks: []*aiserverv1.ConversationMessage_CodeChunk{
					{
						RelativeWorkspacePath: "main.go",
						StartLineNumber:       1,
						Lines:                 []string{"package main", "func main() {}"},
						Intent:                &intent,
					},
				},
			},
		},
		CurrentFile: &aiserverv1.CurrentFileInfo{
			RelativeWorkspacePath: "main.go",
			Contents:              "package main\nfunc main() {}",
			ContentsStartAtLine:   1,
			TotalNumberOfLines:    2,
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
	if resp.GetUserMessageTokenLimit() != 123456 {
		t.Fatalf("token limit = %d, want 123456", resp.GetUserMessageTokenLimit())
	}
	if resp.GetUserMessageTokenCount().GetNumTokens() <= 0 {
		t.Fatalf("user token count = %d, want positive", resp.GetUserMessageTokenCount().GetNumTokens())
	}
	if resp.GetFullConversationTokenCount().GetNumTokens() <= localContextSystemTokens+localContextToolTokens {
		t.Fatalf("full token count = %d, want more than fixed overhead", resp.GetFullConversationTokenCount().GetNumTokens())
	}
	if resp.GetBarFraction() <= 0 {
		t.Fatalf("bar fraction = %f, want positive", resp.GetBarFraction())
	}
	if len(resp.GetCodeChunks()) == 0 || resp.GetCodeChunks()[0].GetRelativeWorkspacePath() != "main.go" {
		t.Fatalf("unexpected code chunks: %#v", resp.GetCodeChunks())
	}
}
