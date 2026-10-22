package config

func init() { RegisterConfigAdapter("opencode", nativeConfigAdapter{harness: "opencode"}) }
