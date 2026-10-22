package runtime

import "path/filepath"

// piNPMPackage is the official Pi 1.1+ package. The old Dyna catalog name
// @mariozechner/pi-coding-agent is stale and must never be installed.
const piNPMPackage = "@earendil-works/pi-coding-agent"

// piProviderEnv lists inherited provider credential and endpoint variables that
// docs/providers.md of the pinned package documents for Pi. The private account
// clears them so it can never silently use the owner's personal provider
// identity, credentials, or endpoints.
var piProviderEnv = []string{
	"ANTHROPIC_API_KEY", "ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN",
	"ANTHROPIC_FEDERATION_RULE_ID", "ANTHROPIC_ORGANIZATION_ID", "ANTHROPIC_IDENTITY_TOKEN_FILE",
	"ANTHROPIC_SERVICE_ACCOUNT_ID", "ANTHROPIC_WORKSPACE_ID",
	"ANT_LING_API_KEY", "OPENAI_API_KEY", "DEEPSEEK_API_KEY", "NVIDIA_API_KEY",
	"GEMINI_API_KEY", "COPILOT_GITHUB_TOKEN", "MISTRAL_API_KEY", "GROQ_API_KEY",
	"CEREBRAS_API_KEY", "XAI_API_KEY", "OPENROUTER_API_KEY", "AI_GATEWAY_API_KEY",
	"ZAI_API_KEY", "ZAI_CODING_CN_API_KEY", "OPENCODE_API_KEY", "RADIUS_API_KEY",
	"TYPESAFE_API_KEY", "HF_TOKEN", "FIREWORKS_API_KEY", "TOGETHER_API_KEY",
	"BASETEN_API_KEY", "KIMI_API_KEY", "META_API_KEY", "MINIMAX_API_KEY",
	"MINIMAX_CN_API_KEY", "MOONSHOT_API_KEY", "QWEN_TOKEN_PLAN_API_KEY",
	"QWEN_TOKEN_PLAN_CN_API_KEY", "XIAOMI_API_KEY", "XIAOMI_TOKEN_PLAN_CN_API_KEY",
	"XIAOMI_TOKEN_PLAN_AMS_API_KEY", "XIAOMI_TOKEN_PLAN_SGP_API_KEY",
	"AWS_PROFILE", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
	"AWS_BEARER_TOKEN_BEDROCK", "AWS_WEB_IDENTITY_TOKEN_FILE",
	"AZURE_OPENAI_API_KEY", "AZURE_OPENAI_BASE_URL", "AZURE_OPENAI_RESOURCE_NAME",
	"GOOGLE_CLOUD_API_KEY", "GOOGLE_APPLICATION_CREDENTIALS",
	"CLOUDFLARE_API_KEY", "CLOUDFLARE_ACCOUNT_ID",
}

func init() {
	RegisterRuntimeAdapter(RuntimeAdapter{
		Harness:      "pi",
		Binary:       "pi",
		ConfigFormat: "json",
		MCPKey:       "mcpServers",
		ConfigFile:   "settings.json",
		SkillDir:     "skills",
		// Pi loads $PI_CODING_AGENT_DIR/AGENTS.md as global rules.
		NativeEnv:  piNativeEnv,
		ConfigArgs: piConfigArgs,
		LoginArgs:  piLoginArgs,
		// Pi has no account of its own. /login inside Pi stores provider OAuth
		// grants and API keys in auth.json. Pi runs with its own defaults:
		// Dyna imports no personal preferences.
		AuthFiles: []string{"auth.json"},
		// Account sign-in is not blocked: /login stores credentials in the
		// private auth.json, so no shared credential backend is touched.
		AccountBlocked: "",
		// Turns share the account lease. Verified 2026-10-21 on Pi 1.3.0: two
		// `pi --model zai/glm-5.3-flash -p` runs in one PI_CODING_AGENT_DIR both
		// exited 0 side by side, and a third turn ran in the same home. Pi writes
		// each session to its own file under sessions/, and auth.json holds a
		// static API key. An exclusive lease queued new turns behind a long one.
	})
}

// piNativeEnv scopes Pi to one private account home. The agent directory covers
// settings, mcp.json, auth.json, skills, and user instructions. Shared launch
// code supplies private HOME/XDG roots; PI_OFFLINE stops automatic network activity.
func piNativeEnv(home string) map[string]string {
	env := map[string]string{
		"PI_CODING_AGENT_DIR":         home,
		"PI_CODING_AGENT_SESSION_DIR": filepath.Join(home, "sessions"),
		"PI_OFFLINE":                  "1",
		// PI_SKIP_VERSION_CHECK stops the pi.dev latest-version request.
		"PI_SKIP_VERSION_CHECK": "1",
	}
	for _, key := range piProviderEnv {
		env[key] = ""
	}
	return env
}

// piConfigArgs pins sessions into the private home. Pi has no settings or skill
// flag, so main materializes those views inside the agent directory and the
// configPath and skillPath parameters are unused here. --session-dir takes
// precedence over the sessionDir setting and the environment.
func piConfigArgs(home, configPath, skillPath string) []string {
	return []string{"--session-dir", filepath.Join(home, "sessions")}
}

// piLoginArgs returns a complete argv for the interactive sign-in flow. Pi has
// no non-interactive login command; the user runs /login in the terminal UI and
// Pi stores the credential in <agent-dir>/auth.json inside the private home.
func piLoginArgs(executable, home string) []string {
	return []string{executable}
}
