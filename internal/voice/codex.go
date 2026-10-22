package voice

import (
	"context"
	"errors"
	"time"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/harness/runtime"
)

// CodexCredentials reads the sign-in of Dyna's active Codex account. A token
// that expires within five minutes is refreshed first through the shared
// account refresh, which is the only writer of the login.
func CodexCredentials(ctx context.Context) (token, account string, err error) {
	accounts := runtime.NewAccounts()
	n, err := accounts.Active("codex")
	if err != nil {
		return "", "", errors.New("sign in to Codex in Settings > Harnesses first")
	}
	b, err := runtime.NewBundleManager().Resolve("codex", "")
	if err != nil {
		return "", "", errors.New("install Dyna's Codex in Settings > Harnesses first")
	}
	login, err := codexLogin(ctx, accounts, n.ID, b.Executable)
	if err != nil {
		return "", "", err
	}
	if time.Until(login.ExpiresAt) < 5*time.Minute {
		if _, err := accounts.RefreshCredentials(ctx, n.ID, b.Executable); err != nil {
			return "", "", errors.New("the Codex sign-in expired and could not be refreshed; sign in again")
		}
		if login, err = codexLogin(ctx, accounts, n.ID, b.Executable); err != nil {
			return "", "", err
		}
	}
	return native.CodexTokens(login.Credential)
}

func codexLogin(ctx context.Context, accounts *runtime.Accounts, id, executable string) (native.Captured, error) {
	m, err := accounts.Manager(id, executable)
	if err != nil {
		return native.Captured{}, err
	}
	logins, err := m.Capture(ctx, "")
	if err != nil || len(logins) == 0 {
		return native.Captured{}, errors.New("the Codex account is not signed in")
	}
	return logins[0], nil
}
