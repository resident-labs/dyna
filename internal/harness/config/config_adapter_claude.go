package config

func init() { RegisterConfigAdapter("claude-code", nativeConfigAdapter{harness: "claude-code"}) }
