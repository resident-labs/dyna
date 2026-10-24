package config

func init() { RegisterConfigAdapter("kimi-code", nativeConfigAdapter{harness: "kimi-code"}) }
