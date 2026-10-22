package config

func init() { RegisterConfigAdapter("muse", nativeConfigAdapter{harness: "muse"}) }
