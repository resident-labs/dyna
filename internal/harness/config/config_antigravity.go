package config

func init() { RegisterConfigAdapter("antigravity", nativeConfigAdapter{harness: "antigravity"}) }
