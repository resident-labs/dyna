package workflow

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/dop251/goja"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/textutil"
)

func (e *engine) bind(vm *goja.Runtime) error {
	e.vm = vm
	// console.Enable uses goja_nodejs internally, which also installs a module
	// loader. Console is the only intended public part of that integration.
	vm.Set("require", goja.Undefined())
	vm.Set("__phase", func(title string, planNodeID string) {
		e.phaseMu.Lock()
		e.curPhase = title
		e.phaseMu.Unlock()
		e.emit(runstore.Event{T: "phase", Title: title, PlanNodeID: planNodeID})
	})
	vm.Set("__log", func(msg string, planNodeID string) {
		e.emit(runstore.Event{
			T: "log", Msg: textutil.ClipOneLine(msg, 4000), PlanNodeID: planNodeID,
			Provenance: runstore.CanonicalLogProvenance(runstore.LogProvenanceWorkflow),
		})
	})
	vm.Set("__groupStart", func(id, kind string) {
		e.emit(runstore.Event{T: "group_start", PlanGroupID: id, Title: kind})
	})
	vm.Set("__groupEnd", func(id, kind string) {
		e.emit(runstore.Event{T: "group_end", PlanGroupID: id, Title: kind})
	})
	vm.Set("__planContext", func(id string, stage, item int) {
		e.planMu.Lock()
		defer e.planMu.Unlock()
		if id == "" || id == "null" || id == "undefined" {
			e.planIndexSet = false
			e.planGroupID = ""
			return
		}
		e.planGroupID = id
		e.planStage = stage
		e.planItem = item
		e.planIndexSet = true
	})
	vm.Set("__logSystem", func(msg string) {
		e.emitLog(runstore.LogProvenanceSystem, msg)
	})
	vm.Set("__finish", func(resultJSON string) {
		e.finish(outcome{resultJSON: resultJSON})
	})
	vm.Set("__fail", func(msg string) {
		e.finish(outcome{err: fmt.Errorf("workflow failed: %s", msg)})
	})
	vm.Set("__spawn", e.spawn)
	vm.Set("__workflow", e.workflow)
	vm.Set("__post", e.post)
	vm.Set("__worktrees", e.scriptWorktrees)
	vm.Set("__profileRemaining", func(name string) any {
		p, ok := e.opts.Store.Get(name)
		if !ok || p.MaxCallsPerRun <= 0 {
			return nil
		}
		e.shared.mu.Lock()
		remaining := p.MaxCallsPerRun - e.shared.profCalls[name]
		e.shared.mu.Unlock()
		if remaining < 0 {
			remaining = 0
		}
		return remaining
	})

	argsV := goja.Undefined()
	if e.opts.Args != nil {
		argsV = vm.ToValue(e.opts.Args)
	}
	vm.Set("args", argsV)

	process, err := newProcessShim(vm, e.opts.WorkDir)
	if err != nil {
		return fmt.Errorf("build process shim: %w", err)
	}
	vm.Set("process", process)

	// Expose the registry (read-only copy) so scripts can pick workers by
	// stats: profiles.find(p => p.taste >= 4) etc.
	profs := make([]any, 0, len(e.opts.Store.Profiles))
	for _, p := range e.opts.Store.Profiles {
		if !p.WorkflowEligible() {
			continue
		}
		fields := map[string]any{
			"name": p.Name, "description": p.Description, "harness": p.Harness,
			"model": p.Model, "models": p.EffectiveModels(), "taste": p.Taste, "intelligence": p.Intelligence,
			"cost": p.Cost, "diagnostics": p.EffectiveDiagnostics(), "default": p.Default,
			"uses":          p.EffectiveUses(),
			"maxConcurrent": p.MaxConcurrent, "maxCallsPerRun": p.MaxCallsPerRun,
		}
		obj := vm.NewObject()
		for name, value := range fields {
			if err := obj.Set(name, value); err != nil {
				return fmt.Errorf("bind profile %q field %q: %w", p.Name, name, err)
			}
		}
		profs = append(profs, obj)
	}
	vm.Set("profiles", vm.NewArray(profs...))

	_, err = vm.RunScript("prelude.js", prelude)
	return err
}

func (e *engine) scriptWorktrees() []map[string]any {
	e.shared.mu.Lock()
	defer e.shared.mu.Unlock()
	out := make([]map[string]any, 0, len(e.shared.worktrees))
	for _, rec := range e.shared.worktrees {
		out = append(out, map[string]any{
			"id": rec.AgentID, "label": rec.Label, "profile": rec.Profile,
			"path": rec.Path, "repo": rec.Repo, "base": rec.Base, "head": rec.Head,
			"retainedAt": rec.RetainedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return out
}

// newProcessShim supplies the intentionally narrow process compatibility
// surface. It is a snapshot rather than goja_nodejs/process because that
// module copies the complete host environment into the JavaScript runtime.
func newProcessShim(vm *goja.Runtime, workDir string) (*goja.Object, error) {
	env := vm.NewObject()
	for _, name := range processEnvAllowlist {
		value, ok := os.LookupEnv(name)
		if !ok {
			continue
		}
		if err := env.DefineDataProperty(name, vm.ToValue(value), goja.FLAG_FALSE, goja.FLAG_FALSE, goja.FLAG_TRUE); err != nil {
			return nil, fmt.Errorf("define process.env.%s: %w", name, err)
		}
	}
	if err := freezeObject(vm, env); err != nil {
		return nil, fmt.Errorf("freeze process.env: %w", err)
	}

	process := vm.NewObject()
	properties := []struct {
		name  string
		value goja.Value
	}{
		{name: "cwd", value: vm.ToValue(func() string { return workDir })},
		{name: "platform", value: vm.ToValue(nodePlatform())},
		{name: "arch", value: vm.ToValue(nodeArch())},
		{name: "env", value: env},
	}
	for _, property := range properties {
		if err := process.DefineDataProperty(property.name, property.value, goja.FLAG_FALSE, goja.FLAG_FALSE, goja.FLAG_TRUE); err != nil {
			return nil, fmt.Errorf("define process.%s: %w", property.name, err)
		}
	}
	if err := freezeObject(vm, process); err != nil {
		return nil, fmt.Errorf("freeze process: %w", err)
	}
	return process, nil
}

func freezeObject(vm *goja.Runtime, object *goja.Object) error {
	objectConstructor := vm.Get("Object").ToObject(vm)
	freeze, ok := goja.AssertFunction(objectConstructor.Get("freeze"))
	if !ok {
		return errors.New("Object.freeze is unavailable")
	}
	_, err := freeze(objectConstructor, object)
	return err
}

func nodePlatform() string {
	if runtime.GOOS == "windows" {
		return "win32"
	}
	return runtime.GOOS
}

func nodeArch() string {
	switch runtime.GOARCH {
	case "386":
		return "ia32"
	case "amd64":
		return "x64"
	default:
		return runtime.GOARCH
	}
}
