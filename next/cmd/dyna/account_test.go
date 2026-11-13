package main

import (
	"context"
	"github.com/resident-labs/dyna/contract"
	"github.com/spf13/cobra"
	"io"
	"net/http"
	"strings"
	"testing"
)

func (f *fakeClient) AccountsList(context.Context, contract.AccountsListRequest) (contract.AccountsListResponse, error) {
	return contract.AccountsListResponse{}, nil
}
func (f *fakeClient) AccountsQuota(context.Context, contract.AccountsQuotaRequest) (contract.AccountsQuotaResponse, error) {
	return contract.AccountsQuotaResponse{}, nil
}
func (f *fakeClient) AccountsSetLimits(context.Context, contract.AccountsSetLimitsRequest) (contract.AccountsSetLimitsResponse, error) {
	return contract.AccountsSetLimitsResponse{}, nil
}
func (f *fakeClient) SessionSetAccount(context.Context, contract.SessionSetAccountRequest) (contract.SessionSetAccountResponse, error) {
	return contract.SessionSetAccountResponse{}, nil
}
func (f *fakeClient) AddonSetEnabled(context.Context, contract.AddonSetEnabledRequest) (contract.AddonSetEnabledResponse, error) {
	return contract.AddonSetEnabledResponse{}, nil
}
func (f *fakeClient) AddonSetRootOnly(context.Context, contract.AddonSetRootOnlyRequest) (contract.AddonSetRootOnlyResponse, error) {
	return contract.AddonSetRootOnlyResponse{}, nil
}
func TestGuidanceBindings(t *testing.T) {
	r := newRootCmd()
	for _, words := range [][]string{{"account", "list"}, {"account", "quota"}, {"account", "limit"}, {"session", "set-account"}, {"addon", "set"}} {
		c, rest, err := r.Find(words)
		if err != nil || len(rest) > 0 || c == r {
			t.Fatalf("%v: %v %v", words, rest, err)
		}
	}
}
func TestSocketEnvironment(t *testing.T) {
	t.Setenv("DYNA_SOCKET", "/tmp/owner-test.sock")
	if defaultSocket() != "/tmp/owner-test.sock" {
		t.Fatal(defaultSocket())
	}
}

type guidanceTransport func(*http.Request) (*http.Response, error)

func (f guidanceTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestAccountAndAddonRequests(t *testing.T) {
	var calls []string
	c := &socketClient{http: &http.Client{Transport: guidanceTransport(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, r.URL.Path+" "+string(b))
		body := `{}`
		if r.URL.Path == "/v1/accounts.list" {
			body = `{"accounts":[{"id":"acc","label":"primary","rooms":["r"],"preferred":["r"]}]}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	for _, tc := range []struct {
		cmd  *cobra.Command
		args []string
		want string
	}{
		{accountCommand(func() client { return c }), []string{"list"}, "/v1/accounts.list {}"},
		{accountCommand(func() client { return c }), []string{"quota"}, "/v1/accounts.quota {}"},
		{accountCommand(func() client { return c }), []string{"limit", "primary", "--rooms", "all", "--prefer", "none"}, `/v1/accounts.set_limits {"accountId":"acc","rooms":null,"preferred":null}`},
		{setAccountCommand(func() client { return c }), []string{"ses", "primary"}, `/v1/session.set_account {"session":"ses","account":"acc"}`},
		{setAccountCommand(func() client { return c }), []string{"ses", "auto"}, `/v1/session.set_account {"session":"ses"}`},
		{addonCommand(func() client { return c }), []string{"set", "skill", "test", "--room", "r", "--enabled", "off", "--root-only", "on"}, `/v1/addon.set_root_only {"kind":"skill","name":"test","room":"r","rootOnly":true}`},
	} {
		tc.cmd.SetArgs(tc.args)
		if err := tc.cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if got := calls[len(calls)-1]; got != tc.want {
			t.Fatalf("got %s want %s", got, tc.want)
		}
	}
}
