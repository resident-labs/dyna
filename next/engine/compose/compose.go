// Package compose reads compose files. A compose file comes from a repository that agents can write, so at every level it
// is untrusted input: Parse lets through a small allowed subset and refuses everything else, and the file reaches a host
// only as the checked values in box.Stack.
package compose

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/template"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/distribution/reference"
	"gopkg.in/yaml.v3"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

const (
	// Default is the file a session uses when it names none.
	Default = "default"
	// AgentService is the service that is the agent's box.
	AgentService = "agent"
	// MaxFileBytes bounds a file, which a repository could otherwise make any size.
	MaxFileBytes = 64 << 10
	maxServices  = 16
	// maxHealthWait bounds how long one sidecar holds up a start.
	maxHealthWait = 2 * time.Minute
	imagesFolder  = ".dyna/images/"
)

var (
	fileName    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	serviceName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`) // it is also a hostname
	volumeName  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	imageFolder = regexp.MustCompile(`^[a-z0-9_.-]+$`)
	envName     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	mountPath   = regexp.MustCompile(`^/[A-Za-z0-9_./-]+$`)
)

// The allowed keys. Anything else in a file is refused, so a key added to Compose later is refused too.
var (
	fileKeys    = []string{"services", "volumes"}
	serviceKeys = []string{"build", "command", "cpus", "depends_on", "environment", "healthcheck", "image", "mem_limit", "pids_limit", "volumes"}
	agentKeys   = []string{"build", "depends_on", "environment", "volumes"}
	buildKeys   = []string{"context"}
	dependKeys  = []string{"condition"}
	healthKeys  = []string{"interval", "retries", "start_period", "test", "timeout"}
	mountKeys   = []string{"read_only", "source", "target", "type"}
)

// reservedEnv holds what Dyna itself sets in a box. A file may not change it.
var reservedEnv = []string{"CARGO_HOME", "GOCACHE", "GOMODCACHE", "HOME", "PI_CODING_AGENT_DIR", "npm_config_store_dir"}

// agentPaths are where Dyna mounts things in a box. A compose volume may not cover them.
var agentPaths = []string{"/box", "/opt/dyna", "/run/dyna"}

// Service is one checked service. Only Build or Image names its image; an agent with neither takes the repository's sole
// image, or dyna-base.
type Service struct {
	Name          string
	Image         string // a fully qualified reference
	Build         string // a folder under .dyna/images
	Command       []string
	Env           []box.EnvVar
	Volumes       []box.Volume
	Health        []string
	HealthTimeout time.Duration
	Wait          time.Duration
	CPUs          float64
	MemBytes      int64
	Pids          int
}

// File is a checked compose file.
type File struct {
	Agent Service
	// Sidecars are in start order, so whatever a service depends on comes before it.
	Sidecars []Service
}

// Builds lists the image folders the file builds from, each once.
func (f *File) Builds() []string {
	var folders []string
	for _, service := range append([]Service{f.Agent}, f.Sidecars...) {
		if service.Build != "" && !slices.Contains(folders, service.Build) {
			folders = append(folders, service.Build)
		}
	}
	return folders
}

func refuse(format string, args ...any) error {
	return box.StackError{Reason: fmt.Sprintf(format, args...)}
}

// ValidName reports whether name can name a compose file. It keeps a name that came from a caller inside its folder.
func ValidName(name string) bool { return fileName.MatchString(name) }

// Reserved reports whether Dyna sets this variable in a box itself.
func Reserved(env string) bool { return slices.Contains(reservedEnv, env) }

// Parse checks a compose file. Every error is a box.StackError that says what to change.
func Parse(data []byte) (*File, error) {
	if len(data) > MaxFileBytes {
		return nil, refuse("the file is over %d bytes", MaxFileBytes)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var raw map[string]any
	if err := decoder.Decode(&raw); err != nil {
		return nil, refuse("not valid YAML: %v", err)
	}
	// The loader reads every document of a file and merges them, so a second one would pass the keys unchecked.
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, refuse("a file holds one YAML document")
	}
	// The keys go first, so the loader never sees include, extends, env_file, secrets or configs, which read host files.
	if err := checkKeys(raw); err != nil {
		return nil, err
	}
	if vars := slices.Sorted(maps.Keys(template.ExtractVariables(raw, template.DefaultPattern))); len(vars) > 0 {
		return nil, refuse("variable %s: a compose file has no environment to read from; write the value, or $$ for a dollar sign", vars[0])
	}
	project, err := loader.LoadWithContext(context.Background(), types.ConfigDetails{
		WorkingDir:  "/",
		ConfigFiles: []types.ConfigFile{{Filename: "compose.yaml", Content: data}},
		Environment: types.Mapping{}, // nothing of the engine's can reach a value
	}, func(o *loader.Options) {
		o.SetProjectName("dyna", true)
		o.SkipInclude, o.SkipExtends, o.SkipResolveEnvironment = true, true, true
		o.ResolvePaths = false
		o.SkipConsistencyCheck = true // it wants an image for every service, and an agent may have none; checkService covers the rest
	})
	if err != nil {
		return nil, refuse("%v", err)
	}
	if len(project.Services) > maxServices {
		return nil, refuse("%d services; a file may have %d", len(project.Services), maxServices)
	}
	for name := range project.Volumes {
		if !volumeName.MatchString(name) {
			return nil, refuse("volume %q: use lowercase letters, digits, hyphens and underscores", name)
		}
	}
	if _, ok := project.Services[AgentService]; !ok {
		return nil, refuse("no service named %s, which is the agent's box", AgentService)
	}
	services := make(map[string]Service, len(project.Services))
	for _, name := range slices.Sorted(maps.Keys(project.Services)) {
		service, err := checkService(name, project.Services[name], project.Volumes)
		if err != nil {
			return nil, err
		}
		services[name] = service
	}
	sidecars, err := startOrder(project, services)
	if err != nil {
		return nil, err
	}
	return &File{Agent: services[AgentService], Sidecars: sidecars}, nil
}

func keysIn(raw map[string]any, allowed []string, where string) error {
	for _, key := range slices.Sorted(maps.Keys(raw)) {
		if !slices.Contains(allowed, key) {
			return refuse("%s: %s is not allowed; this level allows %s", where, key, strings.Join(allowed, ", "))
		}
	}
	return nil
}

func checkKeys(raw map[string]any) error {
	if err := keysIn(raw, fileKeys, "file"); err != nil {
		return err
	}
	if volumes, ok := raw["volumes"]; ok && volumes != nil {
		declared, ok := volumes.(map[string]any)
		if !ok {
			return refuse("volumes: must map names to nothing")
		}
		for name, settings := range declared {
			// A driver option can bind any host path, and external or name can reach another box's volume.
			if empty, ok := settings.(map[string]any); settings != nil && (!ok || len(empty) > 0) {
				return refuse("volume %s: settings are not allowed; a volume is only a name", name)
			}
		}
	}
	services, ok := raw["services"].(map[string]any)
	if !ok {
		return refuse("services: must map names to services")
	}
	for _, name := range slices.Sorted(maps.Keys(services)) {
		service, ok := services[name].(map[string]any)
		if !ok {
			return refuse("service %s: must be a mapping", name)
		}
		allowed := serviceKeys
		if name == AgentService {
			// The agent's image, command and limits are the box's own.
			allowed = agentKeys
		}
		where := "service " + name
		if err := keysIn(service, allowed, where); err != nil {
			return err
		}
		if err := checkNested(service, where); err != nil {
			return err
		}
	}
	return nil
}

func checkNested(service map[string]any, where string) error {
	if build, ok := service["build"].(map[string]any); ok {
		if err := keysIn(build, buildKeys, where+" build"); err != nil {
			return err
		}
	}
	if health, ok := service["healthcheck"].(map[string]any); ok {
		if err := keysIn(health, healthKeys, where+" healthcheck"); err != nil {
			return err
		}
	}
	if depends, ok := service["depends_on"].(map[string]any); ok {
		for name, settings := range depends {
			if setting, ok := settings.(map[string]any); ok {
				if err := keysIn(setting, dependKeys, where+" depends_on "+name); err != nil {
					return err
				}
			}
		}
	}
	if mounts, ok := service["volumes"].([]any); ok {
		for _, mount := range mounts {
			switch mount := mount.(type) {
			case map[string]any:
				if err := keysIn(mount, mountKeys, where+" volume"); err != nil {
					return err
				}
			case string:
				if parts := strings.Split(mount, ":"); len(parts) > 3 || len(parts) == 3 && parts[2] != "ro" && parts[2] != "rw" {
					return refuse("%s: volume %q: the only option is ro or rw", where, mount)
				}
			}
		}
	}
	return nil
}

func checkService(name string, s types.ServiceConfig, declared types.Volumes) (Service, error) {
	where := "service " + name
	if !serviceName.MatchString(name) {
		return Service{}, refuse("%s: use lowercase letters, digits and hyphens", where)
	}
	out := Service{Name: name, Command: []string(s.Command), Pids: int(s.PidsLimit), CPUs: float64(s.CPUS), MemBytes: int64(s.MemLimit)}
	if s.PidsLimit < 0 || s.MemLimit < 0 || s.CPUS < 0 {
		return Service{}, refuse("%s: a limit cannot be negative", where)
	}
	for _, word := range out.Command {
		if strings.ContainsRune(word, 0) {
			return Service{}, refuse("%s: command holds a NUL", where)
		}
	}
	var err error
	if out.Image, err = checkImage(s.Image); err != nil {
		return Service{}, refuse("%s: %v", where, err)
	}
	if s.Build != nil {
		if out.Build, err = checkBuild(s.Build.Context); err != nil {
			return Service{}, refuse("%s: %v", where, err)
		}
	}
	if name != AgentService && (out.Image == "") == (out.Build == "") {
		return Service{}, refuse("%s: give either image or build", where)
	}
	if out.Env, err = checkEnv(name, s.Environment); err != nil {
		return Service{}, refuse("%s: %v", where, err)
	}
	if out.Volumes, err = checkVolumes(name, s.Volumes, declared); err != nil {
		return Service{}, refuse("%s: %v", where, err)
	}
	if s.HealthCheck != nil {
		if err := checkHealth(&out, s.HealthCheck); err != nil {
			return Service{}, refuse("%s: %v", where, err)
		}
	}
	return out, nil
}

func checkImage(image string) (string, error) {
	if image == "" {
		return "", nil
	}
	ref, err := reference.ParseDockerRef(image)
	if err != nil {
		return "", fmt.Errorf("image %q: %w", image, err)
	}
	// Local names reach every other repository's images on the host.
	if domain := reference.Domain(ref); domain == "localhost" || strings.HasPrefix(domain, "localhost:") {
		return "", fmt.Errorf("image %q: only a registry image is allowed; a local image belongs in .dyna/images and a build", image)
	}
	return ref.String(), nil
}

func checkBuild(context string) (string, error) {
	folder, ok := strings.CutPrefix(path.Clean(context), imagesFolder)
	if !ok || !imageFolder.MatchString(folder) || folder == "." || folder == ".." {
		return "", fmt.Errorf("build %q: build must be a folder in %s", context, imagesFolder)
	}
	return folder, nil
}

func checkEnv(service string, env types.MappingWithEquals) ([]box.EnvVar, error) {
	var out []box.EnvVar
	for _, name := range slices.Sorted(maps.Keys(env)) {
		value := env[name]
		switch {
		case !envName.MatchString(name):
			return nil, fmt.Errorf("environment: %q is not a variable name", name)
		case value == nil:
			return nil, fmt.Errorf("environment: %s has no value, which would pass the host's through", name)
		case strings.ContainsRune(*value, 0):
			return nil, fmt.Errorf("environment: %s holds a NUL", name)
		case service == AgentService && Reserved(name):
			return nil, fmt.Errorf("environment: %s is set by Dyna", name)
		}
		out = append(out, box.EnvVar{Name: name, Value: *value})
	}
	return out, nil
}

func checkVolumes(service string, mounts []types.ServiceVolumeConfig, declared types.Volumes) ([]box.Volume, error) {
	var out []box.Volume
	for _, mount := range mounts {
		if mount.Type != types.VolumeTypeVolume {
			return nil, fmt.Errorf("volumes: %s is not a named volume", mount.String())
		}
		if _, ok := declared[mount.Source]; !ok || mount.Source == "" {
			return nil, fmt.Errorf("volumes: %q is not a volume this file declares", mount.Source)
		}
		target := mount.Target
		if !mountPath.MatchString(target) || path.Clean(target) != target {
			return nil, fmt.Errorf("volumes: %q is not a plain absolute path", target)
		}
		if service == AgentService {
			for _, kept := range agentPaths {
				if target == kept || strings.HasPrefix(target, kept+"/") || strings.HasPrefix(kept, target+"/") {
					return nil, fmt.Errorf("volumes: %s would cover %s, which is Dyna's", target, kept)
				}
			}
		}
		out = append(out, box.Volume{Name: mount.Source, Target: target, ReadOnly: mount.ReadOnly})
	}
	return out, nil
}

func checkHealth(out *Service, h *types.HealthCheckConfig) error {
	if len(h.Test) < 2 || h.Test[0] != "CMD" && h.Test[0] != "CMD-SHELL" {
		return fmt.Errorf("healthcheck: test must be a command")
	}
	for _, word := range h.Test {
		if strings.ContainsRune(word, 0) {
			return fmt.Errorf("healthcheck: test holds a NUL")
		}
	}
	interval, retries := 30*time.Second, uint64(3) // Compose's defaults
	if h.Interval != nil {
		interval = time.Duration(*h.Interval)
	}
	if h.Retries != nil {
		retries = *h.Retries
	}
	wait := interval * time.Duration(min(retries, 1<<16))
	if h.StartPeriod != nil {
		wait += time.Duration(*h.StartPeriod)
	}
	out.Health = []string(h.Test)
	out.Wait = max(time.Second, min(wait, maxHealthWait))
	if h.Timeout != nil {
		out.HealthTimeout = time.Duration(*h.Timeout)
	}
	return nil
}

// startOrder sorts the sidecars so that each comes after what it depends on. Names break ties, so a file always gives one order.
func startOrder(project *types.Project, services map[string]Service) ([]Service, error) {
	const visiting, done = 1, 2
	state := make(map[string]int)
	var order []Service
	var visit func(string) error
	visit = func(name string) error {
		switch state[name] {
		case done:
			return nil
		case visiting:
			return refuse("service %s: depends_on forms a cycle", name)
		}
		state[name] = visiting
		depends := project.Services[name].DependsOn
		for _, dep := range slices.Sorted(maps.Keys(depends)) {
			switch _, known := project.Services[dep]; {
			case !known:
				return refuse("service %s: depends_on names %s, which is not a service here", name, dep)
			case dep == AgentService:
				return refuse("service %s: nothing can depend on %s, which starts last", name, AgentService)
			case depends[dep].Condition == types.ServiceConditionHealthy && len(services[dep].Health) == 0:
				return refuse("service %s: %s has no healthcheck to wait for", name, dep)
			case depends[dep].Condition != types.ServiceConditionStarted && depends[dep].Condition != types.ServiceConditionHealthy:
				return refuse("service %s: depends_on allows service_started and service_healthy", name)
			}
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[name] = done
		if name != AgentService {
			order = append(order, services[name])
		}
		return nil
	}
	for _, name := range slices.Sorted(maps.Keys(project.Services)) {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// CheckLimits refuses a service limit above the session's. Nothing here is needed to hold the limit: every container of a
// box runs in its slice, so together they stay under it whatever a file says.
func (f *File) CheckLimits(size contract.BoxSize) error {
	for _, s := range f.Sidecars {
		switch {
		case s.CPUs > float64(size.CPUs):
			return refuse("service %s: cpus %v is over the session's %d", s.Name, s.CPUs, size.CPUs)
		case s.MemBytes > size.MemBytes:
			return refuse("service %s: mem_limit %d is over the session's %d", s.Name, s.MemBytes, size.MemBytes)
		case s.Pids > size.Pids:
			return refuse("service %s: pids_limit %d is over the session's %d", s.Name, s.Pids, size.Pids)
		}
	}
	return nil
}

// Stack is what a box adds from this file, once a host has each sidecar's image. images maps service names to them, and
// infra is the image that holds the pod's network.
func (f *File) Stack(infra string, images map[string]string) *box.Stack {
	stack := &box.Stack{Env: f.Agent.Env, Volumes: f.Agent.Volumes, Infra: infra, Names: []string{AgentService}}
	for _, s := range f.Sidecars {
		stack.Names = append(stack.Names, s.Name)
		stack.Sidecars = append(stack.Sidecars, box.Sidecar{Name: s.Name, Image: images[s.Name], Command: s.Command, Env: s.Env, Volumes: s.Volumes,
			Health: s.Health, HealthTimeout: s.HealthTimeout, Wait: s.Wait, CPUs: s.CPUs, MemBytes: s.MemBytes, Pids: s.Pids})
	}
	encoded, err := json.Marshal(stack)
	if err != nil {
		panic(err) // it holds only strings, numbers and slices
	}
	sum := sha256.Sum256(encoded)
	stack.Hash = hex.EncodeToString(sum[:])
	return stack
}
