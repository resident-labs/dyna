package runtime

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
	// Kimi Code's harness is retired, but its saved accounts still refresh and
	// report usage from config.toml and credentials/ in the account home, so
	// the account keeps the native files and environment its reader needs. No
	// harness lists, installs, leases or runs it.
	RegisterRuntimeAdapter(RuntimeAdapter{
		Harness:     "kimi-code",
		AccountOnly: true,
		AuthFiles:   []string{"config.toml", "credentials/kimi-code.json"},
		NativeEnv: func(home string) map[string]string {
			return map[string]string{
				"KIMI_CODE_HOME":           home,
				"KIMI_CODE_NO_AUTO_UPDATE": "1",
				"KIMI_DISABLE_TELEMETRY":   "1",
			}
		},
		UnsetEnv: kimiCodeInheritedEnv,
		// Nothing starts this login any more; the command only satisfies the
		// manager contract that builds the private environment.
		LoginArgs: func(executable, _ string) []string { return []string{executable, "login"} },
	})
}
