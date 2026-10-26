package localci

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"

	"dyna-agent/internal/repoid"
	"github.com/goccy/go-yaml"
)

const (
	// DefinitionPath is the repository file that describes local CI. Dyna reads
	// it from a commit, never from a working tree that can differ from that commit.
	DefinitionPath = ".dyna/local-ci.yaml"
	// MaxDefinitionBytes bounds a definition before Dyna decodes it.
	MaxDefinitionBytes = 1 << 20
)

// ErrDefinitionNotFound means local CI is not configured for the repository at
// the requested commit. It is not a check failure or an infrastructure error.
var ErrDefinitionNotFound = errors.New("repository has no " + DefinitionPath)

// ReportMode says which test evidence a command must leave in its report
// directory. Every check also writes the generic execution timing file.
type ReportMode string

const (
	// ReportNone requires no test-specific report.
	ReportNone ReportMode = "none"
	// ReportJUnit requires at least one completed test in a JUnit XML report.
	ReportJUnit ReportMode = "junit"
	// ReportGo reads JUnit XML and Go's JSON test stream for failure details.
	ReportGo ReportMode = "go"
)

// CacheDefinition binds a managed cache to a container target and, optionally,
// an environment variable. A host check receives the host path through Env.
type CacheDefinition struct {
	Name   string
	Target string
	Env    string
}

// ExecutableCheck is one command from a repository definition. Check carries
// its stable identity and the aggregate it feeds in the selected suite.
type ExecutableCheck struct {
	Check Check
	// Job, when set, makes the check run one GitHub Actions job through act
	// instead of running Command itself. Command, Image, Host, HostUser, Setup
	// and Caches are empty then.
	Job      *JobRef
	Command  string
	Image    string
	Host     bool
	HostUser bool
	Setup    string
	Env      map[string]string
	Caches   []CacheDefinition
	Reports  ReportMode
	Provider CheckName
	Priority int
	// CPUs and MemoryMiB are the resources the check asks for, and zero when it
	// keeps the default. The node bounds them before the check runs.
	CPUs      int
	MemoryMiB int64
}

// Definition is the validated local CI graph at one repository revision.
// Its methods replace repository-specific suite tables with repository data.
type Definition struct {
	revision   string
	trial      []Suite
	suites     []definitionSuite
	bySuite    map[Suite]definitionSuite
	aliases    map[Suite]Suite
	groupOf    map[CheckName]Group
	providerOf map[CheckName]CheckName
	actImage   string
}

type definitionSuite struct {
	id     Suite
	files  []*regexp.Regexp
	group  Group
	checks []ExecutableCheck
}

type definitionFile struct {
	Version int         `yaml:"version"`
	Trial   []Suite     `yaml:"trial"`
	Caches  []cacheFile `yaml:"caches"`
	Act     *actFile    `yaml:"act"`
	Checks  []checkFile `yaml:"checks"`
	Suites  []suiteFile `yaml:"suites"`
}

type cacheFile struct {
	Name   string `yaml:"name"`
	Target string `yaml:"target"`
	Env    string `yaml:"env"`
}

// actFile holds how this repository's workflow jobs run on a node.
type actFile struct {
	// Image is the platform image a workflow's runs-on: ubuntu-latest maps to.
	// A machine uses it for every job, so a repository that needs another
	// runner image names it once here.
	Image string `yaml:"image"`
}

// DefaultActImage is the act runner image used when a definition names none.
const DefaultActImage = "catthehacker/ubuntu:act-22.04"

// jobFile names one job of a GitHub Actions workflow that a check runs through
// act instead of a command of its own.
type jobFile struct {
	Workflow string `yaml:"workflow"`
	Name     string `yaml:"name"`
	// Matrix selects one configuration of a matrix job, such as shard:7, so a
	// workflow shard can be a check of its own.
	Matrix []string `yaml:"matrix"`
}

type checkFile struct {
	ID            Suite             `yaml:"id"`
	Name          CheckName         `yaml:"name"`
	Command       string            `yaml:"command"`
	Job           *jobFile          `yaml:"job"`
	Image         string            `yaml:"image"`
	Host          bool              `yaml:"host"`
	HostUser      bool              `yaml:"hostUser"`
	Setup         string            `yaml:"setup"`
	Env           map[string]string `yaml:"env"`
	Caches        []string          `yaml:"caches"`
	Reports       ReportMode        `yaml:"reports"`
	ProviderGroup CheckName         `yaml:"providerGroup"`
	Priority      int               `yaml:"priority"`
	CPUs          int               `yaml:"cpus"`
	Memory        string            `yaml:"memory"`
}

type suiteFile struct {
	ID     Suite    `yaml:"id"`
	Files  []string `yaml:"files"`
	Group  Group    `yaml:"group"`
	Checks []Suite  `yaml:"checks"`
}

var (
	definitionID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	// jobWorkflow accepts the workflow paths a repository can run: one file
	// under .github/workflows, never a path outside the repository.
	jobWorkflow = regexp.MustCompile(`^\.github/workflows/[A-Za-z0-9][A-Za-z0-9._-]*\.(yml|yaml)$`)
	// jobName is a GitHub Actions job id, not its display name.
	jobName          = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,127}$`)
	definitionEnv    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	definitionMemory = regexp.MustCompile(`^([1-9][0-9]{0,6})([mMgG])$`)
)

// parseMemory reads a check's memory request, such as 512m or 16g, as MiB. The
// unit is required, so a bare number cannot mean two different sizes.
func parseMemory(value string) (int64, error) {
	match := definitionMemory.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return 0, fmt.Errorf("memory %q must be a whole number with m or g, such as 512m or 16g", value)
	}
	size, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("memory %q: %w", value, err)
	}
	if strings.EqualFold(match[2], "g") {
		size *= 1024
	}
	if size < 16 {
		return 0, fmt.Errorf("memory %q must be at least 16m", value)
	}
	return size, nil
}

// LoadDefinition reads and validates DefinitionPath at ref. Git first resolves
// ref to a commit, so all later reads name one immutable revision.
func LoadDefinition(ctx context.Context, repo, ref string) (*Definition, error) {
	if strings.TrimSpace(ref) == "" {
		ref = "HEAD"
	}
	sha, err := repoid.Git(ctx, repo, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("resolve local CI definition revision: %w", err)
	}
	if !commitSHA.MatchString(sha) {
		return nil, fmt.Errorf("git returned invalid commit SHA for local CI definition")
	}
	entry, err := repoid.Git(ctx, repo, "ls-tree", "-l", sha, "--", DefinitionPath)
	if err != nil {
		return nil, fmt.Errorf("find %s: %w", DefinitionPath, err)
	}
	if entry == "" {
		return nil, ErrDefinitionNotFound
	}
	fields := strings.Fields(entry)
	if len(fields) != 5 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
		return nil, fmt.Errorf("%s must be a regular file", DefinitionPath)
	}
	size, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil || size < 0 || size > MaxDefinitionBytes {
		return nil, fmt.Errorf("%s exceeds the %d-byte limit", DefinitionPath, MaxDefinitionBytes)
	}
	body, err := repoid.GitRaw(ctx, repo, "cat-file", "blob", sha+":"+DefinitionPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", DefinitionPath, err)
	}
	definition, err := ParseDefinition([]byte(body))
	if err != nil {
		return nil, fmt.Errorf("%s at %.12s: %w", DefinitionPath, sha, err)
	}
	definition.revision = sha
	return definition, nil
}

// ParseDefinition strictly decodes and validates a repository definition.
// Unknown keys and duplicate identities are errors, so a typo cannot silently
// remove a command or a required check.
func ParseDefinition(body []byte) (*Definition, error) {
	var file definitionFile
	decoder := yaml.NewDecoder(bytes.NewReader(body), yaml.Strict())
	if err := decoder.Decode(&file); err != nil {
		return nil, err
	}
	var extra definitionFile
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, fmt.Errorf("decode additional YAML document: %w", err)
		}
		return nil, fmt.Errorf("local CI definition must contain exactly one YAML document")
	}
	if file.Version != 1 {
		return nil, fmt.Errorf("version must be 1")
	}
	if len(file.Checks) == 0 || len(file.Suites) == 0 || len(file.Trial) == 0 {
		return nil, fmt.Errorf("checks, suites and trial must not be empty")
	}
	caches := make(map[string]CacheDefinition, len(file.Caches))
	for _, cache := range file.Caches {
		if !definitionID.MatchString(cache.Name) {
			return nil, fmt.Errorf("invalid cache name %q", cache.Name)
		}
		switch cache.Name {
		case "runs", "slots", "diagnostics", "caches":
			return nil, fmt.Errorf("cache name %q is reserved for runner state", cache.Name)
		}
		if _, exists := caches[cache.Name]; exists {
			return nil, fmt.Errorf("duplicate cache %q", cache.Name)
		}
		if cache.Target == "" && cache.Env == "" {
			return nil, fmt.Errorf("cache %q needs a target or env", cache.Name)
		}
		if cache.Target != "" && (cache.Target == "/" || !path.IsAbs(cache.Target) || path.Clean(cache.Target) != cache.Target) {
			return nil, fmt.Errorf("cache %q target must be a clean absolute path other than /", cache.Name)
		}
		if cache.Env != "" && !definitionEnv.MatchString(cache.Env) {
			return nil, fmt.Errorf("cache %q has invalid environment name %q", cache.Name, cache.Env)
		}
		caches[cache.Name] = CacheDefinition(cache)
	}

	definition := &Definition{
		bySuite:    make(map[Suite]definitionSuite, len(file.Suites)),
		aliases:    make(map[Suite]Suite),
		groupOf:    map[CheckName]Group{},
		providerOf: map[CheckName]CheckName{},
		actImage:   DefaultActImage,
	}
	if file.Act != nil && file.Act.Image != "" {
		if strings.TrimSpace(file.Act.Image) != file.Act.Image || len(strings.Fields(file.Act.Image)) != 1 || strings.ContainsRune(file.Act.Image, '\x00') {
			return nil, fmt.Errorf("act image %q must be one container image", file.Act.Image)
		}
		definition.actImage = file.Act.Image
	}
	checks := make(map[Suite]ExecutableCheck, len(file.Checks))
	checkNames := map[CheckName]Suite{}
	for _, input := range file.Checks {
		if !definitionID.MatchString(string(input.ID)) {
			return nil, fmt.Errorf("invalid check id %q", input.ID)
		}
		if _, exists := checks[input.ID]; exists {
			return nil, fmt.Errorf("duplicate check id %q", input.ID)
		}
		if input.Name == "" {
			input.Name = CheckName(input.ID)
		}
		if err := validateDefinitionName(input.Name); err != nil {
			return nil, fmt.Errorf("check %q: %w", input.ID, err)
		}
		if previous, exists := checkNames[input.Name]; exists {
			return nil, fmt.Errorf("checks %q and %q share display name %q", previous, input.ID, input.Name)
		}
		checkNames[input.Name] = input.ID
		if input.Job != nil {
			if input.Command != "" || input.Image != "" || input.Host || input.HostUser || input.Setup != "" || len(input.Caches) > 0 {
				return nil, fmt.Errorf("check %q runs a workflow job, so it cannot also set command, image, host, hostUser, setup or caches", input.ID)
			}
			if _, err := validateJobRef(*input.Job); err != nil {
				return nil, fmt.Errorf("check %q job: %w", input.ID, err)
			}
		} else if strings.TrimSpace(input.Command) == "" || strings.ContainsRune(input.Command, '\x00') {
			return nil, fmt.Errorf("check %q needs a nonempty command without NUL", input.ID)
		}
		if input.Job == nil && input.Host {
			if input.Image != "" || input.HostUser || input.Setup != "" {
				return nil, fmt.Errorf("host check %q cannot set image, hostUser or setup", input.ID)
			}
		} else if input.Job == nil {
			if input.Image == "" || strings.TrimSpace(input.Image) != input.Image || len(strings.Fields(input.Image)) != 1 || strings.ContainsRune(input.Image, '\x00') {
				return nil, fmt.Errorf("container check %q needs one container image", input.ID)
			}
			if strings.ContainsRune(input.Setup, '\x00') {
				return nil, fmt.Errorf("check %q setup contains NUL", input.ID)
			}
			if input.Setup != "" && !input.HostUser {
				return nil, fmt.Errorf("check %q setup requires hostUser", input.ID)
			}
		}
		switch input.Reports {
		case "":
			input.Reports = ReportNone
		case ReportNone, ReportJUnit, ReportGo:
		default:
			return nil, fmt.Errorf("check %q has unknown report mode %q", input.ID, input.Reports)
		}
		if input.Priority < 0 {
			return nil, fmt.Errorf("check %q priority cannot be negative", input.ID)
		}
		if input.CPUs < 0 {
			return nil, fmt.Errorf("check %q cpus must be at least 1", input.ID)
		}
		var memoryMiB int64
		if input.Memory != "" {
			parsed, parseErr := parseMemory(input.Memory)
			if parseErr != nil {
				return nil, fmt.Errorf("check %q: %w", input.ID, parseErr)
			}
			memoryMiB = parsed
		}
		for name, value := range input.Env {
			if !definitionEnv.MatchString(name) || strings.ContainsRune(value, '\x00') {
				return nil, fmt.Errorf("check %q has invalid environment entry %q", input.ID, name)
			}
		}
		if input.ProviderGroup == "" {
			input.ProviderGroup = input.Name
		} else if err := validateDefinitionName(input.ProviderGroup); err != nil {
			return nil, fmt.Errorf("check %q provider group: %w", input.ID, err)
		}
		check := ExecutableCheck{
			Check:     Check{ID: input.ID, Name: input.Name},
			Command:   input.Command,
			Image:     input.Image,
			Host:      input.Host,
			HostUser:  input.HostUser,
			Setup:     input.Setup,
			Env:       cloneStrings(input.Env),
			Reports:   input.Reports,
			Provider:  input.ProviderGroup,
			Priority:  input.Priority,
			CPUs:      input.CPUs,
			MemoryMiB: memoryMiB,
		}
		if input.Job != nil {
			ref, _ := validateJobRef(*input.Job)
			check.Job = &ref
		}
		seenCaches := map[string]bool{}
		for _, name := range input.Caches {
			cache, exists := caches[name]
			if !exists {
				return nil, fmt.Errorf("check %q references unknown cache %q", input.ID, name)
			}
			if seenCaches[name] {
				return nil, fmt.Errorf("check %q repeats cache %q", input.ID, name)
			}
			seenCaches[name] = true
			if input.Host && cache.Env == "" {
				return nil, fmt.Errorf("host check %q cache %q needs env", input.ID, name)
			}
			if !input.Host && cache.Target == "" {
				return nil, fmt.Errorf("container check %q cache %q needs a target", input.ID, name)
			}
			check.Caches = append(check.Caches, cache)
		}
		checks[input.ID] = check
		definition.providerOf[input.Name] = input.ProviderGroup
	}

	groups := map[Group]bool{}
	for _, input := range file.Suites {
		if !definitionID.MatchString(string(input.ID)) {
			return nil, fmt.Errorf("invalid suite id %q", input.ID)
		}
		if _, exists := definition.bySuite[input.ID]; exists {
			return nil, fmt.Errorf("duplicate suite id %q", input.ID)
		}
		if input.Group != "" {
			if err := validateDefinitionName(CheckName(input.Group)); err != nil {
				return nil, fmt.Errorf("suite %q group: %w", input.ID, err)
			}
			if _, exists := checkNames[CheckName(input.Group)]; exists {
				return nil, fmt.Errorf("suite %q group %q collides with a check name", input.ID, input.Group)
			}
			if _, exists := checks[Suite(input.Group)]; exists {
				return nil, fmt.Errorf("suite %q group %q collides with a check id", input.ID, input.Group)
			}
			if groups[input.Group] {
				return nil, fmt.Errorf("duplicate suite group %q", input.Group)
			}
			if _, exists := definition.bySuite[Suite(input.Group)]; exists {
				return nil, fmt.Errorf("suite group %q collides with a suite id", input.Group)
			}
			groups[input.Group] = true
			definition.aliases[Suite(input.Group)] = input.ID
		}
		if len(input.Checks) == 0 {
			return nil, fmt.Errorf("suite %q has no checks", input.ID)
		}
		suite := definitionSuite{id: input.ID, group: input.Group}
		for _, pattern := range input.Files {
			compiled, err := regexp.Compile(pattern)
			if err != nil {
				return nil, fmt.Errorf("suite %q file pattern %q: %w", input.ID, pattern, err)
			}
			suite.files = append(suite.files, compiled)
		}
		seen := map[Suite]bool{}
		for _, id := range input.Checks {
			check, exists := checks[id]
			if !exists {
				return nil, fmt.Errorf("suite %q references unknown check %q", input.ID, id)
			}
			if seen[id] {
				return nil, fmt.Errorf("suite %q repeats check %q", input.ID, id)
			}
			seen[id] = true
			check.Check.Group = input.Group
			if input.Group != "" {
				for _, key := range []CheckName{check.Check.Name, CheckName(check.Check.ID)} {
					if previous := definition.groupOf[key]; previous != "" && previous != input.Group {
						return nil, fmt.Errorf("check %q feeds both %q and %q", id, previous, input.Group)
					}
					definition.groupOf[key] = input.Group
				}
			}
			suite.checks = append(suite.checks, cloneExecutableCheck(check))
		}
		definition.suites = append(definition.suites, suite)
		definition.bySuite[input.ID] = suite
	}
	for id := range definition.bySuite {
		if alias, exists := definition.aliases[id]; exists {
			return nil, fmt.Errorf("suite id %q collides with group alias %q", id, alias)
		}
	}
	seenTrials := map[Suite]bool{}
	for _, id := range file.Trial {
		if _, exists := definition.bySuite[id]; !exists {
			return nil, fmt.Errorf("trial references unknown suite %q", id)
		}
		if seenTrials[id] {
			return nil, fmt.Errorf("trial repeats suite %q", id)
		}
		seenTrials[id] = true
		definition.trial = append(definition.trial, id)
	}
	return definition, nil
}

func validateDefinitionName(name CheckName) error {
	if strings.TrimSpace(string(name)) == "" || len(name) > 128 || strings.ContainsAny(string(name), "\r\n\x00") {
		return fmt.Errorf("name must be nonempty, at most 128 bytes, and contain no newline or NUL")
	}
	return nil
}

func cloneStrings(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func cloneExecutableCheck(input ExecutableCheck) ExecutableCheck {
	input.Env = cloneStrings(input.Env)
	input.Caches = append([]CacheDefinition(nil), input.Caches...)
	return input
}

// Suites returns every suite in definition order.
func (d *Definition) Suites() []Suite {
	out := make([]Suite, 0, len(d.suites))
	for _, suite := range d.suites {
		out = append(out, suite.id)
	}
	return out
}

// WorkflowSuites returns the suites a merge trial runs, in definition order.
func (d *Definition) WorkflowSuites() []Suite {
	return append([]Suite(nil), d.trial...)
}

// SuitesForFiles selects trial suites whose RE2 patterns match a changed path.
// Paths are repository-relative and use slashes on every platform.
func (d *Definition) SuitesForFiles(files []string) []Suite {
	var out []Suite
	for _, id := range d.trial {
		suite := d.bySuite[id]
		matched := false
		for _, file := range files {
			for _, pattern := range suite.files {
				if pattern.MatchString(file) {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if matched {
			out = append(out, id)
		}
	}
	return out
}

func (d *Definition) suite(id Suite) (definitionSuite, bool) {
	if alias, exists := d.aliases[id]; exists {
		id = alias
	}
	suite, ok := d.bySuite[id]
	return suite, ok
}

// ExecutableChecks expands one suite into its commands in run order.
func (d *Definition) ExecutableChecks(id Suite) ([]ExecutableCheck, bool) {
	suite, ok := d.suite(id)
	if !ok {
		return nil, false
	}
	out := make([]ExecutableCheck, len(suite.checks))
	for i, check := range suite.checks {
		out[i] = cloneExecutableCheck(check)
	}
	return out, true
}

// Checks expands one suite into check identities in run order.
func (d *Definition) Checks(id Suite) ([]Check, bool) {
	commands, ok := d.ExecutableChecks(id)
	if !ok {
		return nil, false
	}
	out := make([]Check, len(commands))
	for i, command := range commands {
		out[i] = command.Check
	}
	return out, true
}

// ParseSuites validates names from a command line against this definition.
func (d *Definition) ParseSuites(names []string) ([]Suite, error) {
	suites := make([]Suite, 0, len(names))
	for _, name := range names {
		if _, ok := d.Checks(Suite(name)); !ok {
			return nil, fmt.Errorf("unknown CI suite %q", name)
		}
		suites = append(suites, Suite(name))
	}
	return suites, nil
}

// GroupOf reports the aggregate a check feeds, or an empty group.
func (d *Definition) GroupOf(check CheckName) Group { return d.groupOf[check] }

// ProviderGroup reports the name a CI provider gives one check.
func (d *Definition) ProviderGroup(check CheckName) CheckName {
	if group := d.providerOf[check]; group != "" {
		return group
	}
	return check
}

// ProviderGroups returns the distinct provider names feeding an aggregate.
func (d *Definition) ProviderGroups(group Group) []CheckName {
	var names []CheckName
	for _, suite := range d.suites {
		for _, check := range suite.checks {
			if check.Check.Group != group {
				continue
			}
			name := d.ProviderGroup(check.Check.Name)
			if !containsName(names, name) {
				names = append(names, name)
			}
		}
	}
	return names
}

// RequiredChecks returns every check and aggregate a passing result must hold.
func (d *Definition) RequiredChecks(suites []Suite) []CheckName {
	var required []CheckName
	add := func(name CheckName) {
		if name != "" && !containsName(required, name) {
			required = append(required, name)
		}
	}
	for _, id := range suites {
		checks, ok := d.Checks(id)
		if !ok {
			add(CheckName(id))
			continue
		}
		for _, check := range checks {
			add(check.Name)
			add(check.Group.Name())
		}
	}
	if len(required) == 0 {
		add(CheckName(SuiteChanges))
	}
	return required
}

// CoversSuites reports whether result holds every check the suites require.
func (d *Definition) CoversSuites(result Result, suites []Suite) bool {
	if !result.Complete() {
		return false
	}
	if result.Status != OutcomePassed {
		return true
	}
	seen := map[CheckName]bool{}
	for _, check := range result.Checks {
		seen[check.Name()] = check.Status == OutcomePassed
	}
	for _, name := range d.RequiredChecks(suites) {
		if !seen[name] {
			return false
		}
	}
	return true
}

// ActImage is the platform image a workflow's runs-on: ubuntu-latest maps to on
// this repository's nodes.
func (d *Definition) ActImage() string { return d.actImage }

// JobRef names one job of a GitHub Actions workflow that a check runs through
// act instead of a command of its own.
type JobRef struct {
	Workflow string
	Name     string
	Matrix   []string
}

// validateJobRef accepts a workflow file inside the repository's
// .github/workflows and a job id, never a display name or a path that leaves
// the checkout.
func validateJobRef(in jobFile) (JobRef, error) {
	if !jobWorkflow.MatchString(in.Workflow) {
		return JobRef{}, fmt.Errorf("workflow %q must be one file under .github/workflows ending in .yml or .yaml", in.Workflow)
	}
	if !jobName.MatchString(in.Name) {
		return JobRef{}, fmt.Errorf("job name %q must be a job id, such as go-static", in.Name)
	}
	for _, entry := range in.Matrix {
		key, value, ok := strings.Cut(entry, ":")
		if !ok || !jobName.MatchString(key) || strings.TrimSpace(value) == "" || strings.ContainsRune(value, '\x00') {
			return JobRef{}, fmt.Errorf("matrix entry %q must be key:value, such as shard:7", entry)
		}
	}
	return JobRef{Workflow: in.Workflow, Name: in.Name, Matrix: append([]string(nil), in.Matrix...)}, nil
}
