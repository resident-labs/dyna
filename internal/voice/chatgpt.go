package voice

import (
	"context"
	"errors"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/harness/runtime"
)

// ChatGPTCredentials reads the sign-in of the ChatGPT account that Pi uses.
// Voice runs on that account's OpenAI grant. The engine's web search tool reads
// the same grant through the same reader, so account selection, token
// validation and renewal follow one set of rules for both.
func ChatGPTCredentials(ctx context.Context) (token, account string, err error) {
	grant, err := runtime.NewAccounts().ChatGPTGrant(ctx, "", "")
	if err != nil {
		return "", "", chatGPTError(err)
	}
	return grant.Access, grant.Account, nil
}

// chatGPTError is what Voice says when the one reader cannot supply the grant.
func chatGPTError(err error) error {
	switch {
	case errors.Is(err, native.ErrNotSignedIn):
		return errors.New("sign in to a ChatGPT account in Settings > Accounts first")
	case errors.Is(err, runtime.ErrGrantExpired):
		return errors.New("the ChatGPT sign-in expired and could not be renewed; sign in again")
	case errors.Is(err, runtime.ErrPiMissing):
		return errors.New("Dyna's copy of Pi is not installed; Voice cannot renew the ChatGPT sign-in")
	}
	return err
}
