package config

func init() { RegisterConfigAdapter("codex", nativeConfigAdapter{harness: "codex"}) }
