package relay

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"

	agentv1 "github.com/burpheart/cursor-tap/cursor_proto/gen/agent/v1"
	"google.golang.org/protobuf/proto"
)

const (
	agentStoreDirName       = "agent"
	agentStoreBlobDirName   = "blobs"
	agentStoreFileDirName   = "files"
	agentStoreStateDirName  = "states"
	agentStoreFileMode      = 0600
	agentStoreDirMode       = 0700
	agentStoreMaxBlobBytes  = 8 * 1024 * 1024
	agentStoreMaxFileBytes  = 4 * 1024 * 1024
	agentStoreMaxStateBytes = 2 * 1024 * 1024
)

func (g *Gateway) persistAgentKVBlob(blob agentKVBlob) {
	if g == nil || strings.TrimSpace(g.agentStateDir) == "" || len(blob.ID) == 0 || len(blob.Data) == 0 {
		return
	}
	if len(blob.Data) > agentStoreMaxBlobBytes {
		log.Printf("[Gateway] Agent KV blob skipped disk cache label=%s blob=%s bytes=%d", blob.Label, shortBlobID(blob.ID), len(blob.Data))
		return
	}
	path := g.agentKVBlobPath(blob.ID)
	if path == "" {
		return
	}
	if err := writeAgentStoreFile(path, blob.Data); err != nil {
		log.Printf("[Gateway] Agent KV blob disk cache failed label=%s blob=%s error=%v", blob.Label, shortBlobID(blob.ID), err)
	}
}

func (g *Gateway) readAgentKVBlobFromDisk(blobID []byte) ([]byte, bool) {
	if g == nil || strings.TrimSpace(g.agentStateDir) == "" || len(blobID) == 0 {
		return nil, false
	}
	path := g.agentKVBlobPath(blobID)
	if path == "" {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 || len(data) > agentStoreMaxBlobBytes {
		return nil, false
	}
	return data, true
}

func (g *Gateway) agentKVBlobPath(blobID []byte) string {
	key := agentKVBlobKey(blobID)
	if key == "" || len(key) < 2 {
		return ""
	}
	return filepath.Join(g.agentStoreRoot(), agentStoreBlobDirName, key[:2], key+".bin")
}

func (g *Gateway) storeAgentConversationState(requestID string, state *agentv1.ConversationStateStructure) {
	if g == nil || strings.TrimSpace(requestID) == "" || state == nil {
		return
	}
	if cloned, ok := proto.Clone(state).(*agentv1.ConversationStateStructure); ok {
		g.mu.Lock()
		if g.agentSessions == nil {
			g.agentSessions = make(map[string]*agentSessionState)
		}
		session := g.agentSessions[requestID]
		if session == nil {
			session = &agentSessionState{RequestID: requestID, Ready: make(chan struct{})}
			g.agentSessions[requestID] = session
		}
		session.Conversation = cloned
		session.PriorUsedTokens = int(cloned.GetTokenDetails().GetUsedTokens())
		if mode := cursorAgentModeFromProto(cloned.GetMode()); mode != "" {
			session.AgentMode = mode
		}
		g.mu.Unlock()
	}
	if strings.TrimSpace(g.agentStateDir) == "" {
		return
	}
	data, err := proto.Marshal(state)
	if err != nil || len(data) == 0 || len(data) > agentStoreMaxStateBytes {
		if err != nil {
			log.Printf("[Gateway] Agent conversation state marshal failed request=%s error=%v", requestID, err)
		}
		return
	}
	if err := writeAgentStoreFile(g.agentConversationStatePath(requestID), data); err != nil {
		log.Printf("[Gateway] Agent conversation state disk cache failed request=%s error=%v", requestID, err)
	}
}

func (g *Gateway) readAgentConversationState(requestID string) *agentv1.ConversationStateStructure {
	if g == nil || strings.TrimSpace(g.agentStateDir) == "" || strings.TrimSpace(requestID) == "" {
		return nil
	}
	data, err := os.ReadFile(g.agentConversationStatePath(requestID))
	if err != nil || len(data) == 0 || len(data) > agentStoreMaxStateBytes {
		return nil
	}
	state := &agentv1.ConversationStateStructure{}
	if err := proto.Unmarshal(data, state); err != nil {
		log.Printf("[Gateway] Agent conversation state disk decode failed request=%s error=%v", requestID, err)
		return nil
	}
	return state
}

func (g *Gateway) persistFileSyncEntry(entry fileSyncCacheEntry) {
	if g == nil || strings.TrimSpace(g.agentStateDir) == "" || strings.TrimSpace(entry.Path) == "" || entry.Contents == "" {
		return
	}
	if len(entry.Contents) > agentStoreMaxFileBytes {
		log.Printf("[Gateway] FileSync disk cache skipped path=%q bytes=%d", entry.Path, len(entry.Contents))
		return
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	if err := writeAgentStoreFile(g.fileSyncEntryPath(entry.Path), data); err != nil {
		log.Printf("[Gateway] FileSync disk cache failed path=%q error=%v", entry.Path, err)
	}
}

func (g *Gateway) readFileSyncEntryFromDisk(path string) (fileSyncCacheEntry, bool) {
	if g == nil || strings.TrimSpace(g.agentStateDir) == "" || strings.TrimSpace(path) == "" {
		return fileSyncCacheEntry{}, false
	}
	data, err := os.ReadFile(g.fileSyncEntryPath(path))
	if err != nil || len(data) == 0 || len(data) > agentStoreMaxFileBytes {
		return fileSyncCacheEntry{}, false
	}
	var entry fileSyncCacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		log.Printf("[Gateway] FileSync disk cache decode failed path=%q error=%v", path, err)
		return fileSyncCacheEntry{}, false
	}
	if strings.TrimSpace(entry.Path) == "" || entry.Contents == "" {
		return fileSyncCacheEntry{}, false
	}
	return entry, true
}

func (g *Gateway) agentConversationStatePath(requestID string) string {
	return filepath.Join(g.agentStoreRoot(), agentStoreStateDirName, agentStoreKey(requestID)+".pb")
}

func (g *Gateway) fileSyncEntryPath(path string) string {
	key := agentStoreKey(normalizeFileSyncPath(path))
	return filepath.Join(g.agentStoreRoot(), agentStoreFileDirName, key[:2], key+".json")
}

func (g *Gateway) agentStoreRoot() string {
	return filepath.Join(g.agentStateDir, agentStoreDirName)
}

func agentStoreKey(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])
}

func writeAgentStoreFile(path string, data []byte) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), agentStoreDirMode); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, agentStoreFileMode); err != nil {
		return err
	}
	_ = os.Remove(path)
	return os.Rename(tmp, path)
}
