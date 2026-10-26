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

// postBody reads the message from post(to, text, data?) or post(to, {text,
// data}). data is a script object or array, returned as indented JSON that
// keeps the script's key order. It is empty when the post carries no data.
func (e *engine) postBody(call goja.FunctionCall) (text, data string) {
	vm := e.vm
	arg, extra := call.Argument(1), call.Argument(2)
	if obj, ok := arg.(*goja.Object); ok && len(call.Arguments) == 2 {
		t, ok := obj.Get("text").Export().(string)
		if !ok {
			panic(vm.NewGoError(fmt.Errorf("post text must be a non-empty string")))
		}
		text, extra = t, obj.Get("data")
	} else if t, ok := arg.Export().(string); ok {
		text = t
	} else {
		panic(vm.NewGoError(fmt.Errorf("post text must be a non-empty string")))
	}
	if extra == nil || goja.IsUndefined(extra) || goja.IsNull(extra) {
		return text, ""
	}
	if _, ok := extra.(*goja.Object); !ok {
		panic(vm.NewGoError(fmt.Errorf("post data must be an object or an array")))
	}
	stringify, ok := goja.AssertFunction(vm.Get("JSON").ToObject(vm).Get("stringify"))
	if !ok {
		panic(vm.NewGoError(fmt.Errorf("post data cannot be encoded")))
	}
	out, err := stringify(goja.Undefined(), extra, goja.Null(), vm.ToValue(2))
	if err != nil || goja.IsUndefined(out) {
		panic(vm.NewGoError(fmt.Errorf("post data must be JSON-serializable")))
	}
	return text, out.String()
}

// withData appends data to the message as a fenced JSON block. The feed shows
// that block as a collapsed key and value view; an agent reads it as text.
func withData(text, data string) string {
	if data == "" {
		return text
	}
	return text + "\n\n```json\n" + data + "\n```"
}

// post implements post(to, text, data?) and post(to, {text, data}), where to
// is one @mention string or an array of them. Delivery is supplied by the
// embedding CLI so the engine does not import Dyna's routing service. The
// script posts as run:<run-id>.
func (e *engine) post(call goja.FunctionCall) goja.Value {
	vm := e.vm
	if len(call.Arguments) < 2 || len(call.Arguments) > 3 {
		panic(vm.NewGoError(fmt.Errorf("post(to, text, data?) or post(to, {text, data}) requires two or three arguments")))
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
	text, data := e.postBody(call)
	if strings.TrimSpace(text) == "" {
		panic(vm.NewGoError(fmt.Errorf("post text must be a non-empty string")))
	}
	text = withData(strings.TrimSpace(text), data)
	if e.opts.Run == nil {
		panic(vm.NewGoError(fmt.Errorf("post() requires an active run")))
	}
	if e.opts.Poster == nil {
		panic(vm.NewGoError(fmt.Errorf("post() is unavailable in this execution")))
	}
	res, err := e.opts.Poster(context.Background(), e.opts.Run.Meta.ID, mentions, text)
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
