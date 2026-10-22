package config

func init() { RegisterConfigAdapter("grok", nativeConfigAdapter{harness: "grok"}) }
