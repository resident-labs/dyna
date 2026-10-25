package voice

import (
	"context"
	"errors"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
	"dyna-agent/internal/harness/runtime"
)

// ChatGPTCredentials reads the sign-in of the ChatGPT account that Pi uses.
// Voice runs on that account's OpenAI grant.
//
// Voice does not renew the grant. Pi owns it: the account's own home is Pi's
// live login file, and Pi renews that file on its next request in that account.
// Renewing it here would give two writers one refresh token, which needs the
// harness switch gate and can invalidate the grant. An expired token therefore
// fails the call with a sign-in message instead of pretending to renew it.
func ChatGPTCredentials(ctx context.Context) (token, account string, err error) {
	login, err := chatgptLogin(ctx)
	if err != nil {
		return "", "", err
	}
	token, account, err = native.ChatGPTTokens(login.Credential)
	if err != nil {
		return "", "", errors.New("the ChatGPT account has no usable sign-in; sign in again")
	}
	return token, account, nil
}

// chatgptLogin captures the live login of the ChatGPT account that Pi uses.
func chatgptLogin(ctx context.Context) (native.Captured, error) {
	accounts := runtime.NewAccounts()
	id := activeChatGPTAccount(accounts)
	if id == "" {
		return native.Captured{}, errors.New("sign in to a ChatGPT account in Settings > Accounts first")
	}
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
func activeChatGPTAccount(accounts *runtime.Accounts) string {
	id, err := (&store.Store{Root: accounts.Root}).Active(store.ProviderChatGPT, "pi", "")
	if err != nil {
		return ""
	}
	return id
}
