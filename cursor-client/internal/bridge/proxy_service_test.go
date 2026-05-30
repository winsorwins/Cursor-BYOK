package bridge

import (
	appconfig "cursor-client/internal/config"
	"cursor-client/internal/relay"
	"testing"
)

func TestProxyStatsTracksProviderCacheUsage(t *testing.T) {
	service := &ProxyService{config: appconfig.DefaultUserConfig()}
	service.handleGatewayEvent(relay.Event{
		Type:             relay.EventTokens,
		Model:            "gpt-test",
		PromptTokens:     100,
		CompletionTokens: 25,
		CacheReadTokens:  40,
		CacheWriteTokens: 7,
	})

	if service.stats.CacheReadTokens != 40 || service.stats.CacheWriteTokens != 7 {
		t.Fatalf("cache tokens = %d/%d, want 40/7", service.stats.CacheReadTokens, service.stats.CacheWriteTokens)
	}
	if service.stats.CacheHits != 1 || service.stats.CacheMisses != 0 {
		t.Fatalf("cache hit/miss = %d/%d, want 1/0", service.stats.CacheHits, service.stats.CacheMisses)
	}
}

func TestProxyStatsTracksCacheMissWhenProviderReportsNoRead(t *testing.T) {
	service := &ProxyService{config: appconfig.DefaultUserConfig()}
	service.handleGatewayEvent(relay.Event{
		Type:         relay.EventTokens,
		Model:        "gpt-test",
		PromptTokens: 100,
	})

	if service.stats.CacheHits != 0 || service.stats.CacheMisses != 1 {
		t.Fatalf("cache hit/miss = %d/%d, want 0/1", service.stats.CacheHits, service.stats.CacheMisses)
	}
}

func TestFixIssuesMutualExclusion(t *testing.T) {
	service := &ProxyService{config: appconfig.DefaultUserConfig()}

	_, err := service.FixIssues(FixOptions{
		FixCursorProxy:  true,
		RestoreOfficial: true,
	})

	if err == nil || err.Error() != "fixCursorProxy and restoreOfficial are mutually exclusive" {
		t.Fatalf("expected mutual exclusion error, got: %v", err)
	}
}

func TestFixIssuesProxyNotRunning(t *testing.T) {
	service := &ProxyService{config: appconfig.DefaultUserConfig()}

	result, err := service.FixIssues(FixOptions{
		FixCursorProxy: true,
	})

	if err != nil {
		t.Fatalf("FixIssues failed: %v", err)
	}

	// When proxy is not running, fixCursorProxy returns skipped, not failed
	// So success should be true
	if !result.Success {
		t.Fatal("expected success=true when proxy not running (skipped, not failed)")
	}

	found := false
	for _, issue := range result.FixedIssues {
		if issue.Issue == "cursor_proxy" && issue.Status == "skipped" {
			found = true
			break
		}
	}

	if !found {
		t.Fatal("expected cursor_proxy to be skipped when proxy not running")
	}
}

func TestFixIssuesFailedIssuesSetsSuccessFalse(t *testing.T) {
	service := &ProxyService{config: appconfig.DefaultUserConfig()}

	// Test with restoreOfficial when cursor is not initialized
	// This will cause a failure
	result, err := service.FixIssues(FixOptions{
		RestoreOfficial: true,
	})

	if err != nil {
		t.Fatalf("FixIssues failed: %v", err)
	}

	// Should have failed issues
	if len(result.FailedIssues) == 0 {
		t.Fatal("expected failed issues when cursor is not initialized")
	}

	// Success should be false when there are failed issues
	if result.Success {
		t.Fatal("expected success=false when there are failed issues")
	}
}

func TestDetermineCATrustFixStatus(t *testing.T) {
	tests := []struct {
		name            string
		beforeTrusted   bool
		afterTrusted    bool
		afterInstalled  bool
		wantStatus      string
		wantFailed      bool
		wantErrContains string
	}{
		{
			name:          "already trusted before fix",
			beforeTrusted: true,
			afterTrusted:  true,
			wantStatus:    "already_ok",
			wantFailed:    false,
		},
		{
			name:          "fixed - trusted after",
			beforeTrusted: false,
			afterTrusted:  true,
			wantStatus:    "fixed",
			wantFailed:    false,
		},
		{
			name:           "fixed - installed after",
			beforeTrusted:  false,
			afterInstalled: true,
			wantStatus:     "fixed",
			wantFailed:     false,
		},
		{
			name:            "failed - no change",
			beforeTrusted:   false,
			afterTrusted:    false,
			afterInstalled:  false,
			wantFailed:      true,
			wantErrContains: "unchanged",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, failed, errMsg := determineCATrustFixStatus(
				tt.beforeTrusted,
				tt.afterTrusted,
				tt.afterInstalled,
			)

			if status != tt.wantStatus {
				t.Errorf("status = %q, want %q", status, tt.wantStatus)
			}
			if failed != tt.wantFailed {
				t.Errorf("failed = %v, want %v", failed, tt.wantFailed)
			}
			if tt.wantErrContains != "" && !contains(errMsg, tt.wantErrContains) {
				t.Errorf("errMsg = %q, want to contain %q", errMsg, tt.wantErrContains)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) >= len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
