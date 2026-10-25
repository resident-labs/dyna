package runtime

import (
	"context"
	"testing"
)

// Selecting an account changes a stored row. It must not read vendor metadata:
// that fetch goes to the network, so a caller that only wants the switch would
// wait seconds for a snapshot it does not show.
func TestUseAccountReadsNoVendorMetadata(t *testing.T) {
	r := testPrivateRuntime(t)
	a := testAccounts(t)
	work := signedClaude(t, a, "work", "aaaaaaaa-bbbb-cccc-dddd-000000000002")
	r.Accounts = a
	r.privateRecommendations = func(context.Context) []RecommendedVersion {
		t.Error("selecting an account read vendor metadata over the network")
		return nil
	}
	if err := r.UseAccount(context.Background(), work.ID, "room-one"); err != nil {
		t.Fatal(err)
	}
}
