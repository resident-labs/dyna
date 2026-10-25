package runtime

func init() {
	// Grok's harness is retired, but its saved accounts still refresh and
	// report usage from auth.json in the account home, so the account keeps
	// the native files and environment its reader needs. No harness lists,
	// installs, leases or runs it.
	RegisterRuntimeAdapter(RuntimeAdapter{
		Harness:     "grok",
		AccountOnly: true,
		AuthFiles:   []string{"auth.json"},
		NativeEnv: func(home string) map[string]string {
			return map[string]string{
				"GROK_HOME":             home,
				"XAI_API_KEY":           "",
				"GROK_XAI_API_BASE_URL": "",
				"GROK_MODELS_BASE_URL":  "",
				"GROK_MODELS_LIST_URL":  "",
				"GROK_DEFAULT_MODEL":    "",
				"GROK_WEB_SEARCH_MODEL": "",
				"GROK_CONFIG":           "",
				"GROK_CONFIG_PATH":      "",
			}
		},
		// Nothing starts this login any more; the empty command only lets the
		// account manager build the private environment for capture and
		// activation.
		LoginArgs: func(string, string) []string { return nil },
	})
}
