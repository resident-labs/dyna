// Package workflowcatalog lists and saves workflow sources without executing them.
package workflowcatalog

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"dyna-agent/internal/plan"
	"dyna-agent/internal/workflowresolve"
	"dyna-agent/internal/workflows/bundled"
)

func List(workDir, personalDir string) ([]Info, error) {
	out := []Info{}
	add := func(name string, scope Scope, source []byte) {
		meta, _ := plan.ReadMetadata(string(source))
		args := append([]plan.WorkflowArg{}, meta.Args...)
		out = append(out, Info{Name: name, Scope: scope, Description: meta.Description, Args: args})
	}
	for _, entry := range []struct {
		dir   string
		scope Scope
	}{
		{filepath.Join(workDir, ".dyna", "workflows"), ScopeProject},
		{workflowresolve.PersonalDir(personalDir), ScopePersonal},
	} {
		files, err := os.ReadDir(entry.dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".js") {
				continue
			}
			name := strings.TrimSuffix(file.Name(), ".js")
			if !workflowresolve.ValidName(name) {
				continue
			}
			b, err := os.ReadFile(filepath.Join(entry.dir, file.Name()))
			if err != nil {
				return nil, err
			}
			add(name, entry.scope, b)
		}
	}
	files, err := fs.ReadDir(bundled.FS, ".")
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".js") {
			continue
		}
		name := strings.TrimSuffix(file.Name(), ".js")
		if !workflowresolve.ValidName(name) {
			continue
		}
		b, err := bundled.FS.ReadFile(file.Name())
		if err != nil {
			return nil, err
		}
		add(name, ScopeBundled, b)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return false
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func Save(name, source, workDir, personalDir string, project, overwrite bool) error {
	if !workflowresolve.ValidName(name) {
		return fmt.Errorf("workflow name must use only lowercase letters, digits and hyphens")
	}
	dir := workflowresolve.PersonalDir(personalDir)
	if project {
		dir = filepath.Join(workDir, ".dyna", "workflows")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	flags := os.O_WRONLY | os.O_CREATE
	if overwrite {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}
	f, err := os.OpenFile(filepath.Join(dir, name+".js"), flags, 0644)
	if err != nil {
		return err
	}
	_, writeErr := f.WriteString(source)
	return errors.Join(writeErr, f.Close())
}
