package workflow

import (
	"context"
	"testing"
)

// Retired profile review APIs must not expose the removed store.
func TestProfileReviewAPIsAreAbsent(t *testing.T) {
	result, err := executeTest(context.Background(), Options{
		ScriptSrc: `return {
   rateProfile: typeof rateProfile,
   summary: typeof __profileReviewSummary,
   stars: typeof profiles[0].stars,
   reviewCount: typeof profiles[0].reviewCount,
   taste: profiles[0].taste,
   intelligence: profiles[0].intelligence
  };`,
		Store: postTestStore(), WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"rateProfile":"undefined","summary":"undefined","stars":"undefined","reviewCount":"undefined","taste":5,"intelligence":5}`
	if result != want {
		t.Fatalf("profile API = %s, want %s", result, want)
	}
}
