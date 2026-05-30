package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"cursor-client/internal/certs"
	appruntime "cursor-client/internal/runtime"

	agentv1 "github.com/burpheart/cursor-tap/cursor_proto/gen/agent/v1"
	aiserverv1 "github.com/burpheart/cursor-tap/cursor_proto/gen/aiserver/v1"
	"github.com/elazarl/goproxy"
	"google.golang.org/protobuf/proto"
)

const (
	captureLimit       = 32 * 1024 * 1024
	streamProgressStep = 64
)

var sensitiveRE = regexp.MustCompile(`(?i)(bearer\s+)[a-z0-9._\-]{8,}|(sk-[a-z0-9_\-]{8,})|((api[_\-]?key|authorization|x-api-key)["'\s:=]+)[^"'\s,}]+`)

type tapLogger struct {
	mu sync.Mutex
	f  *os.File
}

type tapEvent struct {
	Time        string         `json:"time"`
	Session     int64          `json:"session,omitempty"`
	Source      string         `json:"source,omitempty"`
	Direction   string         `json:"direction"`
	Method      string         `json:"method,omitempty"`
	Host        string         `json:"host,omitempty"`
	Path        string         `json:"path,omitempty"`
	StatusCode  int            `json:"statusCode,omitempty"`
	ContentType string         `json:"contentType,omitempty"`
	BodyBytes   int            `json:"bodyBytes,omitempty"`
	Details     map[string]any `json:"details,omitempty"`
	Error       string         `json:"error,omitempty"`
}

type settingsBackup struct {
	path    string
	exists  bool
	content []byte
}

func main() {
	listenAddr := flag.String("listen", "127.0.0.1:18081", "diagnostic proxy listen address")
	upstreamProxy := flag.String("upstream-proxy", "http://127.0.0.1:18080", "upstream proxy provided by the reference BYOK app")
	applyCursorProxy := flag.Bool("apply-cursor-proxy", true, "temporarily point Cursor settings at this diagnostic proxy")
	captureAll := flag.Bool("capture-all", true, "capture all HTTP(S) traffic metadata instead of only known Cursor/provider hosts")
	referenceExe := flag.String("reference-exe", "", "optional reference BYOK executable to launch with HTTP_PROXY/HTTPS_PROXY pointed at this tap")
	referenceListen := flag.String("reference-listen", "127.0.0.1:18082", "direct diagnostic proxy for traffic emitted by the launched reference app")
	logPathFlag := flag.String("log", "", "JSONL log path")
	flag.Parse()

	paths, err := appruntime.ResolvePaths()
	if err != nil {
		log.Fatalf("resolve paths: %v", err)
	}
	logPath := *logPathFlag
	if strings.TrimSpace(logPath) == "" {
		logPath = filepath.Join(paths.LogDir, "byok-tap.jsonl")
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0700); err != nil {
		log.Fatalf("create log dir: %v", err)
	}
	logger, err := newTapLogger(logPath)
	if err != nil {
		log.Fatalf("open log: %v", err)
	}
	defer logger.Close()

	ca, err := certs.LoadOrCreateCA(paths.CACertPath, paths.CAKeyPath)
	if err != nil {
		log.Fatalf("load CA: %v", err)
	}
	trust := ca.EnsureTrusted()
	logger.Log(tapEvent{Direction: "tap_start", Details: map[string]any{
		"listen":           *listenAddr,
		"upstream_proxy":   *upstreamProxy,
		"log_path":         logPath,
		"capture_all":      *captureAll,
		"reference_exe":    sanitize(*referenceExe),
		"reference_listen": *referenceListen,
		"ca_trusted":       trust.Trusted,
		"ca_installed":     trust.Installed,
		"ca_store":         trust.Store,
		"ca_thumbprint":    trust.Thumbprint,
		"ca_trust_error":   trust.Error,
		"cursor_proxy_set": *applyCursorProxy,
	}})

	proxyURL, err := url.Parse(*upstreamProxy)
	if err != nil {
		log.Fatalf("invalid upstream proxy: %v", err)
	}

	proxy := goproxy.NewProxyHttpServer()
	proxy.Verbose = false
	proxy.Tr = &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		Proxy:             conditionalUpstreamProxy(proxyURL),
		ForceAttemptHTTP2: false,
	}
	proxy.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		ensureAbsoluteURL(req)
		if shouldCaptureRequest(req, *captureAll) {
			body := readAndRestoreRequestBody(req)
			logger.Log(withSource(withSession(summarizeRequest(req, body), ctx), "cursor_to_reference"))
		}
		return req, nil
	})
	proxy.OnResponse().DoFunc(func(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
		if resp == nil || ctx == nil || ctx.Req == nil {
			return resp
		}
		req := ctx.Req
		if !shouldCaptureRequest(req, *captureAll) {
			return resp
		}
		resp.Body = &bodyTapper{
			ReadCloser: resp.Body,
			logger:     logger,
			req:        req,
			resp:       resp,
			session:    ctx.Session,
			limit:      captureLimit,
			onClose: func(body []byte, truncated bool) {
				event := summarizeResponse(req, resp, body)
				event.Session = ctx.Session
				event.Source = "cursor_to_reference"
				if truncated {
					if event.Details == nil {
						event.Details = map[string]any{}
					}
					event.Details["capture_truncated"] = true
				}
				logger.Log(event)
			},
		}
		return resp
	})
	proxy.OnRequest().HandleConnectFunc(func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
		if *captureAll || isCursorHost(host) || isProviderHost(host) {
			logger.Log(tapEvent{Session: ctx.Session, Source: "cursor_to_reference", Direction: "connect", Method: "CONNECT", Host: sanitize(host)})
		}
		return &goproxy.ConnectAction{
			Action: goproxy.ConnectMitm,
			TLSConfig: func(host string, ctx *goproxy.ProxyCtx) (*tls.Config, error) {
				return tlsConfigForHost(ca, host), nil
			},
		}, host
	})

	listener, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		log.Fatalf("listen %s: %v", *listenAddr, err)
	}
	defer listener.Close()

	var backup *settingsBackup
	if *applyCursorProxy {
		backup, err = applyTemporaryCursorProxy("http://" + *listenAddr)
		if err != nil {
			log.Fatalf("apply Cursor proxy: %v", err)
		}
		defer func() {
			if err := backup.restore(); err != nil {
				log.Printf("restore Cursor settings failed: %v", err)
			}
		}()
	}

	server := &http.Server{Handler: proxy}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("tap server error: %v", err)
		}
	}()

	referenceServer, referenceListener, err := startDirectCaptureProxy(*referenceListen, ca, logger, *captureAll)
	if err != nil {
		log.Fatalf("start reference capture proxy: %v", err)
	}
	defer func() {
		_ = referenceServer.Close()
		_ = referenceListener.Close()
	}()

	var referenceCmd *exec.Cmd
	if strings.TrimSpace(*referenceExe) != "" {
		referenceCmd, err = startReferenceApp(*referenceExe, "http://"+*referenceListen)
		if err != nil {
			log.Fatalf("start reference app: %v", err)
		}
		logger.Log(tapEvent{Direction: "reference_start", Details: map[string]any{"exe": sanitize(*referenceExe), "pid": referenceCmd.Process.Pid, "proxy": "http://" + *referenceListen}})
		defer func() {
			if referenceCmd != nil && referenceCmd.Process != nil {
				_ = referenceCmd.Process.Kill()
				_, _ = referenceCmd.Process.Wait()
				logger.Log(tapEvent{Direction: "reference_stop", Details: map[string]any{"pid": referenceCmd.Process.Pid}})
			}
		}()
	}

	log.Printf("byok-tap listening on %s, forwarding via %s", *listenAddr, *upstreamProxy)
	log.Printf("reference egress capture listening on %s", *referenceListen)
	log.Printf("log: %s", logPath)
	log.Printf("run the reference BYOK app on %s, then use Cursor normally; press Ctrl+C here when done", *upstreamProxy)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	_ = server.Close()
	logger.Log(tapEvent{Direction: "tap_stop"})
}

func newTapLogger(path string) (*tapLogger, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	return &tapLogger{f: f}, nil
}

func (l *tapLogger) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	return l.f.Close()
}

func (l *tapLogger) Log(event tapEvent) {
	if l == nil || l.f == nil {
		return
	}
	if event.Time == "" {
		event.Time = time.Now().Format(time.RFC3339Nano)
	}
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.f.Write(append(data, '\n'))
}

type bodyTapper struct {
	io.ReadCloser
	buf       bytes.Buffer
	logger    *tapLogger
	req       *http.Request
	resp      *http.Response
	session   int64
	limit     int
	truncated bool
	bytesRead int64
	nextPulse int64
	onClose   func([]byte, bool)
	once      sync.Once
}

func (b *bodyTapper) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 && b.buf.Len() < b.limit {
		remain := b.limit - b.buf.Len()
		if n > remain {
			_, _ = b.buf.Write(p[:remain])
			b.truncated = true
		} else {
			_, _ = b.buf.Write(p[:n])
		}
	}
	if n > 0 && isRunSSERequest(b.req) && b.logger != nil {
		read := atomic.AddInt64(&b.bytesRead, int64(n))
		threshold := atomic.LoadInt64(&b.nextPulse)
		if threshold == 0 {
			threshold = 256 * 1024
			atomic.CompareAndSwapInt64(&b.nextPulse, 0, threshold)
		}
		if read >= threshold {
			atomic.StoreInt64(&b.nextPulse, threshold+256*1024)
			b.logger.Log(tapEvent{
				Session:     b.session,
				Direction:   "response_progress",
				Method:      requestMethod(b.req),
				Host:        requestHost(b.req),
				Path:        requestPath(b.req),
				StatusCode:  responseStatus(b.resp),
				ContentType: responseContentType(b.resp),
				BodyBytes:   int(read),
				Details:     map[string]any{"rpc": "RunSSE", "captured_bytes": b.buf.Len()},
			})
		}
	}
	return n, err
}

func (b *bodyTapper) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() {
		if b.onClose != nil {
			b.onClose(b.buf.Bytes(), b.truncated)
		}
	})
	return err
}

func summarizeRequest(req *http.Request, body []byte) tapEvent {
	event := tapEvent{
		Direction:   "request",
		Method:      req.Method,
		Host:        requestHost(req),
		Path:        requestPath(req),
		ContentType: req.Header.Get("Content-Type"),
		BodyBytes:   len(body),
		Details: map[string]any{
			"headers": sanitizedHeaderSummary(req.Header),
		},
	}
	decoded, err := decodeBodyContent(body, req.Header)
	if err != nil {
		event.Error = err.Error()
		return event
	}
	if provider := summarizeProviderRequest(req, decoded); provider != nil {
		event.Details["provider_request"] = provider
	}
	path := requestPathOnly(req)
	switch {
	case strings.Contains(path, "/aiserver.v1.BidiService/BidiAppend"):
		event.Details["rpc"] = "BidiAppend"
		event.Details["bidi_append"] = summarizeBidiAppendRequest(decoded, req.Header.Get("Content-Type"))
	case strings.Contains(path, "/agent.v1.AgentService/RunSSE"):
		event.Details["rpc"] = "RunSSE"
		event.Details["run_sse"] = summarizeRunSSERequest(decoded, req.Header.Get("Content-Type"))
	case strings.Contains(path, "/aiserver.v1.AiService/StreamCpp"):
		event.Details["rpc"] = "StreamCpp"
		event.Details["stream_cpp"] = summarizeStreamCppRequest(decoded, req.Header.Get("Content-Type"))
	case strings.Contains(path, "/AvailableModels"):
		event.Details["rpc"] = "AvailableModels"
	case strings.Contains(path, "/GetDefaultModel"):
		event.Details["rpc"] = "GetDefaultModel"
	case len(decoded) > 0 && shouldLogBodySample(req.Header.Get("Content-Type"), decoded):
		event.Details["body_sample"] = summarizeRawBody(decoded)
	}
	return event
}

func summarizeResponse(req *http.Request, resp *http.Response, body []byte) tapEvent {
	header := http.Header{}
	if resp != nil {
		header = resp.Header
	}
	event := tapEvent{
		Direction:   "response",
		Method:      req.Method,
		Host:        requestHost(req),
		Path:        requestPath(req),
		StatusCode:  resp.StatusCode,
		ContentType: header.Get("Content-Type"),
		BodyBytes:   len(body),
		Details:     map[string]any{},
	}
	decoded, err := decodeBodyContent(body, header)
	if err != nil {
		event.Error = err.Error()
		return event
	}
	if provider := summarizeProviderResponse(req, resp, decoded); provider != nil {
		event.Details["provider_response"] = provider
	}
	path := requestPathOnly(req)
	switch {
	case strings.Contains(path, "/agent.v1.AgentService/RunSSE"):
		event.Details["rpc"] = "RunSSE"
		event.Details["agent_server_frames"] = summarizeAgentServerStream(decoded, header.Get("Content-Type"))
	case strings.Contains(path, "/aiserver.v1.AiService/StreamCpp"):
		event.Details["rpc"] = "StreamCpp"
		event.Details["stream_cpp_frames"] = summarizeStreamCppResponseStream(decoded, header.Get("Content-Type"))
	case strings.Contains(path, "/aiserver.v1.AiService/AvailableModels"):
		event.Details["rpc"] = "AvailableModels"
		event.Details["available_models"] = summarizeAvailableModelsResponse(decoded, header.Get("Content-Type"))
	case strings.Contains(path, "/aiserver.v1.AiService/GetDefaultModel"):
		event.Details["rpc"] = "GetDefaultModel"
		event.Details["default_model"] = summarizeGetDefaultModelResponse(decoded, header.Get("Content-Type"))
	case strings.Contains(path, "/aiserver.v1.BidiService/BidiAppend"):
		event.Details["rpc"] = "BidiAppend"
	case len(decoded) > 0 && shouldLogBodySample(header.Get("Content-Type"), decoded):
		event.Details["body_sample"] = summarizeRawBody(decoded)
	}
	return event
}

func summarizeBidiAppendRequest(body []byte, contentType string) map[string]any {
	payload, err := firstPayload(body, contentType)
	if err != nil {
		return map[string]any{"decode_error": err.Error()}
	}
	var req aiserverv1.BidiAppendRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return map[string]any{"decode_error": err.Error(), "payload_len": len(payload)}
	}
	out := map[string]any{"data_hex_len": len(req.GetData())}
	if req.GetRequestId() != nil {
		out["request_id"] = req.GetRequestId().GetRequestId()
	}
	if data := req.GetData(); data != "" {
		raw, err := hex.DecodeString(data)
		if err != nil {
			out["data_decode_error"] = err.Error()
		} else {
			out["agent_client_message"] = summarizeAgentClientMessage(raw)
		}
	}
	return out
}

func summarizeRunSSERequest(body []byte, contentType string) map[string]any {
	payload, err := firstPayload(body, contentType)
	if err != nil {
		return map[string]any{"decode_error": err.Error()}
	}
	var req agentv1.BidiRequestId
	if err := proto.Unmarshal(payload, &req); err != nil {
		return map[string]any{"decode_error": err.Error(), "payload_len": len(payload)}
	}
	return map[string]any{"request_id": req.GetRequestId()}
}

func summarizeStreamCppRequest(body []byte, contentType string) map[string]any {
	payload, err := firstPayload(body, contentType)
	if err != nil {
		return map[string]any{"decode_error": err.Error()}
	}
	var req aiserverv1.StreamCppRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return map[string]any{"decode_error": err.Error(), "payload_len": len(payload)}
	}
	out := map[string]any{
		"payload_len":              len(payload),
		"model_name":               sanitize(req.GetModelName()),
		"workspace_id":             sanitize(req.GetWorkspaceId()),
		"context_items":            len(req.GetContextItems()),
		"diff_history":             len(req.GetDiffHistory()),
		"diff_history_keys":        len(req.GetDiffHistoryKeys()),
		"file_diff_histories":      len(req.GetFileDiffHistories()),
		"merged_diff_histories":    len(req.GetMergedDiffHistories()),
		"block_diff_patches":       len(req.GetBlockDiffPatches()),
		"parameter_hints":          len(req.GetParameterHints()),
		"lsp_contexts":             len(req.GetLspContexts()),
		"additional_files":         len(req.GetAdditionalFiles()),
		"filesync_updates":         len(req.GetFilesyncUpdates()),
		"code_results":             len(req.GetCodeResults()),
		"supports_cpt":             req.GetSupportsCpt(),
		"supports_crlf_cpt":        req.GetSupportsCrlfCpt(),
		"control_token":            req.GetControlToken().String(),
		"time_since_request_start": req.GetTimeSinceRequestStart(),
	}
	if file := req.GetCurrentFile(); file != nil {
		out["current_file"] = summarizeCurrentFile(file)
	}
	return out
}

func summarizeCurrentFile(file *aiserverv1.CurrentFileInfo) map[string]any {
	if file == nil {
		return nil
	}
	out := map[string]any{
		"relative_workspace_path": sanitize(file.GetRelativeWorkspacePath()),
		"workspace_root_path":     sanitize(file.GetWorkspaceRootPath()),
		"contents_len":            len(file.GetContents()),
		"contents_head":           truncate(sanitize(file.GetContents()), 500),
		"language_id":             file.GetLanguageId(),
		"total_lines":             file.GetTotalNumberOfLines(),
		"contents_start_at_line":  file.GetContentsStartAtLine(),
		"rely_on_filesync":        file.GetRelyOnFilesync(),
		"file_version":            file.GetFileVersion(),
		"cells":                   len(file.GetCells()),
		"top_chunks":              len(file.GetTopChunks()),
		"diagnostics":             len(file.GetDiagnostics()),
	}
	if pos := file.GetCursorPosition(); pos != nil {
		out["cursor_position"] = map[string]any{
			"line":   pos.GetLine(),
			"column": pos.GetColumn(),
		}
	}
	return out
}

func summarizeStreamCppResponseStream(body []byte, contentType string) map[string]any {
	frames, err := payloadFrames(body, contentType)
	if err != nil {
		return map[string]any{"decode_error": err.Error(), "body_sample": summarizeRawBody(body)}
	}
	counts := map[string]int{}
	items := []map[string]any{}
	textChars := 0
	for _, frame := range frames {
		if frame.Flag&0x80 != 0 || frame.Flag&0x02 != 0 {
			counts["trailer"]++
			continue
		}
		var msg aiserverv1.StreamCppResponse
		if err := proto.Unmarshal(frame.Payload, &msg); err != nil {
			counts["decode_error"]++
			items = appendLimited(items, map[string]any{
				"decode_error": err.Error(),
				"payload_len":  len(frame.Payload),
				"hex_prefix":   hex.EncodeToString(frame.Payload[:minInt(len(frame.Payload), 32)]),
			}, 60)
			continue
		}
		item := summarizeStreamCppResponse(&msg)
		textChars += len(msg.GetText())
		kind := "message"
		if msg.GetText() != "" {
			kind = "text"
		}
		if msg.GetModelInfo() != nil {
			kind = "model_info"
		}
		if msg.GetRangeToReplace() != nil {
			kind = "range_to_replace"
		}
		if msg.GetDoneStream() || msg.GetDoneEdit() {
			kind = "done"
		}
		counts[kind]++
		items = appendLimited(items, item, 120)
	}
	return map[string]any{"frame_count": len(frames), "counts": counts, "text_chars": textChars, "items": items}
}

func summarizeStreamCppResponse(msg *aiserverv1.StreamCppResponse) map[string]any {
	if msg == nil {
		return map[string]any{}
	}
	out := map[string]any{
		"text_len":              len(msg.GetText()),
		"text_head":             truncate(sanitize(msg.GetText()), 500),
		"suggestion_start_line": msg.GetSuggestionStartLine(),
		"suggestion_confidence": msg.GetSuggestionConfidence(),
		"done_stream":           msg.GetDoneStream(),
		"done_edit":             msg.GetDoneEdit(),
		"begin_edit":            msg.GetBeginEdit(),
		"binding_id":            sanitize(msg.GetBindingId()),
	}
	if r := msg.GetRangeToReplace(); r != nil {
		out["range_to_replace"] = map[string]any{"start_line": r.GetStartLineNumber(), "end_line": r.GetEndLineNumberInclusive()}
	}
	if info := msg.GetModelInfo(); info != nil {
		out["model_info"] = map[string]any{"fused_cursor_prediction": info.GetIsFusedCursorPredictionModel(), "multidiff": info.GetIsMultidiffModel()}
	}
	if target := msg.GetCursorPredictionTarget(); target != nil {
		out["cursor_prediction_target"] = map[string]any{
			"relative_path":        sanitize(target.GetRelativePath()),
			"line_number":          target.GetLineNumberOneIndexed(),
			"expected_content_len": len(target.GetExpectedContent()),
			"should_retrigger_cpp": target.GetShouldRetriggerCpp(),
		}
	}
	return out
}

func summarizeAgentClientMessage(raw []byte) map[string]any {
	var msg agentv1.AgentClientMessage
	if err := proto.Unmarshal(raw, &msg); err != nil {
		return map[string]any{"decode_error": err.Error(), "payload_len": len(raw)}
	}
	out := map[string]any{"kind": agentClientMessageKind(&msg)}
	if run := msg.GetRunRequest(); run != nil {
		model, effort := requestedModelSummary(run)
		out["model"] = model
		out["thinking_effort"] = effort
		out["workspace"] = workspaceFromRunRequest(run)
		out["user_text_len"] = len(textFromConversationAction(run.GetAction()))
	}
	if action := msg.GetConversationAction(); action != nil {
		out["workspace"] = workspaceFromConversationAction(action)
		out["user_text_len"] = len(textFromConversationAction(action))
	}
	if exec := msg.GetExecClientMessage(); exec != nil {
		out["exec_client"] = summarizeExecClientMessage(exec)
	}
	return out
}

func summarizeAgentServerStream(body []byte, contentType string) map[string]any {
	frames, err := payloadFrames(body, contentType)
	if err != nil {
		out := map[string]any{"decode_error": err.Error(), "body_preview": truncate(sanitize(string(body)), 300)}
		if sse := summarizeSSEBody(body); sse != nil {
			out["sse"] = sse
		}
		return out
	}
	counts := map[string]int{}
	items := []map[string]any{}
	rawSamples := []map[string]any{}
	for _, frame := range frames {
		if frame.Flag&0x80 != 0 || frame.Flag&0x02 != 0 {
			counts["trailer"]++
			continue
		}
		var msg agentv1.AgentServerMessage
		if err := proto.Unmarshal(frame.Payload, &msg); err != nil {
			counts["decode_error"]++
			rawSamples = appendLimited(rawSamples, map[string]any{
				"error":       err.Error(),
				"payload_len": len(frame.Payload),
				"preview":     truncate(sanitize(string(frame.Payload)), 500),
				"hex_prefix":  hex.EncodeToString(frame.Payload[:minInt(len(frame.Payload), 32)]),
			}, 20)
			continue
		}
		item := summarizeAgentServerMessage(&msg)
		item["frame_index"] = len(items) + counts["trailer"] + counts["decode_error"]
		if kind, _ := item["kind"].(string); kind != "" {
			counts[kind]++
		}
		items = appendLimited(items, item, 120)
	}
	out := map[string]any{"frame_count": len(frames), "counts": counts, "items": items}
	if len(rawSamples) > 0 {
		out["raw_samples"] = rawSamples
	}
	return out
}

func summarizeAgentServerMessage(msg *agentv1.AgentServerMessage) map[string]any {
	if msg == nil || msg.GetMessage() == nil {
		return map[string]any{"kind": "empty"}
	}
	if update := msg.GetInteractionUpdate(); update != nil {
		return summarizeInteractionUpdate(update)
	}
	if exec := msg.GetExecServerMessage(); exec != nil {
		item := summarizeExecServerMessage(exec)
		item["kind"] = "exec_server_message"
		return item
	}
	if msg.GetExecServerControlMessage() != nil {
		return map[string]any{"kind": "exec_server_control"}
	}
	if checkpoint := msg.GetConversationCheckpointUpdate(); checkpoint != nil {
		item := map[string]any{"kind": "conversation_checkpoint"}
		if details := checkpoint.GetTokenDetails(); details != nil {
			item["token_details"] = map[string]any{
				"used_tokens": details.GetUsedTokens(),
				"max_tokens":  details.GetMaxTokens(),
			}
		}
		if checkpoint.GetMode() != agentv1.AgentMode_AGENT_MODE_UNSPECIFIED {
			item["mode"] = checkpoint.GetMode().String()
		}
		if len(checkpoint.GetPreviousWorkspaceUris()) > 0 {
			item["previous_workspace_uris"] = checkpoint.GetPreviousWorkspaceUris()
		}
		if len(checkpoint.GetReadPaths()) > 0 {
			item["read_paths"] = checkpoint.GetReadPaths()
		}
		return item
	}
	if msg.GetKvServerMessage() != nil {
		return map[string]any{"kind": "kv_server_message"}
	}
	if msg.GetInteractionQuery() != nil {
		return map[string]any{"kind": "interaction_query"}
	}
	return map[string]any{"kind": fmt.Sprintf("%T", msg.GetMessage())}
}

func summarizeInteractionUpdate(update *agentv1.InteractionUpdate) map[string]any {
	switch {
	case update.GetTextDelta() != nil:
		return map[string]any{"kind": "text_delta", "text_len": len(update.GetTextDelta().GetText())}
	case update.GetPartialToolCall() != nil:
		partial := update.GetPartialToolCall()
		return map[string]any{"kind": "partial_tool_call", "call_id": partial.GetCallId(), "model_call_id": partial.GetModelCallId(), "args_delta": truncate(sanitize(partial.GetArgsTextDelta()), 500), "tool": summarizeToolCall(partial.GetToolCall())}
	case update.GetToolCallStarted() != nil:
		started := update.GetToolCallStarted()
		return map[string]any{"kind": "tool_call_started", "call_id": started.GetCallId(), "model_call_id": started.GetModelCallId(), "tool": summarizeToolCall(started.GetToolCall())}
	case update.GetToolCallCompleted() != nil:
		completed := update.GetToolCallCompleted()
		return map[string]any{"kind": "tool_call_completed", "call_id": completed.GetCallId(), "model_call_id": completed.GetModelCallId(), "tool": summarizeToolCall(completed.GetToolCall())}
	case update.GetToolCallDelta() != nil:
		return map[string]any{"kind": "tool_call_delta"}
	case update.GetThinkingDelta() != nil:
		return map[string]any{"kind": "thinking_delta"}
	case update.GetThinkingCompleted() != nil:
		return map[string]any{"kind": "thinking_completed"}
	case update.GetTokenDelta() != nil:
		return map[string]any{"kind": "token_delta", "tokens": update.GetTokenDelta().GetTokens()}
	case update.GetSummary() != nil:
		return map[string]any{"kind": "summary"}
	case update.GetSummaryStarted() != nil:
		return map[string]any{"kind": "summary_started"}
	case update.GetSummaryCompleted() != nil:
		return map[string]any{"kind": "summary_completed"}
	case update.GetShellOutputDelta() != nil:
		return map[string]any{"kind": "shell_output_delta"}
	case update.GetHeartbeat() != nil:
		return map[string]any{"kind": "heartbeat"}
	case update.GetTurnEnded() != nil:
		return map[string]any{"kind": "turn_ended"}
	case update.GetStepStarted() != nil:
		return map[string]any{"kind": "step_started", "step_id": update.GetStepStarted().GetStepId()}
	case update.GetStepCompleted() != nil:
		return map[string]any{"kind": "step_completed", "step_id": update.GetStepCompleted().GetStepId(), "duration_ms": update.GetStepCompleted().GetStepDurationMs()}
	default:
		return map[string]any{"kind": fmt.Sprintf("%T", update.GetMessage())}
	}
}

func summarizeToolCall(call *agentv1.ToolCall) map[string]any {
	if call == nil {
		return nil
	}
	switch {
	case call.GetReadToolCall() != nil:
		args := call.GetReadToolCall().GetArgs()
		return map[string]any{"name": "Read", "path": sanitize(args.GetPath()), "has_result": call.GetReadToolCall().GetResult() != nil}
	case call.GetGrepToolCall() != nil:
		args := call.GetGrepToolCall().GetArgs()
		return map[string]any{"name": "Grep", "pattern": truncate(sanitize(args.GetPattern()), 300), "path": sanitize(args.GetPath()), "glob": sanitize(args.GetGlob()), "has_result": call.GetGrepToolCall().GetResult() != nil}
	case call.GetGlobToolCall() != nil:
		args := call.GetGlobToolCall().GetArgs()
		return map[string]any{"name": "Glob", "pattern": sanitize(args.GetGlobPattern()), "target_directory": sanitize(args.GetTargetDirectory()), "has_result": call.GetGlobToolCall().GetResult() != nil}
	case call.GetShellToolCall() != nil:
		args := call.GetShellToolCall().GetArgs()
		return map[string]any{"name": "Shell", "command": truncate(sanitize(args.GetCommand()), 500), "working_directory": sanitize(args.GetWorkingDirectory()), "has_result": call.GetShellToolCall().GetResult() != nil}
	case call.GetLsToolCall() != nil:
		args := call.GetLsToolCall().GetArgs()
		return map[string]any{"name": "Ls", "path": sanitize(args.GetPath()), "has_result": call.GetLsToolCall().GetResult() != nil}
	case call.GetEditToolCall() != nil:
		return map[string]any{"name": "Edit"}
	case call.GetMcpToolCall() != nil:
		return map[string]any{"name": "Mcp"}
	case call.GetWebSearchToolCall() != nil:
		return map[string]any{"name": "WebSearch"}
	case call.GetFetchToolCall() != nil:
		return map[string]any{"name": "Fetch"}
	default:
		return map[string]any{"name": fmt.Sprintf("%T", call.GetTool())}
	}
}

func summarizeExecServerMessage(msg *agentv1.ExecServerMessage) map[string]any {
	out := map[string]any{"id": msg.GetId(), "exec_id": msg.GetExecId()}
	switch {
	case msg.GetReadArgs() != nil:
		out["exec_kind"] = "Read"
		out["path"] = sanitize(msg.GetReadArgs().GetPath())
	case msg.GetGrepArgs() != nil:
		args := msg.GetGrepArgs()
		out["exec_kind"] = "Grep"
		out["pattern"] = truncate(sanitize(args.GetPattern()), 300)
		out["path"] = sanitize(args.GetPath())
		out["glob"] = sanitize(args.GetGlob())
	case msg.GetShellArgs() != nil:
		args := msg.GetShellArgs()
		out["exec_kind"] = "Shell"
		out["command"] = truncate(sanitize(args.GetCommand()), 500)
		out["working_directory"] = sanitize(args.GetWorkingDirectory())
	case msg.GetLsArgs() != nil:
		out["exec_kind"] = "Ls"
		out["path"] = sanitize(msg.GetLsArgs().GetPath())
	case msg.GetDiagnosticsArgs() != nil:
		out["exec_kind"] = "Diagnostics"
	case msg.GetRequestContextArgs() != nil:
		out["exec_kind"] = "RequestContext"
	case msg.GetMcpArgs() != nil:
		out["exec_kind"] = "Mcp"
	case msg.GetFetchArgs() != nil:
		out["exec_kind"] = "Fetch"
	default:
		out["exec_kind"] = fmt.Sprintf("%T", msg.GetMessage())
	}
	return out
}

func summarizeExecClientMessage(msg *agentv1.ExecClientMessage) map[string]any {
	out := map[string]any{"id": msg.GetId(), "exec_id": msg.GetExecId(), "kind": execClientKind(msg)}
	if result := summarizeExecClientResult(msg); result != nil {
		out["result"] = result
	}
	return out
}

func summarizeAvailableModelsResponse(body []byte, contentType string) map[string]any {
	payload, err := firstPayload(body, contentType)
	if err != nil {
		return map[string]any{"decode_error": err.Error()}
	}
	var resp aiserverv1.AvailableModelsResponse
	if err := proto.Unmarshal(payload, &resp); err != nil {
		return map[string]any{"decode_error": err.Error(), "payload_len": len(payload)}
	}
	models := []map[string]any{}
	for _, model := range resp.GetModels() {
		models = append(models, map[string]any{
			"name":                  model.GetName(),
			"display":               model.GetClientDisplayName(),
			"server_model":          model.GetServerModelName(),
			"default_on":            model.GetDefaultOn(),
			"supports_agent":        model.GetSupportsAgent(),
			"supports_thinking":     model.GetSupportsThinking(),
			"supports_max_mode":     model.GetSupportsMaxMode(),
			"supports_non_max_mode": model.GetSupportsNonMaxMode(),
			"supports_cmdk":         model.GetSupportsCmdK(),
			"context_limit":         model.GetContextTokenLimit(),
			"variants":              len(model.GetVariants()),
			"parameters":            len(model.GetParameterDefinitions()),
			"parameter_defs":        summarizeModelParameterDefinitions(model.GetParameterDefinitions()),
			"variant_defs":          summarizeModelVariants(model.GetVariants()),
		})
	}
	return map[string]any{
		"model_count":               len(models),
		"models":                    models,
		"model_names":               resp.GetModelNames(),
		"use_model_parameters":      resp.GetUseModelParameters(),
		"composer_default":          featureDefault(resp.GetComposerModelConfig()),
		"cmdk_default":              featureDefault(resp.GetCmdKModelConfig()),
		"plan_execution_default":    featureDefault(resp.GetPlanExecutionModelConfig()),
		"quick_agent_default":       featureDefault(resp.GetQuickAgentModelConfig()),
		"background_comp_default":   featureDefault(resp.GetBackgroundComposerModelConfig()),
		"deep_search_model_default": featureDefault(resp.GetDeepSearchModelConfig()),
	}
}

func summarizeGetDefaultModelResponse(body []byte, contentType string) map[string]any {
	payload, err := firstPayload(body, contentType)
	if err != nil {
		return map[string]any{"decode_error": err.Error()}
	}
	var resp aiserverv1.GetDefaultModelResponse
	if err := proto.Unmarshal(payload, &resp); err != nil {
		return map[string]any{"decode_error": err.Error(), "payload_len": len(payload)}
	}
	return map[string]any{"model": resp.GetModel(), "thinking_model": resp.GetThinkingModel(), "max_mode": resp.GetMaxMode(), "next_default_set_date": resp.GetNextDefaultSetDate()}
}

func summarizeExecClientResult(msg *agentv1.ExecClientMessage) map[string]any {
	if msg == nil {
		return nil
	}
	switch {
	case msg.GetReadResult() != nil:
		return summarizeReadResult(msg.GetReadResult())
	case msg.GetGrepResult() != nil:
		return summarizeGrepResult(msg.GetGrepResult())
	case msg.GetLsResult() != nil:
		return summarizeLsResult(msg.GetLsResult())
	case msg.GetShellResult() != nil:
		return summarizeShellResult(msg.GetShellResult())
	default:
		return nil
	}
}

func summarizeReadResult(result *agentv1.ReadResult) map[string]any {
	if result == nil {
		return nil
	}
	if success := result.GetSuccess(); success != nil {
		return map[string]any{
			"status":       "success",
			"path":         sanitize(success.GetPath()),
			"total_lines":  success.GetTotalLines(),
			"file_size":    success.GetFileSize(),
			"truncated":    success.GetTruncated(),
			"content_len":  len(success.GetContent()),
			"data_len":     len(success.GetData()),
			"content_head": truncate(sanitize(success.GetContent()), 500),
		}
	}
	if err := result.GetError(); err != nil {
		return map[string]any{"status": "error", "path": sanitize(err.GetPath()), "error": truncate(sanitize(err.GetError()), 500)}
	}
	if rejected := result.GetRejected(); rejected != nil {
		return map[string]any{"status": "rejected", "reason": truncate(sanitize(rejected.GetReason()), 500)}
	}
	if notFound := result.GetFileNotFound(); notFound != nil {
		return map[string]any{"status": "file_not_found", "path": sanitize(notFound.GetPath())}
	}
	if denied := result.GetPermissionDenied(); denied != nil {
		return map[string]any{"status": "permission_denied", "path": sanitize(denied.GetPath())}
	}
	if invalid := result.GetInvalidFile(); invalid != nil {
		return map[string]any{"status": "invalid_file", "reason": truncate(sanitize(invalid.GetReason()), 500)}
	}
	return map[string]any{"status": fmt.Sprintf("%T", result.GetResult())}
}

func summarizeGrepResult(result *agentv1.GrepResult) map[string]any {
	if result == nil {
		return nil
	}
	if success := result.GetSuccess(); success != nil {
		workspaceCount := 0
		matchCount := 0
		matchedLines := int32(0)
		fileCount := 0
		for _, union := range success.GetWorkspaceResults() {
			workspaceCount++
			if content := union.GetContent(); content != nil {
				matchCount += len(content.GetMatches())
				matchedLines += content.GetTotalMatchedLines()
			}
			if files := union.GetFiles(); files != nil {
				fileCount += len(files.GetFiles())
			}
		}
		if active := success.GetActiveEditorResult(); active != nil {
			if content := active.GetContent(); content != nil {
				matchCount += len(content.GetMatches())
				matchedLines += content.GetTotalMatchedLines()
			}
			if files := active.GetFiles(); files != nil {
				fileCount += len(files.GetFiles())
			}
		}
		return map[string]any{
			"status":          "success",
			"pattern":         truncate(sanitize(success.GetPattern()), 300),
			"path":            sanitize(success.GetPath()),
			"output_mode":     success.GetOutputMode(),
			"workspace_count": workspaceCount,
			"file_count":      fileCount,
			"match_count":     matchCount,
			"matched_lines":   matchedLines,
		}
	}
	if err := result.GetError(); err != nil {
		return map[string]any{"status": "error", "error": truncate(sanitize(err.GetError()), 500)}
	}
	return map[string]any{"status": fmt.Sprintf("%T", result.GetResult())}
}

func summarizeLsResult(result *agentv1.LsResult) map[string]any {
	if result == nil {
		return nil
	}
	if success := result.GetSuccess(); success != nil {
		root := success.GetDirectoryTreeRoot()
		out := map[string]any{"status": "success"}
		if root != nil {
			out["path"] = sanitize(root.GetAbsPath())
			out["dirs"] = len(root.GetChildrenDirs())
			out["files"] = len(root.GetChildrenFiles())
			out["num_files"] = root.GetNumFiles()
			out["children_processed"] = root.GetChildrenWereProcessed()
		}
		return out
	}
	if err := result.GetError(); err != nil {
		return map[string]any{"status": "error", "path": sanitize(err.GetPath()), "error": truncate(sanitize(err.GetError()), 500)}
	}
	if rejected := result.GetRejected(); rejected != nil {
		return map[string]any{"status": "rejected", "reason": truncate(sanitize(rejected.GetReason()), 500)}
	}
	if timeout := result.GetTimeout(); timeout != nil {
		root := timeout.GetDirectoryTreeRoot()
		path := ""
		if root != nil {
			path = sanitize(root.GetAbsPath())
		}
		return map[string]any{"status": "timeout", "path": path}
	}
	return map[string]any{"status": fmt.Sprintf("%T", result.GetResult())}
}

func summarizeShellResult(result *agentv1.ShellResult) map[string]any {
	if result == nil {
		return nil
	}
	base := map[string]any{"pid": result.GetPid(), "background": result.GetIsBackground()}
	if success := result.GetSuccess(); success != nil {
		base["status"] = "success"
		base["command"] = truncate(sanitize(success.GetCommand()), 500)
		base["working_directory"] = sanitize(success.GetWorkingDirectory())
		base["exit_code"] = success.GetExitCode()
		base["stdout_len"] = len(success.GetStdout())
		base["stderr_len"] = len(success.GetStderr())
		base["interleaved_len"] = len(success.GetInterleavedOutput())
		base["stdout_head"] = truncate(sanitize(success.GetStdout()), 500)
		base["stderr_head"] = truncate(sanitize(success.GetStderr()), 500)
		return base
	}
	if failure := result.GetFailure(); failure != nil {
		base["status"] = "failure"
		base["command"] = truncate(sanitize(failure.GetCommand()), 500)
		base["working_directory"] = sanitize(failure.GetWorkingDirectory())
		base["exit_code"] = failure.GetExitCode()
		base["stdout_len"] = len(failure.GetStdout())
		base["stderr_len"] = len(failure.GetStderr())
		base["stdout_head"] = truncate(sanitize(failure.GetStdout()), 500)
		base["stderr_head"] = truncate(sanitize(failure.GetStderr()), 500)
		return base
	}
	if timeout := result.GetTimeout(); timeout != nil {
		base["status"] = "timeout"
		base["command"] = truncate(sanitize(timeout.GetCommand()), 500)
		base["working_directory"] = sanitize(timeout.GetWorkingDirectory())
		base["timeout_ms"] = timeout.GetTimeoutMs()
		return base
	}
	if rejected := result.GetRejected(); rejected != nil {
		base["status"] = "rejected"
		base["reason"] = truncate(sanitize(rejected.GetReason()), 500)
		return base
	}
	if spawn := result.GetSpawnError(); spawn != nil {
		base["status"] = "spawn_error"
		base["command"] = truncate(sanitize(spawn.GetCommand()), 500)
		base["working_directory"] = sanitize(spawn.GetWorkingDirectory())
		base["error"] = truncate(sanitize(spawn.GetError()), 500)
		return base
	}
	if denied := result.GetPermissionDenied(); denied != nil {
		base["status"] = "permission_denied"
		base["command"] = truncate(sanitize(denied.GetCommand()), 500)
		base["working_directory"] = sanitize(denied.GetWorkingDirectory())
		base["error"] = truncate(sanitize(denied.GetError()), 500)
		base["is_readonly"] = denied.GetIsReadonly()
		return base
	}
	base["status"] = fmt.Sprintf("%T", result.GetResult())
	return base
}

func summarizeModelParameterDefinitions(defs []*aiserverv1.ModelParameterDefinition) []map[string]any {
	out := []map[string]any{}
	for _, def := range defs {
		if def == nil {
			continue
		}
		item := map[string]any{"id": def.GetId(), "name": def.GetName()}
		if enum := def.GetParameterType().GetEnumParameter(); enum != nil {
			values := []map[string]any{}
			for _, value := range enum.GetValues() {
				values = append(values, map[string]any{"value": value.GetValue(), "display": value.GetDisplayName()})
			}
			item["type"] = "enum"
			item["values"] = values
		} else if def.GetParameterType().GetBooleanParameter() != nil {
			item["type"] = "boolean"
		}
		out = append(out, item)
	}
	return out
}

func summarizeModelVariants(variants []*aiserverv1.AvailableModelsResponse_ModelVariantConfig) []map[string]any {
	out := []map[string]any{}
	for _, variant := range variants {
		if variant == nil {
			continue
		}
		params := []map[string]any{}
		for _, value := range variant.GetParameterValues() {
			params = append(params, map[string]any{"id": value.GetId(), "value": value.GetValue()})
		}
		out = append(out, map[string]any{
			"display":                   variant.GetDisplayName(),
			"is_max_mode":               variant.GetIsMaxMode(),
			"is_default_max_config":     variant.GetIsDefaultMaxConfig(),
			"is_default_non_max_config": variant.GetIsDefaultNonMaxConfig(),
			"parameter_values":          params,
		})
	}
	return out
}

func featureDefault(cfg *aiserverv1.AvailableModelsResponse_FeatureModelConfig) string {
	if cfg == nil {
		return ""
	}
	return cfg.GetDefaultModel()
}

func summarizeProviderRequest(req *http.Request, body []byte) map[string]any {
	if req == nil || len(body) == 0 || !isLikelyProviderAPI(req) {
		return nil
	}
	contentType := strings.ToLower(req.Header.Get("Content-Type"))
	if !strings.Contains(contentType, "json") {
		return map[string]any{"body_sample": summarizeRawBody(body)}
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return map[string]any{"decode_error": err.Error(), "body_sample": summarizeRawBody(body)}
	}
	out := map[string]any{
		"api_family": providerAPIFamily(req),
		"model":      stringFromMap(payload, "model"),
		"stream":     payload["stream"],
		"keys":       sortedJSONKeys(payload),
	}
	if messages, ok := payload["messages"].([]any); ok {
		out["messages"] = summarizeJSONArray(messages, 8)
	}
	if input, ok := payload["input"].([]any); ok {
		out["input"] = summarizeJSONArray(input, 8)
	} else if input, ok := payload["input"].(string); ok {
		out["input_text_len"] = len(input)
		out["input_text_head"] = truncate(sanitize(input), 500)
	}
	if tools, ok := payload["tools"].([]any); ok {
		out["tools"] = summarizeProviderTools(tools)
	}
	if reasoning, ok := payload["reasoning"].(map[string]any); ok {
		out["reasoning"] = sanitizedJSONMap(reasoning)
	}
	if thinking, ok := payload["thinking"].(map[string]any); ok {
		out["thinking"] = sanitizedJSONMap(thinking)
	}
	if extra := summarizeToolResultCarrier(payload); extra != nil {
		out["tool_result_carrier"] = extra
	}
	return out
}

func summarizeProviderResponse(req *http.Request, resp *http.Response, body []byte) map[string]any {
	if req == nil || len(body) == 0 || !isLikelyProviderAPI(req) {
		return nil
	}
	contentType := ""
	if resp != nil {
		contentType = resp.Header.Get("Content-Type")
	}
	out := map[string]any{"api_family": providerAPIFamily(req)}
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		out["sse"] = summarizeProviderSSE(body)
		return out
	}
	if strings.Contains(strings.ToLower(contentType), "json") || looksJSON(body) {
		var payload any
		if err := json.Unmarshal(body, &payload); err != nil {
			out["decode_error"] = err.Error()
			out["body_sample"] = summarizeRawBody(body)
			return out
		}
		out["json"] = summarizeJSONValue(payload)
		return out
	}
	out["body_sample"] = summarizeRawBody(body)
	return out
}

func summarizeProviderSSE(body []byte) map[string]any {
	return summarizeSSEBodyWithParser(body, func(eventName string, data string) map[string]any {
		if data == "[DONE]" {
			return map[string]any{"done": true}
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			return map[string]any{"event": eventName, "data_len": len(data), "data_head": truncate(sanitize(data), 500)}
		}
		item := map[string]any{"event": eventName, "type": stringFromMap(payload, "type")}
		if text := providerTextDelta(payload); text != "" {
			item["text_len"] = len(text)
			item["text_head"] = truncate(sanitize(text), 300)
		}
		if calls := providerToolCalls(payload); len(calls) > 0 {
			item["tool_calls"] = calls
		}
		if usage := providerUsage(payload); usage != nil {
			item["usage"] = usage
		}
		return item
	})
}

func summarizeSSEBody(body []byte) map[string]any {
	return summarizeSSEBodyWithParser(body, func(eventName string, data string) map[string]any {
		return map[string]any{"event": eventName, "data_len": len(data), "data_head": truncate(sanitize(data), 500)}
	})
}

func summarizeSSEBodyWithParser(body []byte, parse func(eventName string, data string) map[string]any) map[string]any {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	eventName := "message"
	dataLines := []string{}
	counts := map[string]int{}
	items := []map[string]any{}
	flush := func() {
		if len(dataLines) == 0 {
			return
		}
		data := strings.TrimSpace(strings.Join(dataLines, "\n"))
		dataLines = nil
		if data == "" {
			return
		}
		counts[eventName]++
		if parse != nil {
			items = appendLimited(items, parse(eventName, data), 120)
		}
	}
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			flush()
			eventName = "message"
			continue
		}
		if strings.HasPrefix(trimmed, ":") {
			counts["comment"]++
			continue
		}
		if strings.HasPrefix(trimmed, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(trimmed, "event:"))
			if eventName == "" {
				eventName = "message"
			}
			continue
		}
		if strings.HasPrefix(trimmed, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")))
		}
	}
	flush()
	out := map[string]any{"events": counts, "samples": items}
	if err := scanner.Err(); err != nil {
		out["scan_error"] = err.Error()
	}
	return out
}

func summarizeRawBody(body []byte) map[string]any {
	return map[string]any{
		"bytes":      len(body),
		"text_head":  truncate(sanitize(string(body)), 1000),
		"hex_prefix": hex.EncodeToString(body[:minInt(len(body), 64)]),
	}
}

func isLikelyProviderAPI(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}
	path := strings.ToLower(req.URL.Path)
	if strings.Contains(path, "/v1/responses") || strings.Contains(path, "/responses") || strings.Contains(path, "/v1/chat/completions") || strings.Contains(path, "/chat/completions") || strings.Contains(path, "/v1/messages") || strings.Contains(path, "/messages") {
		return true
	}
	return isProviderHost(req.Host) || isProviderHost(req.URL.Host)
}

func providerAPIFamily(req *http.Request) string {
	path := strings.ToLower(requestPathOnly(req))
	switch {
	case strings.Contains(path, "responses"):
		return "openai_responses"
	case strings.Contains(path, "chat/completions"):
		return "openai_chat"
	case strings.Contains(path, "messages"):
		return "anthropic_messages"
	default:
		return "unknown"
	}
}

func shouldLogBodySample(contentType string, body []byte) bool {
	if len(body) == 0 {
		return false
	}
	ct := strings.ToLower(contentType)
	return strings.Contains(ct, "json") || strings.Contains(ct, "text") || strings.Contains(ct, "event-stream")
}

func looksJSON(body []byte) bool {
	trimmed := bytes.TrimSpace(body)
	return len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
}

func stringFromMap(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if value, ok := m[key].(string); ok {
		return value
	}
	return ""
}

func sortedJSONKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sanitizedJSONMap(m map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range m {
		out[key] = summarizeJSONValue(value)
	}
	return out
}

func summarizeJSONArray(items []any, limit int) map[string]any {
	samples := []map[string]any{}
	roleCounts := map[string]int{}
	for i, item := range items {
		if m, ok := item.(map[string]any); ok {
			role := stringFromMap(m, "role")
			if role != "" {
				roleCounts[role]++
			}
			if i < limit {
				samples = append(samples, summarizeJSONMapShallow(m))
			}
		}
	}
	return map[string]any{"count": len(items), "roles": roleCounts, "samples": samples}
}

func summarizeJSONMapShallow(m map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range m {
		switch key {
		case "content", "input", "output", "arguments":
			out[key] = summarizeJSONValue(value)
		default:
			if s, ok := value.(string); ok {
				out[key] = truncate(sanitize(s), 300)
			} else {
				out[key] = summarizeJSONValue(value)
			}
		}
	}
	return out
}

func summarizeJSONValue(value any) any {
	switch v := value.(type) {
	case nil, bool, float64:
		return v
	case string:
		return map[string]any{"len": len(v), "head": truncate(sanitize(v), 500)}
	case []any:
		out := map[string]any{"count": len(v)}
		samples := []any{}
		for i, item := range v {
			if i >= 5 {
				break
			}
			samples = append(samples, summarizeJSONValue(item))
		}
		out["samples"] = samples
		return out
	case map[string]any:
		out := map[string]any{"keys": sortedJSONKeys(v)}
		for _, key := range []string{"type", "role", "id", "call_id", "name", "model", "status"} {
			if s, ok := v[key].(string); ok {
				out[key] = truncate(sanitize(s), 300)
			}
		}
		if content, ok := v["content"]; ok {
			out["content"] = summarizeJSONValue(content)
		}
		if text, ok := v["text"]; ok {
			out["text"] = summarizeJSONValue(text)
		}
		return out
	default:
		return fmt.Sprintf("%T", value)
	}
}

func summarizeProviderTools(tools []any) []map[string]any {
	out := []map[string]any{}
	for _, tool := range tools {
		m, ok := tool.(map[string]any)
		if !ok {
			continue
		}
		name := stringFromMap(m, "name")
		if fn, ok := m["function"].(map[string]any); ok && name == "" {
			name = stringFromMap(fn, "name")
		}
		out = append(out, map[string]any{"type": stringFromMap(m, "type"), "name": name})
	}
	return out
}

func summarizeToolResultCarrier(payload map[string]any) map[string]any {
	if payload == nil {
		return nil
	}
	count := 0
	chars := 0
	var walk func(any)
	walk = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			if typ := stringFromMap(v, "type"); strings.Contains(typ, "tool") || typ == "function_call_output" {
				count++
			}
			for _, key := range []string{"output", "content"} {
				if s, ok := v[key].(string); ok {
					chars += len(s)
				}
			}
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(payload)
	if count == 0 && chars == 0 {
		return nil
	}
	return map[string]any{"tool_like_items": count, "tool_result_chars": chars}
}

func providerTextDelta(payload map[string]any) string {
	for _, key := range []string{"delta", "text", "output_text"} {
		if s, ok := payload[key].(string); ok {
			return s
		}
	}
	if choices, ok := payload["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if delta, ok := choice["delta"].(map[string]any); ok {
				return stringFromMap(delta, "content")
			}
		}
	}
	return ""
}

func providerToolCalls(payload map[string]any) []map[string]any {
	out := []map[string]any{}
	if item, ok := payload["item"].(map[string]any); ok {
		if typ := stringFromMap(item, "type"); strings.Contains(typ, "function") || strings.Contains(typ, "tool") {
			out = append(out, map[string]any{"id": firstNonEmpty(stringFromMap(item, "call_id"), stringFromMap(item, "id")), "name": stringFromMap(item, "name"), "type": typ, "arguments_len": len(stringFromMap(item, "arguments"))})
		}
	}
	if choices, ok := payload["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if delta, ok := choice["delta"].(map[string]any); ok {
				if calls, ok := delta["tool_calls"].([]any); ok {
					for _, call := range calls {
						if m, ok := call.(map[string]any); ok {
							fn, _ := m["function"].(map[string]any)
							out = append(out, map[string]any{"id": stringFromMap(m, "id"), "name": stringFromMap(fn, "name"), "index": m["index"], "arguments_len": len(stringFromMap(fn, "arguments"))})
						}
					}
				}
			}
		}
	}
	return out
}

func providerUsage(payload map[string]any) map[string]any {
	if usage, ok := payload["usage"].(map[string]any); ok {
		return sanitizedJSONMap(usage)
	}
	return nil
}

type frame struct {
	Flag    byte
	Payload []byte
}

func payloadFrames(body []byte, contentType string) ([]frame, error) {
	if isGRPCWebTextContentType(contentType) {
		decoded, err := decodeGRPCWebTextBody(body)
		if err != nil {
			return nil, err
		}
		body = decoded
	}
	if looksFramedAny(body) {
		return parseFrames(body)
	}
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		return payloadFramesFromSSE(body)
	}
	return nil, fmt.Errorf("response is not framed")
}

func payloadFramesFromSSE(body []byte) ([]frame, error) {
	frames := []frame{}
	eventName := ""
	dataLines := []string{}
	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		data := strings.TrimSpace(strings.Join(dataLines, "\n"))
		dataLines = nil
		if data == "" || data == "[DONE]" {
			return nil
		}
		if eventName != "" && eventName != "message" && eventName != "data" {
			payload, _ := json.Marshal(map[string]any{"event": eventName, "data": data})
			frames = append(frames, frame{Payload: payload})
			return nil
		}
		decoded, err := decodeMaybeBase64(data)
		if err != nil {
			decoded = []byte(data)
		}
		if looksFramedAny(decoded) {
			parsed, err := parseFrames(decoded)
			if err != nil {
				return err
			}
			frames = append(frames, parsed...)
			return nil
		}
		frames = append(frames, frame{Payload: decoded})
		return nil
	}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if err := flush(); err != nil {
				return nil, err
			}
			eventName = ""
			continue
		}
		if strings.HasPrefix(trimmed, ":") {
			continue
		}
		if strings.HasPrefix(trimmed, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(trimmed, "event:"))
			continue
		}
		if strings.HasPrefix(trimmed, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")))
			continue
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("no SSE data frames")
	}
	return frames, nil
}

func decodeMaybeBase64(data string) ([]byte, error) {
	compact := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\n', '\r', '\t':
			return -1
		default:
			return r
		}
	}, data)
	if compact == "" {
		return nil, fmt.Errorf("empty data")
	}
	encodings := []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding}
	for _, enc := range encodings {
		decoded := make([]byte, enc.DecodedLen(len(compact)))
		n, err := enc.Decode(decoded, []byte(compact))
		if err == nil {
			return decoded[:n], nil
		}
	}
	return nil, fmt.Errorf("not base64")
}

func parseFrames(body []byte) ([]frame, error) {
	frames := []frame{}
	for pos := 0; pos < len(body); {
		if len(body)-pos < 5 {
			return frames, fmt.Errorf("trailing %d byte(s) after frames", len(body)-pos)
		}
		flag := body[pos]
		length := int(binary.BigEndian.Uint32(body[pos+1 : pos+5]))
		start := pos + 5
		end := start + length
		if length < 0 || end > len(body) {
			return frames, fmt.Errorf("invalid frame length %d at %d", length, pos)
		}
		frames = append(frames, frame{Flag: flag, Payload: append([]byte(nil), body[start:end]...)})
		pos = end
	}
	return frames, nil
}

func firstPayload(body []byte, contentType string) ([]byte, error) {
	frames, err := payloadFrames(body, contentType)
	if err == nil {
		for _, frame := range frames {
			if frame.Flag&0x80 != 0 || frame.Flag&0x02 != 0 {
				continue
			}
			if frame.Flag&0x01 != 0 {
				return nil, fmt.Errorf("compressed proto frames are not supported")
			}
			return frame.Payload, nil
		}
		return nil, fmt.Errorf("no proto payload frame found")
	}
	return body, nil
}

func decodeGRPCWebTextBody(body []byte) ([]byte, error) {
	compact := make([]byte, 0, len(body))
	for _, b := range body {
		switch b {
		case ' ', '\n', '\r', '\t':
			continue
		default:
			compact = append(compact, b)
		}
	}
	if len(compact) == 0 {
		return compact, nil
	}
	decoded := make([]byte, base64.StdEncoding.DecodedLen(len(compact)))
	n, err := base64.StdEncoding.Decode(decoded, compact)
	if err == nil {
		return decoded[:n], nil
	}
	decoded = make([]byte, base64.RawStdEncoding.DecodedLen(len(compact)))
	n, rawErr := base64.RawStdEncoding.Decode(decoded, compact)
	if rawErr == nil {
		return decoded[:n], nil
	}
	return nil, fmt.Errorf("invalid grpc-web-text body: %w", err)
}

func decodeBodyContent(body []byte, header http.Header) ([]byte, error) {
	decoded := body
	var err error
	if encoding := header.Get("Content-Encoding"); encoding != "" {
		decoded, err = decodeContentEncoding(decoded, encoding)
		if err != nil {
			return nil, err
		}
	}
	for _, headerName := range []string{"Connect-Content-Encoding", "Grpc-Encoding"} {
		encoding := header.Get(headerName)
		if encoding == "" || !looksCompressedBody(decoded, encoding) {
			continue
		}
		next, err := decodeContentEncoding(decoded, encoding)
		if err != nil {
			continue
		}
		decoded = next
	}
	return decoded, nil
}

func decodeContentEncoding(body []byte, encoding string) ([]byte, error) {
	out := body
	for _, part := range strings.Split(strings.ToLower(encoding), ",") {
		switch strings.TrimSpace(part) {
		case "", "identity":
			continue
		case "gzip":
			reader, err := gzip.NewReader(bytes.NewReader(out))
			if err != nil {
				return nil, err
			}
			data, readErr := io.ReadAll(reader)
			closeErr := reader.Close()
			if readErr != nil {
				return nil, readErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
			out = data
		case "deflate", "zlib":
			reader, err := zlib.NewReader(bytes.NewReader(out))
			if err != nil {
				return nil, err
			}
			data, readErr := io.ReadAll(reader)
			closeErr := reader.Close()
			if readErr != nil {
				return nil, readErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
			out = data
		default:
			return nil, fmt.Errorf("unsupported content encoding %q", part)
		}
	}
	return out, nil
}

func looksCompressedBody(body []byte, encoding string) bool {
	for _, part := range strings.Split(strings.ToLower(encoding), ",") {
		switch strings.TrimSpace(part) {
		case "gzip":
			return len(body) >= 2 && body[0] == 0x1f && body[1] == 0x8b
		case "deflate", "zlib":
			return len(body) >= 2 && body[0] == 0x78
		}
	}
	return false
}

func looksFramedAny(body []byte) bool {
	if len(body) < 5 {
		return false
	}
	length := int(binary.BigEndian.Uint32(body[1:5]))
	return length >= 0 && length <= len(body)-5
}

func isGRPCWebTextContentType(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "grpc-web-text")
}

func readAndRestoreRequestBody(req *http.Request) []byte {
	if req == nil || req.Body == nil {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, captureLimit))
	_ = req.Body.Close()
	if err != nil {
		req.Body = io.NopCloser(bytes.NewReader(nil))
		return nil
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	return body
}

func requestedModelSummary(req *agentv1.AgentRunRequest) (string, string) {
	if req == nil {
		return "", ""
	}
	if requested := req.GetRequestedModel(); requested != nil {
		effort := ""
		if requested.GetMaxMode() {
			effort = "max"
		}
		for _, param := range requested.GetParameters() {
			if strings.Contains(strings.ToLower(param.GetId()), "thinking") {
				effort = param.GetValue()
			}
		}
		return requested.GetModelId(), effort
	}
	if details := req.GetModelDetails(); details != nil {
		effort := ""
		if details.GetMaxMode() {
			effort = "max"
		}
		return firstNonEmpty(details.GetModelId(), details.GetDisplayModelId()), effort
	}
	return "", ""
}

func workspaceFromRunRequest(req *agentv1.AgentRunRequest) string {
	if req == nil {
		return ""
	}
	if state := req.GetConversationState(); state != nil {
		for _, uri := range state.GetPreviousWorkspaceUris() {
			if uri != "" {
				return sanitize(uri)
			}
		}
	}
	return workspaceFromConversationAction(req.GetAction())
}

func workspaceFromConversationAction(action *agentv1.ConversationAction) string {
	if action == nil {
		return ""
	}
	if uma := action.GetUserMessageAction(); uma != nil {
		if root := workspaceFromRequestContext(uma.GetRequestContext()); root != "" {
			return root
		}
	}
	if start := action.GetStartPlanAction(); start != nil {
		return workspaceFromRequestContext(start.GetRequestContext())
	}
	if exec := action.GetExecutePlanAction(); exec != nil {
		return workspaceFromRequestContext(exec.GetRequestContext())
	}
	if resume := action.GetResumeAction(); resume != nil {
		return workspaceFromRequestContext(resume.GetRequestContext())
	}
	return ""
}

func workspaceFromRequestContext(ctx *agentv1.RequestContext) string {
	if ctx == nil || ctx.GetEnv() == nil {
		return ""
	}
	env := ctx.GetEnv()
	if len(env.GetWorkspacePaths()) > 0 {
		return sanitize(env.GetWorkspacePaths()[0])
	}
	return sanitize(env.GetProjectFolder())
}

func textFromConversationAction(action *agentv1.ConversationAction) string {
	if action == nil {
		return ""
	}
	if uma := action.GetUserMessageAction(); uma != nil && uma.GetUserMessage() != nil {
		msg := uma.GetUserMessage()
		if msg.GetText() != "" {
			return msg.GetText()
		}
		return msg.GetRichText()
	}
	return ""
}

func agentClientMessageKind(msg *agentv1.AgentClientMessage) string {
	switch {
	case msg == nil || msg.GetMessage() == nil:
		return "empty"
	case msg.GetRunRequest() != nil:
		return "run_request"
	case msg.GetConversationAction() != nil:
		return "conversation_action"
	case msg.GetExecClientMessage() != nil:
		return "exec_client_message"
	case msg.GetExecClientControlMessage() != nil:
		return "exec_client_control"
	case msg.GetClientHeartbeat() != nil:
		return "client_heartbeat"
	default:
		return fmt.Sprintf("%T", msg.GetMessage())
	}
}

func execClientKind(msg *agentv1.ExecClientMessage) string {
	switch {
	case msg.GetShellResult() != nil:
		return "shell_result"
	case msg.GetWriteResult() != nil:
		return "write_result"
	case msg.GetDeleteResult() != nil:
		return "delete_result"
	case msg.GetGrepResult() != nil:
		return "grep_result"
	case msg.GetReadResult() != nil:
		return "read_result"
	case msg.GetLsResult() != nil:
		return "ls_result"
	case msg.GetDiagnosticsResult() != nil:
		return "diagnostics_result"
	case msg.GetRequestContextResult() != nil:
		return "request_context_result"
	case msg.GetMcpResult() != nil:
		return "mcp_result"
	case msg.GetFetchResult() != nil:
		return "fetch_result"
	default:
		return fmt.Sprintf("%T", msg.GetMessage())
	}
}

func sanitizedHeaderSummary(header http.Header) map[string]any {
	out := map[string]any{}
	for key, values := range header {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "authorization") || strings.Contains(lower, "cookie") || strings.Contains(lower, "api-key") || strings.Contains(lower, "token") {
			out[key] = "[redacted]"
			continue
		}
		if len(values) == 1 {
			out[key] = truncate(sanitize(values[0]), 200)
		} else if len(values) > 1 {
			out[key] = fmt.Sprintf("%d values", len(values))
		}
	}
	return out
}

func sanitize(value string) string {
	return sensitiveRE.ReplaceAllString(value, "${1}${3}[redacted]")
}

func truncate(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

func appendLimited(items []map[string]any, item map[string]any, limit int) []map[string]any {
	if len(items) >= limit {
		return items
	}
	return append(items, item)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func ensureAbsoluteURL(req *http.Request) {
	if req.URL.Scheme == "" {
		req.URL.Scheme = "https"
	}
	if req.URL.Host == "" {
		req.URL.Host = req.Host
	}
}

func requestHost(req *http.Request) string {
	if req == nil {
		return ""
	}
	if req.Host != "" {
		return req.Host
	}
	if req.URL != nil {
		return req.URL.Host
	}
	return ""
}

func requestPath(req *http.Request) string {
	if req == nil || req.URL == nil {
		return ""
	}
	path := req.URL.Path
	if req.URL.RawQuery != "" {
		path += "?" + req.URL.RawQuery
	}
	return truncate(path, 500)
}

func requestPathOnly(req *http.Request) string {
	if req == nil || req.URL == nil {
		return ""
	}
	return req.URL.Path
}

func isCursorHost(host string) bool {
	host = strings.ToLower(host)
	return strings.Contains(host, "cursor.sh") || strings.Contains(host, "cursor.com")
}

func isProviderHost(host string) bool {
	host = strings.ToLower(host)
	host = stripHostPort(host)
	return strings.Contains(host, "openai.com") || strings.Contains(host, "anthropic.com") || strings.Contains(host, "openrouter.ai")
}

func shouldCaptureRequest(req *http.Request, captureAll bool) bool {
	if req == nil || req.URL == nil {
		return false
	}
	if captureAll {
		return true
	}
	return isCursorHost(req.Host) || isCursorHost(req.URL.Host) || isProviderHost(req.Host) || isProviderHost(req.URL.Host) || isLikelyProviderAPI(req)
}

func withSession(event tapEvent, ctx *goproxy.ProxyCtx) tapEvent {
	if ctx != nil {
		event.Session = ctx.Session
	}
	return event
}

func withSource(event tapEvent, source string) tapEvent {
	event.Source = source
	return event
}

func conditionalUpstreamProxy(proxyURL *url.URL) func(*http.Request) (*url.URL, error) {
	return func(req *http.Request) (*url.URL, error) {
		if req == nil || req.URL == nil {
			return nil, nil
		}
		if isCursorHost(req.Host) || isCursorHost(req.URL.Host) {
			return proxyURL, nil
		}
		return nil, nil
	}
}

func startDirectCaptureProxy(listenAddr string, ca *certs.CA, logger *tapLogger, captureAll bool) (*http.Server, net.Listener, error) {
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, nil, err
	}
	proxy := goproxy.NewProxyHttpServer()
	proxy.Verbose = false
	proxy.Tr = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, ForceAttemptHTTP2: false}
	proxy.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		ensureAbsoluteURL(req)
		if shouldCaptureRequest(req, captureAll) {
			body := readAndRestoreRequestBody(req)
			logger.Log(withSource(withSession(summarizeRequest(req, body), ctx), "reference_egress"))
		}
		return req, nil
	})
	proxy.OnResponse().DoFunc(func(resp *http.Response, ctx *goproxy.ProxyCtx) *http.Response {
		if resp == nil || ctx == nil || ctx.Req == nil {
			return resp
		}
		req := ctx.Req
		if !shouldCaptureRequest(req, captureAll) {
			return resp
		}
		resp.Body = &bodyTapper{
			ReadCloser: resp.Body,
			logger:     logger,
			req:        req,
			resp:       resp,
			session:    ctx.Session,
			limit:      captureLimit,
			onClose: func(body []byte, truncated bool) {
				event := summarizeResponse(req, resp, body)
				event.Session = ctx.Session
				event.Source = "reference_egress"
				if truncated {
					if event.Details == nil {
						event.Details = map[string]any{}
					}
					event.Details["capture_truncated"] = true
				}
				logger.Log(event)
			},
		}
		return resp
	})
	proxy.OnRequest().HandleConnectFunc(func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
		if captureAll || isCursorHost(host) || isProviderHost(host) {
			logger.Log(tapEvent{Session: ctx.Session, Source: "reference_egress", Direction: "connect", Method: "CONNECT", Host: sanitize(host)})
		}
		return &goproxy.ConnectAction{
			Action: goproxy.ConnectMitm,
			TLSConfig: func(host string, ctx *goproxy.ProxyCtx) (*tls.Config, error) {
				return tlsConfigForHost(ca, host), nil
			},
		}, host
	})
	server := &http.Server{Handler: proxy}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("reference capture proxy error: %v", err)
		}
	}()
	logger.Log(tapEvent{Direction: "reference_capture_start", Details: map[string]any{"listen": listenAddr}})
	return server, listener, nil
}

func startReferenceApp(exePath string, proxyURL string) (*exec.Cmd, error) {
	exePath = strings.TrimSpace(exePath)
	if exePath == "" {
		return nil, fmt.Errorf("reference executable path is empty")
	}
	cmd := exec.Command(exePath)
	cmd.Env = append(os.Environ(),
		"HTTP_PROXY="+proxyURL,
		"HTTPS_PROXY="+proxyURL,
		"ALL_PROXY="+proxyURL,
		"http_proxy="+proxyURL,
		"https_proxy="+proxyURL,
		"all_proxy="+proxyURL,
		"NO_PROXY=127.0.0.1,localhost,::1",
		"no_proxy=127.0.0.1,localhost,::1",
	)
	cmd.Dir = filepath.Dir(exePath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

func isRunSSERequest(req *http.Request) bool {
	return strings.Contains(requestPathOnly(req), "/agent.v1.AgentService/RunSSE")
}

func requestMethod(req *http.Request) string {
	if req == nil {
		return ""
	}
	return req.Method
}

func responseStatus(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}

func responseContentType(resp *http.Response) string {
	if resp == nil {
		return ""
	}
	return resp.Header.Get("Content-Type")
}

func tlsConfigForHost(ca *certs.CA, hostname string) *tls.Config {
	return &tls.Config{GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		name := hello.ServerName
		if name == "" {
			name = stripHostPort(hostname)
		}
		return ca.GenerateServerCert(name)
	}}
}

func stripHostPort(hostport string) string {
	host, _, err := net.SplitHostPort(hostport)
	if err == nil {
		return host
	}
	return strings.Trim(hostport, "[]")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func applyTemporaryCursorProxy(proxyURL string) (*settingsBackup, error) {
	path, err := cursorSettingsPath()
	if err != nil {
		return nil, err
	}
	backup := &settingsBackup{path: path}
	raw, err := os.ReadFile(path)
	if err == nil {
		backup.exists = true
		backup.content = append([]byte(nil), raw...)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	settings := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &settings)
	}
	settings["cursor.general.disableHttp2"] = true
	settings["http.experimental.systemCertificatesV2"] = true
	settings["http.proxy"] = proxyURL
	settings["http.proxyKerberosServicePrincipal"] = proxyURL
	settings["http.proxyStrictSSL"] = false
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return nil, err
	}
	return backup, nil
}

func (b *settingsBackup) restore() error {
	if b == nil || b.path == "" {
		return nil
	}
	if b.exists {
		return os.WriteFile(b.path, b.content, 0600)
	}
	if err := os.Remove(b.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func cursorSettingsPath() (string, error) {
	base := os.Getenv("APPDATA")
	if base == "" {
		return "", fmt.Errorf("APPDATA is not set")
	}
	return filepath.Join(base, "Cursor", "User", "settings.json"), nil
}
