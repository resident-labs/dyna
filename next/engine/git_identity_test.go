package engine

import (
	"github.com/resident-labs/dyna/contract"
	"testing"
)

func TestGitIdentityReachesBoxHello(t *testing.T) {
	s, _, sid, ctx := setup(t)
	s.SetGitIdentity("Example User", "user@example.invalid")
	hello, problem := s.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: sid}, contract.BoxHelloRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	if hello.GitName != "Example User" || hello.GitEmail != "user@example.invalid" {
		t.Fatalf("identity = %q <%s>", hello.GitName, hello.GitEmail)
	}
}
