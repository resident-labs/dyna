package ghclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/boundedio"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
)

type repositoryContextKey struct{}

// WithRepository supplies repository scope for calls such as GraphQL batches.
func WithRepository(ctx context.Context, host, repo string) context.Context {
	return context.WithValue(ctx, repositoryContextKey{}, repository{host, repo})
}

// AccountFor selects an account without exposing its token.
func AccountFor(ctx context.Context, executable, host, repo string) (string, error) {
	login, _, err := selectAccount(ctx, Command(ctx, executable, ""), repository{host, repo})
	return login, err
}

type repository struct{ host, name string }

// ErrNoSignedInAccount marks a room whose saved GitHub logins cannot act.
// It costs the turn its GitHub commands, never the turn.
var ErrNoSignedInAccount = errors.New("no logged-in GitHub account")

const accountCacheTTL = 30 * time.Minute

// accountProvedTTL is how long one proven access check is trusted, across turns
// and across engine restarts, so a normal turn makes no GitHub API call. The
// stored choice is dropped when the account or the room's access changes.
const accountProvedTTL = 12 * time.Hour

type accountFlight struct {
	done  chan struct{}
	login string
	err   error
}

type choice struct {
	login string
	until time.Time
	// config is the GitHub CLI configuration the choice was proved against. A
	// different one, such as a replaced token or another account, drops it.
	config string
}

var accounts = struct {
	sync.Mutex
	choices map[string]choice
	flights map[string]*accountFlight
	// limited holds the time GitHub said the quota returns, per choice key, so
	// a turn during an outage makes no request at all.
	limited map[string]time.Time
}{choices: make(map[string]choice), flights: make(map[string]*accountFlight), limited: make(map[string]time.Time)}

func repositoryFor(ctx context.Context, cmd *exec.Cmd) repository {
	if r, ok := ctx.Value(repositoryContextKey{}).(repository); ok && r.name != "" {
		return r
	}

	host := "github.com"
	for _, entry := range cmd.Env {
		if value, ok := strings.CutPrefix(entry, "GH_HOST="); ok && value != "" {
			host = value
		}
	}
	repo := ""
	args := cmd.Args[1:]
	flags := true
	var command []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if flags && arg == "--" {
			flags = false
			continue
		}
		if flags && strings.HasPrefix(arg, "-") {
			if !strings.HasPrefix(arg, "--") {
				for j := 1; j < len(arg); j++ {
					flag := arg[j : j+1]
					if flag != "R" && !repositoryFlagTakesValue(command, "-"+flag) {
						continue
					}
					value := strings.TrimPrefix(arg[j+1:], "=")
					if j+1 == len(arg) && i+1 < len(args) {
						i++
						value = args[i]
					}
					if flag == "R" {
						repo = value
					}
					break
				}
				continue
			}
			flag, value, attached := strings.Cut(arg, "=")
			if flag == "--repo" || flag == "--hostname" {
				if !attached && i+1 < len(args) {
					i++
					value = args[i]
				}
				if flag == "--hostname" {
					host = value
				} else {
					repo = value
				}
			} else if !attached && repositoryFlagTakesValue(command, flag) && i+1 < len(args) {
				// A data value can itself look like a flag, URL, or API path.
				i++
			}
			continue
		}
		if len(command) == 2 && command[0] == "repo" && command[1] == "view" && !strings.HasPrefix(arg, "-") {
			repo = arg
		}
		if len(command) < 2 {
			command = append(command, arg)
		}
		if strings.HasPrefix(arg, "https://") || strings.HasPrefix(arg, "http://") {
			if u, err := url.Parse(arg); err == nil {
				parts := strings.Split(strings.Trim(u.Path, "/"), "/")
				if len(parts) >= 2 {
					return repository{u.Hostname(), parts[0] + "/" + parts[1]}
				}
			}
		}
		if path := strings.TrimPrefix(arg, "/"); strings.HasPrefix(path, "repos/") {
			parts := strings.Split(path, "/")
			if len(parts) >= 3 {
				repo = parts[1] + "/" + parts[2]
			}
		}
	}
	if repo != "" {
		parts := strings.Split(repo, "/")
		if len(parts) == 3 {
			host, repo = parts[0], strings.Join(parts[1:], "/")
		}
		return repository{host, repo}
	}
	if cmd.Dir == "" {
		return repository{}
	}
	remote, err := repoid.Git(ctx, cmd.Dir, "remote", "get-url", "origin")
	if err != nil {
		return repository{}
	}
	identity, err := repoid.RemoteIdentity(cmd.Dir, remote)
	if err != nil {
		return repository{}
	}
	parts := strings.Split(identity, "/")
	if len(parts) != 3 || parts[0] == "" {
		return repository{}
	}
	return repository{parts[0], parts[1] + "/" + parts[2]}
}

// These gh flags consume the next argument even when it starts with a dash.
func repositoryFlagTakesValue(command []string, flag string) bool {
	commandName := strings.Join(command, " ")
	if len(command) > 0 && command[0] == "api" {
		commandName = "api"
	}
	if flag == "--comment" {
		return commandName == "pr close" || commandName == "issue close"
	}
	if len(flag) == 2 && flag[0] == '-' {
		// Short flags are command-specific: -s is a merge strategy for
		// pr merge, but a state value for pr list.
		var short string
		switch commandName {
		case "api":
			short = "FfHXpqt"
		case "pr merge":
			short = "AbFt"
		case "pr create":
			short = "aBbFHlmprTt"
		case "issue create":
			short = "abFlmpTt"
		case "pr edit", "issue edit":
			short = "BbFmt"
		case "pr list", "issue list":
			short = "aABHqlLmSst"
		case "pr view", "issue view":
			short = "qt"
		case "repo view":
			short = "bqt"
		case "pr checks":
			short = "iqt"
		case "pr comment", "issue comment", "pr review":
			short = "bF"
		case "pr close":
			short = "c"
		case "issue close":
			short = "cr"
		case "pr checkout":
			short = "b"
		case "run view":
			short = "ajqt"
		case "run list":
			short = "bceLqstuw"
		}
		return strings.ContainsRune(short, rune(flag[1]))
	}
	switch flag {
	case "--title", "--body", "--body-file",
		"--field", "--raw-field", "--input", "--header",
		"--method", "--jq", "--template", "--json",
		"--search", "--label", "--assignee",
		"--reviewer", "--milestone", "--project",
		"--base", "--head", "--state", "--limit",
		"--author", "--mention", "--app", "--match-head-commit",
		"--subject", "--author-email", "--delete-label", "--remove-label", "--add-label",
		"--add-assignee", "--remove-assignee", "--add-reviewer", "--remove-reviewer",
		"--add-project", "--remove-project", "--color", "--attempt",
		"--cache", "--preview", "--attach", "--recover", "--blocked-by", "--blocking",
		"--parent", "--type", "--add-blocked-by", "--add-blocking", "--add-sub-issue",
		"--remove-blocked-by", "--remove-blocking", "--remove-sub-issue", "--branch",
		"--interval", "--duplicate-of", "--reason", "--worktree", "--job",
		"--commit", "--created", "--event", "--status", "--workflow":
		return true
	}
	return false
}

func withoutTokens(env []string) []string {
	clean := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if key != "GH_TOKEN" && key != "GITHUB_TOKEN" && key != "GH_ENTERPRISE_TOKEN" && key != "GITHUB_ENTERPRISE_TOKEN" {
			clean = append(clean, entry)
		}
	}
	return clean
}

// authOutput never exposes stderr or token contents in an error.
func authOutput(ctx context.Context, original *exec.Cmd, token string, args ...string) ([]byte, error) {
	raw, _, err := authResult(ctx, original, token, args...)
	return raw, err
}

func authResult(ctx context.Context, original *exec.Cmd, token string, args ...string) ([]byte, []byte, error) {
	cmd := Command(ctx, original.Path, original.Dir, args...)
	cmd.Env = withoutTokens(original.Env)
	if token != "" {
		cmd.Env = append(cmd.Env, "GH_TOKEN="+token, "GH_ENTERPRISE_TOKEN="+token)
	}
	out := boundedio.New(1024 * 1024)
	stderr := boundedio.New(8192)
	cmd.Stdout, cmd.Stderr = out, stderr
	err := cmd.Run()
	if err != nil || out.Truncated() || stderr.Truncated() {
		err = errors.New("GitHub account check failed")
	}
	return append([]byte(nil), out.Bytes()...), append([]byte(nil), stderr.Bytes()...), err
}

func accountToken(ctx context.Context, cmd *exec.Cmd, host, login string) (string, error) {
	raw, err := authOutput(ctx, cmd, "", "auth", "token", "--hostname", host, "--user", login)
	return strings.TrimSpace(string(raw)), err
}

func accountKey(cmd *exec.Cmd, r repository) string {
	// Different gh installations and config directories must never share a choice.
	config := ""
	for _, entry := range cmd.Env {
		if strings.HasPrefix(entry, "GH_CONFIG_DIR=") || strings.HasPrefix(entry, "HOME=") || strings.HasPrefix(entry, "XDG_CONFIG_HOME=") {
			config += entry + "\n"
		}
	}
	return cmd.Path + "\n" + config + strings.ToLower(r.host+"/"+r.name)
}

func accountProofKey(ctx context.Context, cmd *exec.Cmd, r repository, rec room.Record, write bool) string {
	key := accountKey(cmd, r) + rec.ID + Access(ctx, rec)
	// Read and write proofs can select different accounts and must not share.
	if write {
		return key + ":write"
	}
	return key + ":read"
}

// selectAccount chooses the account for r. The room's upstream repository, the
// parent of a fork checkout, gets a read-only account, as checkPolicy allows
// only reads of it.
func selectAccount(ctx context.Context, cmd *exec.Cmd, r repository) (string, string, error) {
	rec, err := roomFor(ctx, cmd.Dir, r)
	write := Access(ctx, rec) == ReadWrite
	if errors.Is(err, errNotInRoom) && matchesUpstream(rec, r) {
		err, write = nil, false
	}
	if err != nil {
		return "", "", err
	}
	return selectAccountForRoom(ctx, cmd, r, rec, write)
}

// selectAccountAfterPolicy uses the room that checkPolicy just admitted. A
// fork parent needs only read access, even though the acting room is read-write.
func selectAccountAfterPolicy(ctx context.Context, cmd *exec.Cmd, r repository) (string, string, error) {
	rec, err := roomFor(ctx, cmd.Dir, r)
	write := Access(ctx, rec) == ReadWrite
	if errors.Is(err, errNotInRoom) {
		err = nil
		write = false
	}
	if err != nil {
		return "", "", err
	}
	return selectAccountForRoom(ctx, cmd, r, rec, write)
}

func selectAccountForRoom(ctx context.Context, cmd *exec.Cmd, r repository, rec room.Record, write bool) (string, string, error) {
	key := accountProofKey(ctx, cmd, r, rec, write)
	config := ghConfigFingerprint(cmd)
	accounts.Lock()
	inMemory, haveMemory := accounts.choices[key]
	accounts.Unlock()
	if haveMemory && inMemory.config != config {
		// The GitHub CLI configuration changed, so nothing it proved still holds.
		forgetAccount(key)
		forgetChoice(key)
	}
	// A choice an earlier engine process proved is as good as a cached one, so
	// a restart or an empty cache does not probe GitHub again for every session.
	if stored, ok := storedChoice(key, config); ok {
		accounts.Lock()
		if current, have := accounts.choices[key]; !have || stored.until.After(current.until) {
			accounts.choices[key] = stored
		}
		accounts.Unlock()
	}
	for {
		accounts.Lock()
		cached := accounts.choices[key]
		if time.Now().Before(cached.until) && cached.config == config {
			accounts.Unlock()
			if token, err := accountToken(ctx, cmd, r.host, cached.login); err == nil && token != "" {
				return cached.login, token, nil
			}
			if ctx.Err() != nil {
				return "", "", ctx.Err()
			}
			// A managed shell cannot open the desktop keyring, so a saved login
			// gives no token there. The command already carries that account's
			// token, so check it before a new probe replaces the stale choice.
			if token, err := sessionTokenFor(ctx, cmd, r, key, cached.login, write); err != nil {
				// The check could not finish, so the saved proof stays valid.
				return "", "", rememberLimit(key, err)
			} else if token != "" {
				return cached.login, token, nil
			}
			accounts.Lock()
			if accounts.choices[key] == cached {
				delete(accounts.choices, key)
			}
			accounts.Unlock()
			// The account really changed, so its stored choice is stale too.
			forgetChoice(key)
			continue
		}
		until, limited := accounts.limited[key]
		accounts.Unlock()
		// GitHub already refused a request in this window: prove nothing again
		// until the quota returns, and use the last known account when there is
		// one. The caller otherwise starts without GitHub access.
		if limited && time.Now().Before(until) {
			if cached.login != "" {
				if token, err := accountToken(ctx, cmd, r.host, cached.login); err == nil && token != "" {
					cacheAccount(key, cached.login, config)
					return cached.login, token, nil
				}
			}
			return "", "", &accountProbeError{cause: "GitHub API rate limited; retrying", transient: true, until: until}
		}
		accounts.Lock()
		flight := accounts.flights[key]
		if flight == nil {
			flight = &accountFlight{done: make(chan struct{})}
			accounts.flights[key] = flight
			original := &exec.Cmd{Path: cmd.Path, Dir: cmd.Dir, Env: append([]string(nil), cmd.Env...)}
			// One cancelled caller must not cancel the probe for other callers.
			go func() {
				probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				defer cancel()
				login, err := refreshAccount(probeCtx, original, r, key, cached, write)
				accounts.Lock()
				flight.login, flight.err = login, err
				delete(accounts.flights, key)
				close(flight.done)
				accounts.Unlock()
			}()
		}
		accounts.Unlock()
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-flight.done:
			if flight.err != nil || flight.login == "" {
				if flight.err == nil || errors.Is(flight.err, ErrNoSignedInAccount) {
					login, token, err := adoptSessionToken(ctx, cmd, r, key, write)
					switch {
					case err != nil:
						return "", "", rememberLimit(key, err)
					case token != "":
						return login, token, nil
					}
				}
				return "", "", flight.err
			}
			token, err := accountToken(ctx, cmd, r.host, flight.login)
			if (err != nil || token == "") && ctx.Err() == nil {
				reused, reuseErr := sessionTokenFor(ctx, cmd, r, key, flight.login, write)
				if reuseErr != nil {
					return "", "", rememberLimit(key, reuseErr)
				}
				if reused != "" {
					return flight.login, reused, nil
				}
			}
			return flight.login, token, err
		}
	}
}

// sessionTokenFor returns the token the command already carries when GitHub
// confirms it belongs to want and can reach this repository. An error means the
// check could not finish; it never means the token is wrong.
func sessionTokenFor(ctx context.Context, cmd *exec.Cmd, r repository, key, want string, write bool) (string, error) {
	token := inheritedToken(cmd.Env, r.host)
	if token == "" {
		return "", nil
	}
	if err := limitWindow(key); err != nil {
		return "", err
	}
	login, denied, err := probeAccount(ctx, cmd, r.host, token)
	if err != nil || denied || !strings.EqualFold(login, want) {
		// Another account's token must never borrow this account's identity.
		return "", err
	}
	// The saved proof belongs to the saved token. Another token for the same
	// login can have a narrower repository scope, so check this one too.
	denied, err = probeRepository(ctx, cmd, token, r, write)
	if err != nil || denied {
		return "", err
	}
	return token, nil
}

// adoptSessionToken returns the account a command's own token belongs to when
// no saved login can give a token. The token must belong to a login the GitHub
// CLI saved for this host and reach this repository. Nothing is cached: a saved
// proof must only ever describe the saved credential.
func adoptSessionToken(ctx context.Context, cmd *exec.Cmd, r repository, key string, write bool) (login, token string, err error) {
	token = inheritedToken(cmd.Env, r.host)
	if token == "" {
		return "", "", nil
	}
	if err := limitWindow(key); err != nil {
		return "", "", err
	}
	// The token can only stand in for a saved login, so a host with none needs
	// no request at all.
	saved, err := savedLogins(ctx, cmd, r.host)
	if err != nil || len(saved) == 0 {
		return "", "", err
	}
	login, denied, err := probeAccount(ctx, cmd, r.host, token)
	if err != nil || denied {
		return "", "", err
	}
	if !slices.ContainsFunc(saved, func(entry string) bool { return strings.EqualFold(entry, login) }) {
		return "", "", nil
	}
	denied, err = probeRepository(ctx, cmd, token, r, write)
	if err != nil || denied {
		return "", "", err
	}
	return login, token, nil
}

// inheritedToken returns the account token the command's own environment holds
// for the host, in the order the GitHub CLI prefers those variables.
func inheritedToken(env []string, host string) string {
	primary, fallback := tokenEnvNames(host)
	if token := environmentValue(env, primary); token != "" {
		return token
	}
	return environmentValue(env, fallback)
}

// environmentValue reads the value for key the way os/exec prepares a command
// environment: the last duplicate wins, so a later override is the one gh gets.
func environmentValue(env []string, key string) string {
	value := ""
	for _, entry := range env {
		if found, ok := strings.CutPrefix(entry, key+"="); ok {
			value = found
		}
	}
	return value
}

// limitWindow returns the transient error an earlier answer set for key, when
// GitHub's quota has not returned yet, so no probe is spent in that window.
func limitWindow(key string) error {
	accounts.Lock()
	defer accounts.Unlock()
	until, limited := accounts.limited[key]
	if !limited || !time.Now().Before(until) {
		return nil
	}
	return &accountProbeError{cause: "GitHub API rate limited; retrying", transient: true, until: until}
}

// rememberLimit records a transient answer so later callers make no request
// before the reset time, and returns the same error.
func rememberLimit(key string, err error) error {
	var cause *accountProbeError
	if !errors.As(err, &cause) || !cause.transient {
		return err
	}
	if cause.until.IsZero() {
		return err
	}
	accounts.Lock()
	accounts.limited[key] = cause.until
	now := time.Now()
	for older, until := range accounts.limited {
		if !now.Before(until) {
			delete(accounts.limited, older)
		}
	}
	accounts.Unlock()
	return err
}

// savedLogins lists the accounts the GitHub CLI saved for a host. Reading the
// list needs no desktop keyring, so a managed shell can still name its logins.
func savedLogins(ctx context.Context, cmd *exec.Cmd, host string) ([]string, error) {
	raw, _ := authOutput(ctx, cmd, "", "auth", "status", "--hostname", host, "--json", "hosts")
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	var status struct {
		Hosts map[string][]struct {
			Login string `json:"login"`
		} `json:"hosts"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &status) != nil {
		return nil, nil
	}
	logins := make([]string, 0, len(status.Hosts[host]))
	for _, account := range status.Hosts[host] {
		if account.Login != "" {
			logins = append(logins, account.Login)
		}
	}
	return logins, nil
}

func refreshAccount(ctx context.Context, cmd *exec.Cmd, r repository, key string, cached choice, write bool) (login string, err error) {
	config := ghConfigFingerprint(cmd)
	// Read the fallback token before the probe can exhaust its deadline.
	knownToken := ""
	if cached.login != "" {
		knownToken, _ = accountToken(ctx, cmd, r.host, cached.login)
	}
	defer func() {
		if errors.Is(err, context.DeadlineExceeded) {
			err = &accountProbeError{cause: "GitHub API account probe timed out; retrying", transient: true}
		}
		_ = rememberLimit(key, err)
		var cause *accountProbeError
		if !errors.As(err, &cause) || !cause.transient {
			return
		}
		if cached.login != "" && knownToken != "" {
			cacheAccount(key, cached.login, config)
			login, err = cached.login, nil
		}
	}()
	// gh can report a nonzero status when one of several saved accounts expired.
	// Ask auth token for each saved account that remains available.
	list, err := savedLogins(ctx, cmd, r.host)
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "", nil
	}
	// Recheck the last good account first, before considering another account.
	for i := range list {
		if list[i] == cached.login {
			list[0], list[i] = list[i], list[0]
			break
		}
	}
	for _, account := range list {
		token := knownToken
		if account != cached.login || token == "" {
			token, err = accountToken(ctx, cmd, r.host, account)
		}
		if err != nil || token == "" {
			continue
		}
		if account == cached.login {
			knownToken = token
		}
		denied, err := probeRepository(ctx, cmd, token, r, write)
		if err != nil {
			return "", err
		}
		if denied {
			if account == cached.login {
				cached = choice{}
				// GitHub denied this account, so remember nothing that could
				// bring it back during a later outage.
				forgetAccount(key)
				forgetChoice(key)
			}
			continue
		}
		proveAccount(key, account, config)
		return account, nil
	}
	if ctx.Err() != nil {
		return "", context.Cause(ctx)
	}
	operation := "read"
	if write {
		operation = "write"
	}
	return "", fmt.Errorf("%w can %s %s/%s", ErrNoSignedInAccount, operation, r.host, r.name)
}

// Account reports the cached account for the same repository command context.
func Account(ctx context.Context, executable, dir string, args ...string) string {
	cmd := Command(ctx, executable, dir, args...)
	r := repositoryFor(ctx, cmd)
	rec, err := roomFor(ctx, dir, r)
	if err != nil {
		return ""
	}
	key := accountProofKey(ctx, cmd, r, rec, Access(ctx, rec) == ReadWrite)
	config := ghConfigFingerprint(cmd)
	accounts.Lock()
	defer accounts.Unlock()
	cached := accounts.choices[key]
	if cached.config != config {
		return ""
	}
	return cached.login
}
