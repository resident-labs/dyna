package native

// ProviderKey names one provider of a harness. It never carries a key.
type ProviderKey struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Auth says how the provider is signed in: "api_key" or "oauth". It is
	// empty for a catalog choice.
	Auth string `json:"auth,omitempty"`
}
