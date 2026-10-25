package voice

import (
	"context"
	"errors"
	"time"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
	"dyna-agent/internal/harness/runtime"
)

// ChatGPTCredentials reads the sign-in of the ChatGPT account that Pi uses.
// Voice runs on that account's OpenAI grant. A token that expires within five
// minutes is renewed first through the shared account refresh, which is the
// only writer of a saved login.
func ChatGPTCredentials(ctx context.Context) (token, account string, err error) {
	login, err := chatgptLogin(ctx)
	if err != nil {
		return "", "", err
	}
	if expiry := native.CredentialExpiry(login.Credential); !expiry.IsZero() && time.Until(expiry) < 5*time.Minute {
		accounts := runtime.NewAccounts()
		if _, err := accounts.RefreshCredentials(ctx, activeChatGPTAccount(), ""); err != nil {
			return "", "", errors.New("the ChatGPT sign-in expired and Dyna cannot renew it; run a Pi turn or sign in again")
		}
		if login, err = chatgptLogin(ctx); err != nil {
			return "", "", err
		}
	}
	return native.ChatGPTTokens(login.Credential)
}

func chatgptLogin(ctx context.Context) (native.Captured, error) {
	id := activeChatGPTAccount()
	if id == "" {
		return native.Captured{}, errors.New("sign in to a ChatGPT account in Settings > Accounts first")
	}
	accounts := runtime.NewAccounts()
	m, err := accounts.Manager(id, "")
	if err != nil {
		return native.Captured{}, errors.New("choose the ChatGPT account on the Accounts page first")
	}
	logins, err := m.Capture(ctx, "")
	if err != nil || len(logins) == 0 {
		return native.Captured{}, errors.New("the ChatGPT account is not signed in")
	}
	return logins[0], nil
}

// activeChatGPTAccount names the ChatGPT account Pi uses, or "" when none is
// signed in.
func activeChatGPTAccount() string {
	accounts := runtime.NewAccounts()
	id, err := (&store.Store{Root: accounts.Root}).Active(store.ProviderChatGPT, "pi", "")
	if err != nil {
		return ""
	}
	return id
}
