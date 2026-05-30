package statsig

import (
	"encoding/json"
	"time"
)

const DefaultUserID = "cursor-local-assistant"

// BuildBootstrapConfig returns the JSON string Cursor stores in
// workbench.experiments.statsigBootstrap and passes into the Statsig SDK.
func BuildBootstrapConfig(userID string, generatedAtMs int64) string {
	if userID == "" {
		userID = DefaultUserID
	}
	if generatedAtMs <= 0 {
		generatedAtMs = time.Now().UnixMilli()
	}

	http2PingConfig := map[string]any{
		"enabled":                 []string{},
		"pingIdleConnection":      nil,
		"pingIntervalMs":          nil,
		"pingTimeoutMs":           nil,
		"idleConnectionTimeoutMs": nil,
	}
	config := map[string]any{
		"has_updates":        true,
		"time":               generatedAtMs,
		"feature_gates":      map[string]any{},
		"layer_configs":      map[string]any{},
		"param_stores":       map[string]any{},
		"exposures":          []any{},
		"sdk_flags":          map[string]any{},
		"can_record_session": false,
		"user": map[string]any{
			"userID": userID,
			"customIDs": map[string]any{
				"stableID": userID,
			},
		},
		"dynamic_configs": map[string]any{
			"http2_ping_config": dynamicConfig("http2_ping_config", http2PingConfig),
		},
	}
	data, _ := json.Marshal(config)
	return string(data)
}

func dynamicConfig(name string, value map[string]any) map[string]any {
	return map[string]any{
		"name":                            name,
		"value":                           value,
		"rule_id":                         "local",
		"group":                           "local",
		"group_name":                      "local",
		"is_experiment_active":            false,
		"is_user_in_experiment":           false,
		"is_device_based":                 false,
		"id_type":                         "userID",
		"secondary_exposures":             []any{},
		"undelegated_secondary_exposures": []any{},
	}
}

// HasUsableHTTP2PingConfig reports whether a cached bootstrap contains the
// dynamic config shape Cursor's Always Local extension expects at startup.
func HasUsableHTTP2PingConfig(raw string) bool {
	var root map[string]any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return false
	}
	dynamicConfigs, _ := root["dynamic_configs"].(map[string]any)
	entry, _ := dynamicConfigs["http2_ping_config"].(map[string]any)
	value, _ := entry["value"].(map[string]any)
	_, ok := value["enabled"].([]any)
	return ok
}
