package store

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestLocalProxyKeyIsMadeOnceUntilReplaced(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if key, err := st.LocalProxyKey(ctx); key != "" || err != nil {
		t.Fatalf("before first use: %q %v", key, err)
	}
	if key, err := st.SetLocalProxyKey(ctx, "first", false); key != "first" || err != nil {
		t.Fatalf("first use: %q %v", key, err)
	}
	if key, err := st.SetLocalProxyKey(ctx, "second", false); key != "first" || err != nil {
		t.Fatalf("a second read replaced the key: %q %v", key, err)
	}
	if key, err := st.SetLocalProxyKey(ctx, "third", true); key != "third" || err != nil {
		t.Fatalf("reset: %q %v", key, err)
	}
	if key, err := st.LocalProxyKey(ctx); key != "third" || err != nil {
		t.Fatalf("after reset: %q %v", key, err)
	}
}

func TestLocalUsageTotalsByModel(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	at := time.UnixMilli(1000).UTC()
	for _, u := range []contract.ProxyUsage{
		{Model: "one", Requests: 1, InputTokens: 10, OutputTokens: 1, CacheReadTokens: 5, CacheWriteTokens: 2, LastAt: at},
		{Model: "two", Requests: 1, InputTokens: 3, LastAt: at.Add(time.Second)},
		{Model: "one", Requests: 1, InputTokens: 20, OutputTokens: 2, CacheReadTokens: 6, CacheWriteTokens: 3, LastAt: at.Add(2 * time.Second)},
	} {
		if err := st.AddLocalUsage(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.LocalUsage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := contract.ProxyUsage{Model: "one", Requests: 2, InputTokens: 30, OutputTokens: 3, CacheReadTokens: 11, CacheWriteTokens: 5, LastAt: at.Add(2 * time.Second)}
	if len(got) != 2 || got[0] != want || got[1].Model != "two" {
		t.Fatalf("usage = %+v", got)
	}
}
