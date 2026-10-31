package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestProfileCompactionTargetReachesBox(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.json")
	data := `{"test":{"provider":"anthropic","model":"model","allowedModels":["model"],"thinking":"low","compactTokens":150000}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	profiles, err := LoadProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	s, _, sid, ctx := setup(t)
	s.profiles = profiles
	response, problem := s.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: sid}, contract.BoxHelloRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	wire, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		CompactTokens int `json:"compactTokens"`
	}
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.CompactTokens != 150000 {
		t.Fatalf("box hello compactTokens=%d, want 150000", decoded.CompactTokens)
	}
}
