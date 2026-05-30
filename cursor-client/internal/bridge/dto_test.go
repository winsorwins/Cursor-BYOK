package bridge

import (
	"encoding/json"
	"testing"
	"time"
)

func TestRuntimeStatsTimeSerializationToString(t *testing.T) {
	stats := RuntimeStats{
		TotalRequests: 100,
		LastRequest:   time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC).Format(time.RFC3339),
		LastModel:     "claude-3-opus",
	}

	data, err := json.Marshal(stats)
	if err != nil {
		t.Fatalf("Failed to marshal RuntimeStats: %v", err)
	}

	var decoded RuntimeStats
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal RuntimeStats: %v", err)
	}

	if decoded.LastRequest != stats.LastRequest {
		t.Errorf("LastRequest mismatch: got %q, want %q", decoded.LastRequest, stats.LastRequest)
	}

	if decoded.TotalRequests != stats.TotalRequests {
		t.Errorf("TotalRequests mismatch: got %d, want %d", decoded.TotalRequests, stats.TotalRequests)
	}
}

func TestRequestLogEntryTimeSerializationToString(t *testing.T) {
	entry := RequestLogEntry{
		ID:         123,
		Time:       time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC).Format(time.RFC3339),
		Method:     "POST",
		Host:       "api2.cursor.sh",
		Path:       "/aiserver.v1.AiService/StreamChat",
		StatusCode: 200,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Failed to marshal RequestLogEntry: %v", err)
	}

	var decoded RequestLogEntry
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal RequestLogEntry: %v", err)
	}

	if decoded.Time != entry.Time {
		t.Errorf("Time mismatch: got %q, want %q", decoded.Time, entry.Time)
	}

	if decoded.Method != entry.Method {
		t.Errorf("Method mismatch: got %q, want %q", decoded.Method, entry.Method)
	}
}

func TestProxyStateTimeSerializationToString(t *testing.T) {
	state := ProxyState{
		Running:     true,
		Address:     "127.0.0.1:18080",
		LastRequest: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC).Format(time.RFC3339),
	}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("Failed to marshal ProxyState: %v", err)
	}

	var decoded ProxyState
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal ProxyState: %v", err)
	}

	if decoded.LastRequest != state.LastRequest {
		t.Errorf("LastRequest mismatch: got %q, want %q", decoded.LastRequest, state.LastRequest)
	}

	if decoded.Running != state.Running {
		t.Errorf("Running mismatch: got %v, want %v", decoded.Running, state.Running)
	}
}

func TestDiagnosticsDTOSerialization(t *testing.T) {
	diag := DiagnosticsDTO{
		ProxyRunning:     true,
		ProxyAddress:     "127.0.0.1:18080",
		ProxyStartedAt:   time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC).Format(time.RFC3339),
		CAInstalled:      true,
		CACertPath:       "/path/to/ca.crt",
		CAExpiresAt:      time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC).Format(time.RFC3339),
		CursorProxySet:   true,
		CursorConfigPath: "/path/to/cursor/settings.json",
		DataDir:          "/path/to/data",
		LogDir:           "/path/to/logs",
		LastRequestAt:    time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC).Format(time.RFC3339),
		LastErrorAt:      "",
		LastErrorMessage: "",
		TotalRequests:    100,
		TotalErrors:      5,
	}

	data, err := json.Marshal(diag)
	if err != nil {
		t.Fatalf("Failed to marshal DiagnosticsDTO: %v", err)
	}

	var decoded DiagnosticsDTO
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal DiagnosticsDTO: %v", err)
	}

	if decoded.ProxyRunning != diag.ProxyRunning {
		t.Errorf("ProxyRunning mismatch: got %v, want %v", decoded.ProxyRunning, diag.ProxyRunning)
	}

	if decoded.ProxyStartedAt != diag.ProxyStartedAt {
		t.Errorf("ProxyStartedAt mismatch: got %q, want %q", decoded.ProxyStartedAt, diag.ProxyStartedAt)
	}

	if decoded.CAExpiresAt != diag.CAExpiresAt {
		t.Errorf("CAExpiresAt mismatch: got %q, want %q", decoded.CAExpiresAt, diag.CAExpiresAt)
	}

	if decoded.LastRequestAt != diag.LastRequestAt {
		t.Errorf("LastRequestAt mismatch: got %q, want %q", decoded.LastRequestAt, diag.LastRequestAt)
	}

	if decoded.TotalRequests != diag.TotalRequests {
		t.Errorf("TotalRequests mismatch: got %d, want %d", decoded.TotalRequests, diag.TotalRequests)
	}
}

func TestEmptyTimeFieldsSerialization(t *testing.T) {
	// Test that empty time fields serialize as empty strings
	diag := DiagnosticsDTO{
		ProxyRunning:   false,
		ProxyStartedAt: "",
		CAExpiresAt:    "",
		LastRequestAt:  "",
		LastErrorAt:    "",
	}

	data, err := json.Marshal(diag)
	if err != nil {
		t.Fatalf("Failed to marshal DiagnosticsDTO: %v", err)
	}

	var decoded DiagnosticsDTO
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal DiagnosticsDTO: %v", err)
	}

	if decoded.ProxyStartedAt != "" {
		t.Errorf("ProxyStartedAt should be empty, got %q", decoded.ProxyStartedAt)
	}

	if decoded.CAExpiresAt != "" {
		t.Errorf("CAExpiresAt should be empty, got %q", decoded.CAExpiresAt)
	}

	if decoded.LastRequestAt != "" {
		t.Errorf("LastRequestAt should be empty, got %q", decoded.LastRequestAt)
	}
}
