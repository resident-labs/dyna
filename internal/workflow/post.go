package workflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/dop251/goja"
)

// PostResult is what post() returns to a workflow script.
type PostResult struct {
	ID         string           `json:"id"`
	Deliveries []map[string]any `json:"deliveries"`
	Skipped    []map[string]any `json:"skipped,omitempty"`
}

// post implements post(to, text), where to is one @mention string or an
// array of them. Delivery is supplied by the embedding CLI so the engine does
// not import Dyna's routing service. The script posts as run:<run-id>.
func (e *engine) post(call goja.FunctionCall) goja.Value {
	vm := e.vm
	if len(call.Arguments) != 2 {
		panic(vm.NewGoError(fmt.Errorf("post(to, text) requires exactly two arguments")))
	}
	var mentions []string
	switch v := call.Argument(0).Export().(type) {
	case string:
		mentions = []string{strings.TrimSpace(v)}
	case []any:
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				panic(vm.NewGoError(fmt.Errorf("post mentions must be strings")))
			}
			mentions = append(mentions, strings.TrimSpace(s))
		}
	default:
		panic(vm.NewGoError(fmt.Errorf("post to must be an @mention string or an array of them")))
	}
	if len(mentions) == 0 {
		panic(vm.NewGoError(fmt.Errorf("post to must name at least one @mention")))
	}
	for _, m := range mentions {
		if !strings.HasPrefix(m, "@") || len(m) < 2 {
			panic(vm.NewGoError(fmt.Errorf("post mention %q must start with @", m)))
		}
	}
	text, ok := call.Argument(1).Export().(string)
	if !ok || strings.TrimSpace(text) == "" {
		panic(vm.NewGoError(fmt.Errorf("post text must be a non-empty string")))
	}
	if e.opts.Run == nil {
		panic(vm.NewGoError(fmt.Errorf("post() requires an active run")))
	}
	if e.opts.Poster == nil {
		panic(vm.NewGoError(fmt.Errorf("post() is unavailable in this execution")))
	}
	res, err := e.opts.Poster(context.Background(), e.opts.Run.Meta.ID, mentions, strings.TrimSpace(text))
	if err != nil && res.ID == "" {
		panic(vm.NewGoError(err))
	}
	out := map[string]any{"id": res.ID, "deliveries": res.Deliveries}
	if len(res.Skipped) > 0 {
		out["skipped"] = res.Skipped
	}
	if err != nil {
		out["error"] = err.Error()
	}
	return vm.ToValue(out)
}
