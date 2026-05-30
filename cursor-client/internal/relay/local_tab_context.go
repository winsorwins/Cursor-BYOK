package relay

import (
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	aiserverv1 "github.com/burpheart/cursor-tap/cursor_proto/gen/aiserver/v1"
	"google.golang.org/protobuf/proto"
)

const localCppBackendURL = "https://tab.leokun.cn"

type fileSyncCacheEntry struct {
	UUID         string
	Path         string
	Contents     string
	Hash         string
	ModelVersion int32
	UpdatedAt    time.Time
}

func buildLocalCppConfigPayload() []byte {
	trueValue := true
	aboveRadius := int32(80)
	belowRadius := int32(40)
	mergeLimit := int32(240)
	mergeRadius := int32(80)
	tabDebounce := int32(200)
	tabEditorDebounce := int32(120)
	maxCleared := int32(8)

	resp := &aiserverv1.CppConfigResponse{
		AboveRadius:                        &aboveRadius,
		BelowRadius:                        &belowRadius,
		MergeBehavior:                      &aiserverv1.CppConfigResponse_MergeBehavior{Type: "radius", Limit: &mergeLimit, Radius: &mergeRadius},
		IsOn:                               &trueValue,
		IsGhostText:                        &trueValue,
		ShouldLetUserEnableCppEvenIfNotPro: &trueValue,
		Heuristics: []aiserverv1.CppConfigResponse_Heuristic{
			aiserverv1.CppConfigResponse_HEURISTIC_LOTS_OF_ADDED_TEXT,
			aiserverv1.CppConfigResponse_HEURISTIC_DUPLICATING_LINE_AFTER_SUGGESTION,
			aiserverv1.CppConfigResponse_HEURISTIC_DUPLICATING_MULTIPLE_LINES_AFTER_SUGGESTION,
			aiserverv1.CppConfigResponse_HEURISTIC_REVERTING_USER_CHANGE,
			aiserverv1.CppConfigResponse_HEURISTIC_OUTPUT_EXTENDS_BEYOND_RANGE_AND_IS_REPEATED,
			aiserverv1.CppConfigResponse_HEURISTIC_SUGGESTING_RECENTLY_REJECTED_EDIT,
		},
		ExcludeRecentlyViewedFilesPatterns: []string{"**/.git/**", "**/node_modules/**", "**/dist/**", "**/build/**"},
		EnableRvfTracking:                  true,
		GlobalDebounceDurationMillis:       200,
		ClientDebounceDurationMillis:       120,
		CppUrl:                             localCppBackendURL,
		UseWhitespaceDiffHistory:           true,
		ImportPredictionConfig: &aiserverv1.CppConfigResponse_ImportPredictionConfig{
			IsDisabledByBackend:       false,
			ShouldTurnOnAutomatically: true,
			PythonEnabled:             true,
		},
		EnableFilesyncDebounceSkipping: true,
		CheckFilesyncHashPercent:       1,
		GeoCppBackendUrl:               localCppBackendURL,
		RecentlyRejectedEditThresholds: &aiserverv1.CppConfigResponse_RecentlyRejectedEditThresholds{
			HardRejectThreshold: 4,
			SoftRejectThreshold: 2,
		},
		IsFusedCursorPredictionModel:                 false,
		IncludeUnchangedLines:                        true,
		ShouldFetchRvfText:                           true,
		MaxNumberOfClearedSuggestionsSinceLastAccept: &maxCleared,
		SuggestionHintConfig: &aiserverv1.CppConfigResponse_SuggestionHintConfig{
			ImportantLspExtensions:   []string{"ts", "tsx", "js", "jsx", "go", "py", "java", "rs", "vue"},
			EnabledForPathExtensions: []string{"ts", "tsx", "js", "jsx", "go", "py", "java", "rs", "vue", "json", "md"},
		},
		AllowsTabChunks:                         true,
		TabContextRefreshDebounceMs:             &tabDebounce,
		TabContextRefreshEditorChangeDebounceMs: &tabEditorDebounce,
	}
	return marshalLocalProto(resp, "CppConfig")
}

func buildFileSyncEnabledPayload() []byte {
	return marshalLocalProto(&aiserverv1.FSIsEnabledForUserResponse{Enabled: true}, "FSIsEnabledForUser")
}

func buildFileSyncConfigPayload() []byte {
	breakerReset := int32(30000)
	rps := int32(50)
	burst := int32(100)
	maxUpdates := int32(200)
	maxVersionCache := int32(5000)
	maxFileSize := int32(2 * 1024 * 1024)
	retryAttempts := int32(3)
	retryDelay := int32(200)
	retryMultiplier := int32(2)
	statusCache := int32(10000)
	successiveSyncs := int32(1)
	extraSyncs := int32(1)
	bigChangeThreshold := int32(512 * 1024)
	lastUpdates := int32(50)
	statusTTL := int32(5 * 60 * 1000)
	syncDebounce := int32(120)
	updateThreshold := int32(1)

	resp := &aiserverv1.FSConfigResponse{
		CheckFilesyncHashPercent:              1,
		RateLimiterBreakerResetTimeMs:         &breakerReset,
		RateLimiterRps:                        &rps,
		RateLimiterBurstCapacity:              &burst,
		MaxRecentUpdatesStored:                &maxUpdates,
		MaxModelVersionCacheSize:              &maxVersionCache,
		MaxFileSizeToSyncBytes:                &maxFileSize,
		SyncRetryMaxAttempts:                  &retryAttempts,
		SyncRetryInitialDelayMs:               &retryDelay,
		SyncRetryTimeMultiplier:               &retryMultiplier,
		FileSyncStatusMaxCacheSize:            &statusCache,
		SuccessiveSyncsRequiredForReliance:    &successiveSyncs,
		ExtraSuccessfulSyncsNeededAfterErrors: &extraSyncs,
		BigChangeStrippingThresholdBytes:      &bigChangeThreshold,
		LastNUpdatesToSend:                    &lastUpdates,
		FileSyncStatusTtlMs:                   &statusTTL,
		SyncDebounceMs:                        &syncDebounce,
		SyncUpdateThreshold:                   &updateThreshold,
	}
	return marshalLocalProto(resp, "FSConfig")
}

func (g *Gateway) buildFileSyncUploadPayload(req *http.Request) []byte {
	uploadReq := &aiserverv1.FSUploadFileRequest{}
	if err := parseLocalProtoMessage(req, uploadReq); err != nil {
		log.Printf("[Gateway] FSUploadFile decode warning: %v", err)
	}
	if path := strings.TrimSpace(uploadReq.GetRelativeWorkspacePath()); path != "" {
		hash := uploadReq.GetSha256Hash()
		if hash == "" {
			hash = sha256Text(uploadReq.GetContents())
		}
		g.storeFileSyncEntry(uploadReq.GetUuid(), path, uploadReq.GetContents(), hash, uploadReq.GetModelVersion())
		log.Printf("[Gateway] FileSync upload cached path=%q version=%d bytes=%d", path, uploadReq.GetModelVersion(), len(uploadReq.GetContents()))
	}
	return marshalLocalProto(&aiserverv1.FSUploadFileResponse{}, "FSUploadFile")
}

func (g *Gateway) buildFileSyncFilePayload(req *http.Request) []byte {
	syncReq := &aiserverv1.FSSyncFileRequest{}
	if err := parseLocalProtoMessage(req, syncReq); err != nil {
		log.Printf("[Gateway] FSSyncFile decode warning: %v", err)
	}
	path := strings.TrimSpace(syncReq.GetRelativeWorkspacePath())
	if path != "" {
		contents, ok := g.lookupFileSyncContent(syncReq.GetUuid(), path)
		version := syncReq.GetModelVersion()
		if ok {
			for _, updateSet := range syncReq.GetFilesyncUpdates() {
				if updateSet == nil {
					continue
				}
				updatePath := strings.TrimSpace(updateSet.GetRelativeWorkspacePath())
				if updatePath != "" && normalizeFileSyncPath(updatePath) != normalizeFileSyncPath(path) {
					continue
				}
				contents = applyFileSyncUpdates(contents, updateSet.GetUpdates())
				if updateSet.GetModelVersion() > 0 {
					version = updateSet.GetModelVersion()
				}
			}
			hash := syncReq.GetSha256Hash()
			if hash == "" {
				hash = sha256Text(contents)
			}
			g.storeFileSyncEntry(syncReq.GetUuid(), path, contents, hash, version)
			log.Printf("[Gateway] FileSync update cached path=%q version=%d bytes=%d updates=%d", path, version, len(contents), len(syncReq.GetFilesyncUpdates()))
		}
	}
	return marshalLocalProto(&aiserverv1.FSSyncFileResponse{}, "FSSyncFile")
}

func (g *Gateway) buildRefreshTabContextPayload(req *http.Request) []byte {
	refreshReq := &aiserverv1.RefreshTabContextRequest{}
	if err := parseLocalProtoMessage(req, refreshReq); err != nil {
		log.Printf("[Gateway] RefreshTabContext decode warning: %v", err)
	}

	resp := &aiserverv1.RefreshTabContextResponse{}
	if result := g.codeResultFromCurrentFile(refreshReq.GetCurrentFile()); result != nil {
		resp.CodeResults = append(resp.CodeResults, result)
	}
	payload := marshalLocalProto(resp, "RefreshTabContext")
	log.Printf("[Gateway] RefreshTabContext served codeResults=%d", len(resp.GetCodeResults()))
	return payload
}

func (g *Gateway) buildStreamCppPayload(req *http.Request) []byte {
	streamReq := &aiserverv1.StreamCppRequest{}
	if err := parseLocalProtoMessage(req, streamReq); err != nil {
		log.Printf("[Gateway] StreamCpp decode warning: %v", err)
	}
	g.rememberCurrentFile(streamReq.GetCurrentFile())

	done := true
	resp := &aiserverv1.StreamCppResponse{
		DoneStream: &done,
		DoneEdit:   &done,
		ModelInfo:  &aiserverv1.StreamCppResponse_ModelInfo{IsFusedCursorPredictionModel: false, IsMultidiffModel: false},
	}
	return marshalLocalProto(resp, "StreamCpp")
}

func (g *Gateway) codeResultFromCurrentFile(file *aiserverv1.CurrentFileInfo) *aiserverv1.CodeResult {
	if file == nil {
		return nil
	}
	path := strings.TrimSpace(file.GetRelativeWorkspacePath())
	contents := file.GetContents()
	if contents == "" {
		if cached, ok := g.lookupFileSyncContent("", path); ok {
			contents = cached
		}
	}
	if path == "" || contents == "" {
		return nil
	}
	length := int32(len(contents))
	fileContents := contents
	return &aiserverv1.CodeResult{
		CodeBlock: &aiserverv1.CodeBlock{
			RelativeWorkspacePath: path,
			FileContents:          &fileContents,
			FileContentsLength:    &length,
			Contents:              contents,
		},
		Score: 1,
	}
}

func (g *Gateway) rememberCurrentFile(file *aiserverv1.CurrentFileInfo) {
	if file == nil || file.GetContents() == "" || strings.TrimSpace(file.GetRelativeWorkspacePath()) == "" {
		return
	}
	hash := file.GetSha_256Hash()
	if hash == "" {
		hash = sha256Text(file.GetContents())
	}
	g.storeFileSyncEntry("", file.GetRelativeWorkspacePath(), file.GetContents(), hash, file.GetFileVersion())
}

func (g *Gateway) storeFileSyncEntry(uuid string, path string, contents string, hash string, version int32) {
	if g == nil || g.fileSyncCache == nil {
		return
	}
	entry := fileSyncCacheEntry{
		UUID:         uuid,
		Path:         path,
		Contents:     contents,
		Hash:         hash,
		ModelVersion: version,
		UpdatedAt:    time.Now(),
	}
	g.mu.Lock()
	g.fileSyncCache[fileSyncCacheKey(uuid, path)] = entry
	g.fileSyncCache[fileSyncPathCacheKey(path)] = entry
	g.mu.Unlock()
	g.persistFileSyncEntry(entry)

	// Update ContextStore with file sync data
	if g.contextStore != nil {
		// Determine language from file extension
		lang := ""
		if ext := filepath.Ext(path); ext != "" {
			lang = strings.TrimPrefix(ext, ".")
		}
		g.contextStore.UpdateFileSync(path, contents, int(version), lang, false)
	}
}

func (g *Gateway) lookupFileSyncContent(uuid string, path string) (string, bool) {
	if g == nil || g.fileSyncCache == nil || strings.TrimSpace(path) == "" {
		return "", false
	}
	g.mu.RLock()
	if uuid != "" {
		if entry, ok := g.fileSyncCache[fileSyncCacheKey(uuid, path)]; ok {
			g.mu.RUnlock()
			return entry.Contents, true
		}
	}
	if entry, ok := g.fileSyncCache[fileSyncPathCacheKey(path)]; ok {
		g.mu.RUnlock()
		return entry.Contents, true
	}
	g.mu.RUnlock()
	if entry, ok := g.readFileSyncEntryFromDisk(path); ok {
		g.mu.Lock()
		g.fileSyncCache[fileSyncCacheKey(entry.UUID, entry.Path)] = entry
		g.fileSyncCache[fileSyncPathCacheKey(entry.Path)] = entry
		g.mu.Unlock()
		return entry.Contents, true
	}
	return "", false
}

func fileSyncCacheKey(uuid string, path string) string {
	return strings.TrimSpace(uuid) + "\x00" + normalizeFileSyncPath(path)
}

func fileSyncPathCacheKey(path string) string {
	return "\x00" + normalizeFileSyncPath(path)
}

func normalizeFileSyncPath(path string) string {
	path = strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
	return strings.ToLower(path)
}

func applyFileSyncUpdates(contents string, updates []*aiserverv1.SingleUpdateRequest) string {
	for _, update := range updates {
		if update == nil {
			continue
		}
		contents = replaceTextByRuneOffsets(contents, int(update.GetStartPosition()), int(update.GetEndPosition()), update.GetReplacedString())
	}
	return contents
}

func replaceTextByRuneOffsets(text string, start int, end int, replacement string) string {
	runes := []rune(text)
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	if start > len(runes) {
		start = len(runes)
	}
	if end > len(runes) {
		end = len(runes)
	}
	out := make([]rune, 0, len(runes)-end+start+len([]rune(replacement)))
	out = append(out, runes[:start]...)
	out = append(out, []rune(replacement)...)
	out = append(out, runes[end:]...)
	return string(out)
}

func sha256Text(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func marshalLocalProto(msg proto.Message, label string) []byte {
	payload, err := proto.Marshal(msg)
	if err != nil {
		log.Printf("[Gateway] %s encode failed: %v", label, err)
		return nil
	}
	return payload
}
