package native

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"dyna-agent/internal/account/store"
)

const agyClientID = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
const agyTokenURL = "https://oauth2.googleapis.com/token"
const agyCloudURL = "https://cloudcode-pa.googleapis.com/v1internal:"

func savedAgy(c Captured) (map[string]json.RawMessage, map[string]json.RawMessage, Captured, error) {
	if c.Credential.Schema != agyAuthSchema || len(c.Credential.Payload) > agyMaxAuth {
		return nil, nil, Captured{}, ErrSchema
	}
	cli, _, err := agyPayload(c.Credential.Payload)
	if err != nil {
		return nil, nil, Captured{}, err
	}
	validated, err := agyAccount(cli)
	if err != nil {
		return nil, nil, Captured{}, err
	}
	if c.Identity != validated.Identity {
		return nil, nil, Captured{}, ErrNoIdentity
	}
	var wrapper, token map[string]json.RawMessage
	if json.Unmarshal(cli, &wrapper) != nil || json.Unmarshal(wrapper["token"], &token) != nil {
		return nil, nil, Captured{}, ErrSchema
	}
	return wrapper, token, validated, nil
}
func (m *antigravityManager) CachedPlan(c Captured) *store.Plan {
	wrapper, _, _, err := savedAgy(c)
	if err != nil {
		return nil
	}
	tier := jsonString(wrapper["user_tier"])
	if tier == "" {
		return nil
	}
	return &store.Plan{ID: tier, Label: tier}
}

// errAgyUsageUnavailable is the usage of an ACP-only account: Antigravity's
// quota API takes the agy CLI token, which such an account does not have.
var errAgyUsageUnavailable = errors.New("Usage is not available for an Antigravity account signed in through ACP.")

func (m *antigravityManager) ProbeSaved(ctx context.Context, c Captured) (store.Observation, error) {
	var o store.Observation
	if err := ctx.Err(); err != nil {
		return o, err
	}
	if c.Credential.Schema == agyACPSchema {
		return o, errAgyUsageUnavailable
	}
	wrapper, token, validated, err := savedAgy(c)
	if err != nil {
		return o, err
	}
	now := directNow(m.now)
	if !validated.ExpiresAt.IsZero() && !validated.ExpiresAt.After(now.Add(time.Minute)) {
		return o, ErrInvalidGrant
	}
	headers := map[string]string{"Authorization": "Bearer " + jsonString(token["access_token"]), "Content-Type": "application/json", "User-Agent": "antigravity"}
	request := map[string]any{"metadata": map[string]string{"ideType": "ANTIGRAVITY", "platform": "LINUX_AMD64", "pluginType": "GEMINI"}}
	if project := jsonString(wrapper["project_id"]); project != "" {
		request["cloudaicompanionProject"] = project
	}
	body, _ := json.Marshal(request)
	raw, err := directRequest(ctx, m.client, http.MethodPost, agyCloudURL+"loadCodeAssist", string(body), headers, now)
	if err != nil {
		return o, err
	}
	var loaded struct {
		Project     string `json:"cloudaicompanionProject"`
		CurrentTier *struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"currentTier"`
		PaidTier *struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"paidTier"`
	}
	if json.Unmarshal(raw, &loaded) != nil || loaded.Project == "" {
		return o, ErrSchema
	}
	plan := m.CachedPlan(c)
	tier := loaded.PaidTier
	if tier == nil || tier.ID == "" {
		tier = loaded.CurrentTier
	}
	if tier != nil && tier.ID != "" {
		label := tier.Name
		if label == "" {
			label = tier.ID
		}
		plan = &store.Plan{ID: tier.ID, Label: label}
	}
	body, _ = json.Marshal(map[string]string{"project": loaded.Project})
	raw, err = directRequest(ctx, m.client, http.MethodPost, agyCloudURL+"retrieveUserQuotaSummary", string(body), headers, now)
	if err != nil {
		return o, err
	}
	type bucket struct {
		ID        string   `json:"bucketId"`
		Name      string   `json:"displayName"`
		Window    string   `json:"window"`
		Remaining *float64 `json:"remainingFraction"`
		Reset     string   `json:"resetTime"`
		Disabled  bool     `json:"disabled"`
	}
	var summary struct {
		Groups []struct {
			Name    string   `json:"displayName"`
			Buckets []bucket `json:"buckets"`
		} `json:"groups"`
		Buckets []bucket `json:"buckets"`
	}
	if json.Unmarshal(raw, &summary) != nil {
		return o, ErrSchema
	}
	windows := []store.Window{}
	seen := map[string]bool{}
	appendBuckets := func(group string, buckets []bucket) {
		for _, b := range buckets {
			if b.Disabled || b.ID == "" || seen[b.ID] || b.Remaining == nil || math.IsNaN(*b.Remaining) || math.IsInf(*b.Remaining, 0) || *b.Remaining < 0 || *b.Remaining > 1 {
				continue
			}
			used := (1 - *b.Remaining) * 100
			label := strings.TrimSpace(b.Name)
			// The window carries used percent, so a provider bucket name ending in
			// "Remaining" would read backwards: "Weekly Limit Remaining 0%"
			// looks exhausted exactly when the account is untouched.
			if label = strings.TrimSpace(strings.TrimSuffix(label, "Remaining")); label == "" {
				label = b.ID
			}
			if group != "" {
				label = group + ": " + label
			}
			if b.Window != "" {
				label += " (" + b.Window + ")"
			}
			reset := ""
			if t, err := time.Parse(time.RFC3339Nano, b.Reset); err == nil {
				reset = t.UTC().Format(time.RFC3339)
			}
			windows = append(windows, store.Window{ID: b.ID, Label: label, UsedPercent: &used, Unit: "percent", ResetsAt: reset})
			seen[b.ID] = true
		}
	}
	for _, g := range summary.Groups {
		appendBuckets(g.Name, g.Buckets)
	}
	appendBuckets("", summary.Buckets)
	if len(windows) == 0 {
		return o, ErrSchema
	}
	return store.Observation{Plan: plan, Windows: windows, ObservedAt: now}, nil
}

// Refresh changes only the returned saved payload. The service checkpoints it
// under the account-store lock before probing. An ACP-only account has nothing
// to refresh: the ACP server refreshes its own grant when it starts.
func (m *antigravityManager) Refresh(ctx context.Context, c Captured) (Captured, error) {
	if err := ctx.Err(); err != nil {
		return c, err
	}
	if c.Credential.Schema == agyACPSchema {
		_, a, err := agyACPUnpack(c.Credential)
		if err == nil && a.Identity != c.Identity {
			err = ErrNoIdentity
		}
		return c, err
	}
	wrapper, token, validated, err := savedAgy(c)
	if err != nil {
		return c, err
	}
	now := directNow(m.now)
	if !validated.ExpiresAt.IsZero() && validated.ExpiresAt.After(now.Add(5*time.Minute)) {
		return c, nil
	}
	refresh := jsonString(token["refresh_token"])
	if refresh == "" {
		return c, ErrInvalidGrant
	}
	secretFn := m.clientSecret
	if secretFn == nil {
		secretFn = m.oauthClientSecret
	}
	secret, err := secretFn(ctx)
	if err != nil {
		return c, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	body := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {agyClientID}, "client_secret": {secret}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, agyTokenURL, strings.NewReader(body.Encode()))
	if err != nil {
		return c, ErrUnsupported
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := m.client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return c, ctx.Err()
		}
		return c, errors.New("Antigravity sign-in could not be refreshed.")
	}
	defer resp.Body.Close()
	if err := directStatusError(resp, now); err != nil {
		return c, err
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return c, ErrSchema
	}
	var out struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		IDToken string `json:"id_token"`
		Type    string `json:"token_type"`
		Expires int64  `json:"expires_in"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return c, ErrSchema
	}
	if out.Error == "invalid_grant" || resp.StatusCode == http.StatusUnauthorized {
		return c, ErrInvalidGrant
	}
	if resp.StatusCode != http.StatusOK || out.Access == "" || out.Expires <= 0 || out.Expires > 10*365*86400 {
		return c, errors.New("Antigravity sign-in could not be refreshed.")
	}
	set := func(obj map[string]json.RawMessage, key string, value any) { obj[key], _ = json.Marshal(value) }
	set(token, "access_token", out.Access)
	if out.Refresh != "" {
		set(token, "refresh_token", out.Refresh)
	}
	expiry := now.Add(time.Duration(out.Expires) * time.Second)
	set(token, "expiry", expiry.Format(time.RFC3339Nano))
	if out.Type != "" {
		set(token, "token_type", out.Type)
	}
	if out.IDToken != "" {
		set(wrapper, "id_token", out.IDToken)
	}
	wrapper["token"], _ = json.Marshal(token)
	nextRaw, _ := json.Marshal(wrapper)
	next, err := agyAccount(nextRaw)
	if err != nil {
		return c, err
	}
	if next.Identity != c.Identity {
		return c, ErrNoIdentity
	}
	// The ACP server owns its grant (see antigravity_acp.go), so the saved copy rides along.
	_, acp, _ := agyPayload(c.Credential.Payload)
	c.Credential.Payload = agyPack(nextRaw, acp)
	c.ExpiresAt = expiry
	return c, nil
}

var agyClientSecretPattern = regexp.MustCompile(`GOCSPX-[A-Za-z0-9_-]{28}`)

// The inspected agy build embeds three Google installed-app secrets. Its
// Antigravity client uses the second unique secret in lexical order. Require
// that exact layout and client ID; reject unknown layouts rather than guess.
// Scan in bounded chunks. Secret values never enter errors or logs.
func (m *antigravityManager) oauthClientSecret(ctx context.Context) (string, error) {
	path := m.host.Executable
	if path == "" {
		path = "agy"
	}
	path, err := exec.LookPath(path)
	if err != nil {
		return "", ErrUnsupported
	}
	f, err := os.Open(path)
	if err != nil {
		return "", ErrUnsupported
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > 512<<20 {
		return "", ErrUnsupported
	}
	secrets := map[string]bool{}
	foundID := false
	tail := []byte{}
	buffer := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := f.Read(buffer)
		chunk := append(tail, buffer[:n]...)
		if bytes.Contains(chunk, []byte(agyClientID)) {
			foundID = true
		}
		for _, s := range agyClientSecretPattern.FindAll(chunk, -1) {
			secrets[string(s)] = true
			if len(secrets) > 3 {
				return "", ErrUnsupported
			}
		}
		if len(chunk) > 256 {
			tail = append([]byte(nil), chunk[len(chunk)-256:]...)
		} else {
			tail = append([]byte(nil), chunk...)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", ErrUnsupported
		}
	}
	if !foundID || len(secrets) != 3 {
		return "", ErrUnsupported
	}
	values := make([]string, 0, 3)
	for s := range secrets {
		values = append(values, s)
	}
	sort.Strings(values)
	return values[1], nil
}
