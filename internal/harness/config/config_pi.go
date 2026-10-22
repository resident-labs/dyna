package config

func init() { RegisterConfigAdapter("pi", nativeConfigAdapter{harness: "pi"}) }
