package runtime

import "path/filepath"

// kimiCodeInheritedEnv lists Kimi settings that could inject an owner's
// credentials, provider endpoint, model, headers, or region into a private
// account. NativeEnvironmentBase removes them before profile and native
// overrides are applied. Kimi Code CLI 2.3.1 documents each key in
// docs/en/configuration/env-vars.md.
var kimiCodeInheritedEnv = []string{
	"KIMI_API_KEY", "KIMI_BASE_URL", "KIMI_REGISTRY_API_KEY",
	"KIMI_CODE_OAUTH_HOST", "KIMI_OAUTH_HOST", "KIMI_CODE_BASE_URL", "KIMI_CODE_REGION_MARKER",
	"KIMI_CODE_CUSTOM_HEADERS", "KIMI_CODE_PASSWORD",
	"KIMI_MODEL_NAME", "KIMI_MODEL_API_KEY", "KIMI_MODEL_PROVIDER_TYPE", "KIMI_MODEL_BASE_URL",
	"KIMI_MODEL_MAX_CONTEXT_SIZE", "KIMI_MODEL_CAPABILITIES", "KIMI_MODEL_DISPLAY_NAME",
	"KIMI_MODEL_MAX_OUTPUT_SIZE", "KIMI_MODEL_REASONING_KEY", "KIMI_MODEL_THINKING_EFFORT",
	"KIMI_MODEL_THINKING_KEEP", "KIMI_MODEL_TEMPERATURE", "KIMI_MODEL_TOP_P", "KIMI_MODEL_MAX_COMPLETION_TOKENS",
	"KIMI_WEB_SEARCH_BASE_URL", "KIMI_WEB_SEARCH_API_KEY", "KIMI_WEB_FETCH_BASE_URL", "KIMI_WEB_FETCH_API_KEY",
}

func init() {
	RegisterRuntimeAdapter(RuntimeAdapter{
		Harness:      "kimi-code",
		Binary:       "kimi",
		ConfigFormat: "json",
		MCPKey:       "mcpServers",
		// config.toml contains the managed OAuth reference and model catalog.
		// Generations must never replace it. ACP receives the generated MCP
		// catalog directly; only skills use the shared account-home view.
		ConfigFile: "",
		SkillDir:   "skills",
		AuthFiles:  []string{"config.toml", "credentials/kimi-code.json"},
		NativeEnv: func(home string) map[string]string {
			return map[string]string{
				"KIMI_CODE_HOME":           home,
				"KIMI_CODE_NO_AUTO_UPDATE": "1",
				"KIMI_DISABLE_TELEMETRY":   "1",
			}
		},
		UnsetEnv: kimiCodeInheritedEnv,
		// Kimi ACP has no catalog-path flag. The shared ACP launcher consumes
		// this internal generation marker and never forwards it to the CLI.
		ConfigArgs: func(_ string, configPath, _ string) []string {
			return []string{"--add-dir", filepath.Dir(configPath)}
		},
		LoginArgs: func(executable, _ string) []string { return []string{executable, "login"} },
	})
}
