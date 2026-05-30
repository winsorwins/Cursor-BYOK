package relay

import (
	"bytes"
	localstatsig "cursor-client/internal/statsig"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strings"
	"time"
)

const (
	aiGetDefaultModelPath          = "/aiserver.v1.AiService/GetDefaultModel"
	aiGetDefaultModelNudgeDataPath = "/aiserver.v1.AiService/GetDefaultModelNudgeData"
	aiAvailableDocsPath            = "/aiserver.v1.AiService/AvailableDocs"
	aiCppConfigPath                = "/aiserver.v1.AiService/CppConfig"
	aiCppEditHistoryStatusPath     = "/aiserver.v1.AiService/CppEditHistoryStatus"
	aiKnowledgeBaseListPath        = "/aiserver.v1.AiService/KnowledgeBaseList"
	aiNameTabPath                  = "/aiserver.v1.AiService/NameTab"
	aiServerTimePath               = "/aiserver.v1.AiService/ServerTime"
	aiUpdateVscodeProfilePath      = "/aiserver.v1.AiService/UpdateVscodeProfile"
	aiGetUsableModelsPath          = "/aiserver.v1.AiService/GetUsableModels"
	aiGetDefaultModelForCliPath    = "/aiserver.v1.AiService/GetDefaultModelForCli"
	aiCountTokensPath              = "/aiserver.v1.AiService/CountTokens"
	aiStreamCppPath                = "/aiserver.v1.AiService/StreamCpp"
	aiRefreshTabContextPath        = "/aiserver.v1.AiService/RefreshTabContext"
	analyticsBatchPath             = "/aiserver.v1.AnalyticsService/Batch"
	analyticsBootstrapStatsigPath  = "/aiserver.v1.AnalyticsService/BootstrapStatsig"
	analyticsSubmitLogsPath        = "/aiserver.v1.AnalyticsService/SubmitLogs"
	chatGetPromptDryRunPath        = "/aiserver.v1.ChatService/GetPromptDryRun"
	agentGetUsableModelsPath       = "/agent.v1.AgentService/GetUsableModels"
	agentDefaultModelForCliPath    = "/agent.v1.AgentService/GetDefaultModelForCli"
	agentAllowedModelIntentsPath   = "/agent.v1.AgentService/GetAllowedModelIntents"
	dashboardGetMePath             = "/aiserver.v1.DashboardService/GetMe"
	dashboardGetTeamsPath          = "/aiserver.v1.DashboardService/GetTeams"
	dashboardGetTeamReposPath      = "/aiserver.v1.DashboardService/GetTeamRepos"
	dashboardGetTeamReposEmptyPath = "/aiserver.v1.DashboardService/GetTeamReposOrEmptyIfNotInTeam"
	dashboardGetUserPrivacyPath    = "/aiserver.v1.DashboardService/GetUserPrivacyMode"
	dashboardNeedsPrivacyPath      = "/aiserver.v1.DashboardService/NeedsPrivacyModeMigration"
	dashboardUsageLimitPolicyPath  = "/aiserver.v1.DashboardService/GetUsageLimitPolicyStatus"
	dashboardIsOnNewPricingPath    = "/aiserver.v1.DashboardService/IsOnNewPricing"
	dashboardGetPlanInfoPath       = "/aiserver.v1.DashboardService/GetPlanInfo"
	dashboardGetCurrentUsagePath   = "/aiserver.v1.DashboardService/GetCurrentPeriodUsage"
	dashboardUsageStatusGrantsPath = "/aiserver.v1.DashboardService/GetUsageLimitStatusAndActiveGrants"
	dashboardTeamPrivacyForcedPath = "/aiserver.v1.DashboardService/GetTeamPrivacyModeForced"
	dashboardTeamAdminSettingsPath = "/aiserver.v1.DashboardService/GetTeamAdminSettings"
	dashboardTeamAdminEmptyPath    = "/aiserver.v1.DashboardService/GetTeamAdminSettingsOrEmptyIfNotInTeam"
	dashboardEffectivePluginsPath  = "/aiserver.v1.DashboardService/GetEffectiveUserPlugins"
	dashboardManagedSkillsPath     = "/aiserver.v1.DashboardService/GetManagedSkills"
	dashboardTeamRulesPath         = "/aiserver.v1.DashboardService/GetTeamRules"
	dashboardTeamCommandsPath      = "/aiserver.v1.DashboardService/GetTeamCommands"
	dashboardTeamHooksPath         = "/aiserver.v1.DashboardService/GetTeamHooks"
	dashboardGlobalCommandsPath    = "/aiserver.v1.DashboardService/GetGlobalCommands"
	dashboardMarketplacePath       = "/aiserver.v1.DashboardService/ListMarketplacePlugins"
	dashboardListMarketplacesPath  = "/aiserver.v1.DashboardService/ListMarketplaces"
	fileSyncEnabledPath            = "/aiserver.v1.FileSyncService/FSIsEnabledForUser"
	fileSyncConfigPath             = "/aiserver.v1.FileSyncService/FSConfig"
	fileSyncFilePath               = "/aiserver.v1.FileSyncService/FSSyncFile"
	fileSyncUploadFilePath         = "/aiserver.v1.FileSyncService/FSUploadFile"
	fileSyncInternalFilePath       = "/aiserver.v1.FileSyncService/FSInternalSyncFile"
	fileSyncInternalUploadPath     = "/aiserver.v1.FileSyncService/FSInternalUploadFile"
	inAppHasSeenAdPath             = "/aiserver.v1.InAppAdService/HasSeenAd"
	mcpKnownServersPath            = "/aiserver.v1.MCPRegistryService/GetKnownServers"
	networkIsConnectedPath         = "/aiserver.v1.NetworkService/IsConnected"
	repositoryHandshakePath        = "/aiserver.v1.RepositoryService/FastRepoInitHandshakeV2"
	serverConfigPath               = "/aiserver.v1.ServerConfigService/GetServerConfig"
	authStripeProfilePath          = "/auth/stripe_profile"
	authFullStripeProfilePath      = "/auth/full_stripe_profile"
	authHasPaymentMethodPath       = "/auth/has_valid_payment_method"
)

func (g *Gateway) tryHandleLocalRPC(req *http.Request) (*http.Response, bool) {
	if req == nil || req.URL == nil || !isCursorRequest(req) {
		return nil, false
	}
	path := req.URL.Path
	if resp, ok := g.tryHandleLocalHTTP(req); ok {
		return resp, true
	}

	if kind, ok := availableModelsKindForPath(path); ok {
		models := g.byokModels()
		if len(models) == 0 {
			return nil, false
		}
		payload := buildAvailableModelsFallbackPayload(kind, models)
		return g.localProtoResponse(req, payload, "local/available_models"), true
	}

	var payload []byte
	route := "local/mock"
	switch path {
	case aiGetDefaultModelPath:
		payload = g.buildGetDefaultModelPayload()
		route = "local/default_model"
	case aiGetDefaultModelNudgeDataPath:
		payload = buildGetDefaultModelNudgeDataPayload(g.byokModels())
		route = "local/default_model_nudge"
	case aiAvailableDocsPath:
		payload = nil
		route = "local/available_docs"
	case aiCppConfigPath:
		payload = buildCppConfigPayload()
		route = "local/cpp_config"
	case aiCppEditHistoryStatusPath:
		payload = nil
		route = "local/cpp_edit_history"
	case aiKnowledgeBaseListPath:
		payload = appendVarintField(nil, 1, 1)
		route = "local/knowledge_base"
	case aiNameTabPath:
		payload = buildNameTabPayload()
		route = "local/name_tab"
	case aiServerTimePath:
		payload = buildServerTimePayload()
		route = "local/server_time"
	case aiUpdateVscodeProfilePath:
		payload = nil
		route = "local/update_vscode_profile"
	case aiGetUsableModelsPath:
		payload = buildAIUsableModelsPayload(g.byokModels())
		route = "local/ai_usable_models"
	case aiGetDefaultModelForCliPath:
		payload = buildAIDefaultModelForCliPayload(g.byokModels())
		route = "local/ai_default_model_cli"
	case aiCountTokensPath:
		payload = g.buildCountTokensPayload(req)
		route = "local/count_tokens"
	case aiStreamCppPath:
		payload = g.buildStreamCppPayload(req)
		route = "local/stream_cpp"
	case aiRefreshTabContextPath:
		payload = g.buildRefreshTabContextPayload(req)
		route = "local/refresh_tab_context"
	case analyticsBatchPath:
		payload = nil
		route = "local/analytics_batch"
	case analyticsBootstrapStatsigPath:
		payload = buildBootstrapStatsigPayload()
		route = "local/bootstrap_statsig"
	case analyticsSubmitLogsPath:
		payload = buildSubmitLogsPayload()
		route = "local/submit_logs"
	case chatGetPromptDryRunPath:
		payload = g.buildPromptDryRunPayload(req)
		route = "local/prompt_dry_run"
	case agentGetUsableModelsPath:
		payload = buildAgentUsableModelsPayload(g.byokModels())
		route = "local/agent_usable_models"
	case agentDefaultModelForCliPath:
		payload = buildAgentDefaultModelForCliPayload(g.byokModels())
		route = "local/agent_default_model_cli"
	case agentAllowedModelIntentsPath:
		payload = buildAllowedModelIntentsPayload()
		route = "local/agent_model_intents"
	case dashboardGetMePath:
		payload = buildGetMePayload()
		route = "local/get_me"
	case dashboardGetTeamsPath:
		payload = buildGetTeamsPayload()
		route = "local/get_teams"
	case dashboardGetTeamReposPath, dashboardGetTeamReposEmptyPath:
		payload = nil
		route = "local/team_repos"
	case dashboardGetUserPrivacyPath:
		payload = buildGetUserPrivacyModePayload()
		route = "local/privacy_mode"
	case dashboardNeedsPrivacyPath:
		payload = nil
		route = "local/privacy_migration"
	case dashboardUsageLimitPolicyPath:
		payload = buildUsageLimitPolicyPayload(g.byokModels())
		route = "local/usage_policy"
	case dashboardIsOnNewPricingPath:
		payload = buildIsOnNewPricingPayload()
		route = "local/pricing"
	case dashboardGetPlanInfoPath:
		payload = buildPlanInfoPayload()
		route = "local/plan_info"
	case dashboardGetCurrentUsagePath:
		payload = buildCurrentPeriodUsagePayload()
		route = "local/current_usage"
	case dashboardUsageStatusGrantsPath:
		payload = buildUsageLimitStatusAndActiveGrantsPayload()
		route = "local/usage_grants"
	case dashboardTeamPrivacyForcedPath:
		payload = buildTeamPrivacyModeForcedPayload()
		route = "local/team_privacy"
	case dashboardTeamAdminSettingsPath, dashboardTeamAdminEmptyPath:
		payload = buildTeamAdminSettingsPayload(g.byokModels())
		route = "local/team_admin"
	case dashboardEffectivePluginsPath:
		payload = nil
		route = "local/effective_plugins"
	case dashboardManagedSkillsPath:
		payload = nil
		route = "local/managed_skills"
	case dashboardTeamRulesPath:
		payload = nil
		route = "local/team_rules"
	case dashboardTeamCommandsPath:
		payload = nil
		route = "local/team_commands"
	case dashboardTeamHooksPath:
		payload = nil
		route = "local/team_hooks"
	case dashboardGlobalCommandsPath:
		payload = nil
		route = "local/global_commands"
	case dashboardMarketplacePath:
		payload = nil
		route = "local/marketplace_plugins"
	case dashboardListMarketplacesPath:
		payload = nil
		route = "local/marketplaces"
	case fileSyncEnabledPath:
		payload = buildFileSyncEnabledPayload()
		route = "local/filesync_enabled"
	case fileSyncConfigPath:
		payload = buildFileSyncConfigPayload()
		route = "local/filesync_config"
	case fileSyncUploadFilePath, fileSyncInternalUploadPath:
		payload = g.buildFileSyncUploadPayload(req)
		route = "local/filesync_upload"
	case fileSyncFilePath, fileSyncInternalFilePath:
		payload = g.buildFileSyncFilePayload(req)
		route = "local/filesync_file"
	case inAppHasSeenAdPath:
		payload = appendVarintField(nil, 1, 1)
		route = "local/has_seen_ad"
	case mcpKnownServersPath:
		payload = nil
		route = "local/mcp_servers"
	case networkIsConnectedPath:
		payload = nil
		route = "local/is_connected"
	case repositoryHandshakePath:
		payload = appendVarintField(nil, 1, 2) // STATUS_SUCCESS
		route = "local/repo_handshake"
	case serverConfigPath:
		payload = buildServerConfigPayload()
		route = "local/server_config"
	default:
		return nil, false
	}

	return g.localProtoResponse(req, payload, route), true
}

func (g *Gateway) tryHandleLocalHTTP(req *http.Request) (*http.Response, bool) {
	if req == nil || req.URL == nil || req.Method == http.MethodConnect {
		return nil, false
	}
	switch req.URL.Path {
	case authStripeProfilePath, authFullStripeProfilePath:
		if req.Method == http.MethodOptions {
			return g.localJSONResponse(req, http.StatusNoContent, nil, "local/auth_options"), true
		}
		return g.localJSONResponse(req, http.StatusOK, localStripeProfile(), "local/stripe_profile"), true
	case authHasPaymentMethodPath:
		if req.Method == http.MethodOptions {
			return g.localJSONResponse(req, http.StatusNoContent, nil, "local/auth_options"), true
		}
		return g.localJSONResponse(req, http.StatusOK, map[string]any{"hasValidPaymentMethod": true}, "local/payment_method"), true
	default:
		return nil, false
	}
}

func (g *Gateway) localJSONResponse(req *http.Request, status int, value any, route string) *http.Response {
	body := []byte{}
	if value != nil {
		data, err := json.Marshal(value)
		if err != nil {
			status = http.StatusInternalServerError
			body = []byte(`{"error":"failed to encode local response"}`)
		} else {
			body = data
		}
	}
	resp := &http.Response{
		StatusCode:    status,
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:        http.Header{},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
	resp.Header.Set("Access-Control-Allow-Origin", "*")
	resp.Header.Set("Access-Control-Allow-Headers", "authorization,content-type,x-cursor-client-version,x-cursor-privacy-mode,x-cursor-snippet-learning")
	resp.Header.Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
	resp.Header.Set("X-Cursor-Assistant-Local", "1")
	if value != nil {
		resp.Header.Set("Content-Type", "application/json; charset=utf-8")
		resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	}
	g.completeHTTPRequest(req, status, route, true, false, "", "")
	return resp
}

func (g *Gateway) localProtoResponse(req *http.Request, payload []byte, route string) *http.Response {
	body, contentType := encodeLocalProtoHTTPBody(payload, req.Header.Get("Content-Type"))
	status := http.StatusOK
	resp := &http.Response{
		StatusCode:    status,
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:        http.Header{},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
	resp.Header.Set("Content-Type", contentType)
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	resp.Header.Set("Cache-Control", "no-cache")
	resp.Header.Set("X-Cursor-Assistant-Local", "1")
	if contentType == "application/grpc" {
		resp.Header.Set("Grpc-Status", "0")
	}
	g.completeHTTPRequest(req, status, route, true, false, "", "")
	log.Printf("[Gateway] Served local Cursor RPC %s", req.URL.Path)
	return resp
}

func (g *Gateway) buildGetDefaultModelPayload() []byte {
	models := g.byokModels()
	if len(models) == 0 {
		return nil
	}
	name := models[0].CursorModelName()
	payload := appendStringField(nil, 1, name)
	payload = appendStringField(payload, 2, name)
	payload = appendVarintField(payload, 3, 0)
	return payload
}

func buildGetDefaultModelNudgeDataPayload(adapters []*ModelAdapter) []byte {
	payload := appendStringField(nil, 1, "2099-01-01")
	payload = appendVarintField(payload, 2, 0)
	for _, adapter := range adapters {
		if adapter == nil {
			continue
		}
		payload = appendStringField(payload, 3, adapter.CursorModelName())
	}
	return payload
}

func buildGetMePayload() []byte {
	payload := appendStringField(nil, 1, "cursor-local-assistant")
	payload = appendVarintField(payload, 2, 1)
	payload = appendStringField(payload, 3, "cursor@ai.com")
	payload = appendStringField(payload, 4, "Cursor")
	payload = appendStringField(payload, 5, "Local")
	payload = appendStringField(payload, 8, "2026-01-01T00:00:00Z")
	payload = appendVarintField(payload, 9, 0)
	payload = appendStringField(payload, 11, "personal")
	return payload
}

func buildGetTeamsPayload() []byte {
	team := appendStringField(nil, 1, "Cursor Local")
	team = appendVarintField(team, 2, 1)
	team = appendVarintField(team, 3, 1) // TEAM_ROLE_OWNER
	team = appendVarintField(team, 4, 1)
	team = appendVarintField(team, 5, 1)
	team = appendVarintField(team, 6, 999999)
	team = appendVarintField(team, 7, 0)
	team = appendStringField(team, 10, "active")
	team = appendVarintField(team, 12, 1)
	team = appendVarintField(team, 13, 0)
	team = appendStringField(team, 16, "ultra")
	return appendBytesField(nil, 1, team)
}

func buildGetUserPrivacyModePayload() []byte {
	payload := appendVarintField(nil, 1, 1) // PRIVACY_MODE_NO_STORAGE
	payload = appendVarintField(payload, 2, 0)
	payload = appendVarintField(payload, 3, 0)
	payload = appendVarintField(payload, 4, 0)
	payload = appendVarintField(payload, 5, 0)
	payload = appendVarintField(payload, 6, 1)
	return payload
}

func buildUsageLimitPolicyPayload(adapters []*ModelAdapter) []byte {
	payload := appendVarintField(nil, 1, 0)
	payload = appendVarintField(payload, 6, 1)
	return payload
}

func buildCppConfigPayload() []byte {
	return buildLocalCppConfigPayload()
}

func buildBootstrapStatsigPayload() []byte {
	now := time.Now().UnixMilli()
	payload := appendStringField(nil, 1, localstatsig.BuildBootstrapConfig("", now))
	payload = appendVarintField(payload, 2, uint64(now))
	return payload
}

func buildSubmitLogsPayload() []byte {
	payload := appendVarintField(nil, 1, 1)
	payload = appendVarintField(payload, 3, 0)
	payload = appendVarintField(payload, 4, 0)
	return payload
}

func buildPlanInfoPayload() []byte {
	plan := appendStringField(nil, 1, "Local BYOK")
	plan = appendVarintField(plan, 2, 999999)
	plan = appendStringField(plan, 3, "$0")
	plan = appendVarintField(plan, 4, uint64(time.Now().AddDate(1, 0, 0).Unix()))
	return appendBytesField(nil, 1, plan)
}

func buildCurrentPeriodUsagePayload() []byte {
	planUsage := appendVarintField(nil, 1, 0)
	planUsage = appendVarintField(planUsage, 2, 999999)
	planUsage = appendVarintField(planUsage, 4, 999999)
	planUsage = appendVarintField(planUsage, 5, 999999)
	payload := appendVarintField(nil, 1, uint64(time.Now().AddDate(0, 0, -1).Unix()))
	payload = appendVarintField(payload, 2, uint64(time.Now().AddDate(1, 0, 0).Unix()))
	payload = appendBytesField(payload, 3, planUsage)
	payload = appendVarintField(payload, 5, 95)
	payload = appendVarintField(payload, 6, 1)
	payload = appendStringField(payload, 7, "Local BYOK usage is unlimited")
	return payload
}

func buildUsageLimitStatusAndActiveGrantsPayload() []byte {
	payload := appendVarintField(nil, 1, 0)
	payload = appendVarintField(payload, 2, 0)
	payload = appendVarintField(payload, 3, 0)
	return payload
}

func buildTeamPrivacyModeForcedPayload() []byte {
	payload := appendVarintField(nil, 1, 0)
	payload = appendVarintField(payload, 2, 1) // PRIVACY_MODE_NO_STORAGE
	payload = appendVarintField(payload, 3, 0)
	return payload
}

func buildTeamAdminSettingsPayload(adapters []*ModelAdapter) []byte {
	payload := appendVarintField(nil, 19, 0) // byok_disabled=false
	return payload
}

func buildAIUsableModelsPayload(adapters []*ModelAdapter) []byte {
	payload := []byte{}
	for _, adapter := range adapters {
		if adapter == nil {
			continue
		}
		payload = appendBytesField(payload, 1, encodeAIModelDetails(adapter, false))
		if adapter.SupportsThinking {
			payload = appendBytesField(payload, 1, encodeAIModelDetails(adapter, true))
		}
	}
	return payload
}

func buildAIDefaultModelForCliPayload(adapters []*ModelAdapter) []byte {
	if len(adapters) == 0 || adapters[0] == nil {
		return nil
	}
	return appendBytesField(nil, 1, encodeAIModelDetails(adapters[0], false))
}

func encodeAIModelDetails(adapter *ModelAdapter, maxMode bool) []byte {
	name := adapter.CursorModelName()
	if maxMode {
		name += "-max"
	}
	msg := appendStringField(nil, 1, name)
	msg = appendVarintField(msg, 3, 0)
	msg = appendVarintField(msg, 5, 0)
	msg = appendStringField(msg, 6, "http://127.0.0.1:18080")
	msg = appendVarintField(msg, 8, boolToVarint(maxMode))
	return msg
}

func buildAgentUsableModelsPayload(adapters []*ModelAdapter) []byte {
	payload := []byte{}
	for _, adapter := range adapters {
		if adapter == nil {
			continue
		}
		payload = appendBytesField(payload, 1, encodeAgentModelDetails(adapter, false))
		if adapter.SupportsThinking {
			payload = appendBytesField(payload, 1, encodeAgentModelDetails(adapter, true))
		}
	}
	return payload
}

func buildAgentDefaultModelForCliPayload(adapters []*ModelAdapter) []byte {
	if len(adapters) == 0 || adapters[0] == nil {
		return nil
	}
	return appendBytesField(nil, 1, encodeAgentModelDetails(adapters[0], false))
}

func encodeAgentModelDetails(adapter *ModelAdapter, maxMode bool) []byte {
	modelID := adapter.CursorModelName()
	displayName := adapter.DisplayName
	if displayName == "" {
		displayName = adapter.ModelID
	}
	if maxMode {
		modelID += "-max"
		displayName += " Max"
	}

	msg := appendStringField(nil, 1, modelID)
	if adapter.SupportsThinking {
		msg = appendBytesField(msg, 2, nil)
	}
	msg = appendStringField(msg, 3, modelID)
	msg = appendStringField(msg, 4, displayName)
	msg = appendStringField(msg, 5, displayName)
	for _, alias := range adapter.LegacyCursorModelNames() {
		msg = appendStringField(msg, 6, alias)
	}
	msg = appendVarintField(msg, 7, boolToVarint(maxMode))
	return msg
}

func buildAllowedModelIntentsPayload() []byte {
	payload := []byte{}
	for _, intent := range []string{
		"chat",
		"agent",
		"composer",
		"cmdk",
		"edit",
		"plan",
		"apply",
	} {
		payload = appendStringField(payload, 1, intent)
	}
	return payload
}

func buildNameTabPayload() []byte {
	payload := appendStringField(nil, 1, "Local Chat")
	payload = appendStringField(payload, 2, "Generated locally")
	payload = appendStringField(payload, 3, "message-square")
	return payload
}

func buildServerTimePayload() []byte {
	now := uint64(time.Now().UnixMilli())
	payload := appendFixed64Field(nil, 1, float64(now)/1000)
	payload = appendFixed64Field(payload, 2, float64(now)/1000)
	return payload
}

func buildServerConfigPayload() []byte {
	payload := appendStringField(nil, 6, "local")
	chatConfig := appendVarintField(nil, 1, 30000)
	chatConfig = appendVarintField(chatConfig, 2, 100000)
	chatConfig = appendVarintField(chatConfig, 3, 100)
	chatConfig = appendVarintField(chatConfig, 4, 80)
	chatConfig = appendStringField(chatConfig, 5, "Chat context summarized.")
	payload = appendBytesField(payload, 5, chatConfig)
	return payload
}

func localStripeProfile() map[string]any {
	return map[string]any{
		"email":                    "cursor@local.invalid",
		"name":                     "Cursor Local",
		"membershipType":           "pro",
		"stripeStatus":             "active",
		"billingCycle":             "monthly",
		"daysRemainingOnTrial":     3650,
		"hasValidPaymentMethod":    true,
		"isTrialing":               false,
		"isActive":                 true,
		"isPro":                    true,
		"isTeam":                   false,
		"usageBasedPricingEnabled": true,
	}
}

func appendFixed64Field(dst []byte, fieldNumber int, value float64) []byte {
	dst = appendVarint(dst, uint64(fieldNumber<<3|1))
	bits := math.Float64bits(value)
	for i := 0; i < 8; i++ {
		dst = append(dst, byte(bits>>(8*i)))
	}
	return dst
}

func buildIsOnNewPricingPayload() []byte {
	payload := appendVarintField(nil, 1, 1)
	payload = appendVarintField(payload, 2, 0)
	payload = appendVarintField(payload, 3, 1)
	return payload
}

func responseContentType(requestContentType string) string {
	ct := strings.ToLower(requestContentType)
	if strings.Contains(ct, "grpc-web-text") {
		return "application/grpc-web-text+proto"
	}
	if strings.Contains(ct, "grpc-web") {
		return "application/grpc-web+proto"
	}
	if strings.Contains(ct, "grpc") {
		return "application/grpc"
	}
	if strings.Contains(ct, "connect") {
		return "application/connect+proto"
	}
	return "application/proto"
}

func isGRPCWebContentType(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "grpc-web")
}

func isGRPCWebTextContentType(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "grpc-web-text")
}
