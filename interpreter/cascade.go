package interpreter

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Action is one node of a cascade: a shell command, what it waits for, and how
// it decides it is already up to date. Every field is optional — the action's
// name is the key it is filed under in the cascade file, not a field here.
type Action struct {
	// Run is the command line, run through "sh -c" so pipelines and $VARs
	// work as written. Empty makes the action a barrier: it waits on Needs
	// and otherwise does nothing.
	Run string `yaml:"run"`
	// Needs names the actions that must finish first. A need that fails
	// leaves this action blocked — recorded as skipped, not run. In the file
	// a bare string is accepted as a one-element list.
	Needs []string `yaml:"needs"`
	// Produces and Sources are a make-style freshness check: the action is up
	// to date when every Produces path exists and is newer than every file
	// matched by Sources. Produces entries are literal paths; a Sources entry
	// is a glob, where a "**" segment matches any number of directories. A
	// leading "~" is expanded, the way the shell expands it for Run. In the
	// file a bare string is accepted for either.
	Produces []string `yaml:"produces"`
	Sources  []string `yaml:"sources"`
	// Every skips the action when the journal shows it last succeeded less
	// than this ago — a Go duration string such as "24h" or "90m".
	Every string `yaml:"every"`
	// Timeout bounds how long Run (and its checks) may take — a Go duration
	// string. Past it the command is killed and the action fails, like any
	// other failure. Empty means no limit.
	Timeout string `yaml:"timeout"`
	// Unless is a shell command run before the action; exit status 0 means
	// the work is already done and the action is skipped.
	Unless string `yaml:"unless"`
	// Progress is a shell command run on an interval while Run is in flight;
	// its last line of output becomes the action's live status, for a long
	// command that does not report progress of its own. With no Progress set,
	// Run's own latest line of output is used.
	Progress string `yaml:"progress"`
	// ProgressEvery is how often the live status refreshes — a Go duration
	// string. Default: every 15s with a Progress command, every 2s without.
	ProgressEvery string `yaml:"progress-every"`
	// Env sets environment variables for Run and Unless, on top of (not
	// instead of) the environment the runner itself has.
	Env map[string]string `yaml:"env"`
	// Dir is the working directory for Run and Unless, and what relative
	// Produces/Sources paths are resolved against. A leading "~" is expanded.
	Dir string `yaml:"dir"`
	// AllowExit lists non-zero exit codes from Run that are not failures.
	AllowExit []int `yaml:"allow-exit"`
	// Optional turns a failure of Run into a skip: the run is not failed and
	// actions that need this one still run.
	Optional bool `yaml:"optional"`
	// Description is the one line shown under the action by `dot` and `state`.
	Description string `yaml:"description"`
}

// Plan is a parsed, validated action graph, ready for [Plan.Run].
type Plan struct {
	name     string
	actions  map[string]Action
	order    []string
	waves    [][]string
	warnings []string
}

// Warnings are the problems in the cascade file that do not stop it running
// but are very likely mistakes — a barrier action that also sets freshness
// fields, an `allow-exit` code no process can return, two actions that
// declare the same output. [Plan.Run] logs them before it starts, and
// `cascade check` prints them. An empty slice means nothing looked wrong.
func (p *Plan) Warnings() []string { return append([]string(nil), p.warnings...) }

// file is the raw YAML shape.
type file struct {
	Name    string            `yaml:"name"`
	Actions map[string]Action `yaml:"actions"`
}

// actionFields is every key an action may set, for the "unknown field" error.
// Keep it comma-space separated: decode.go splits it into the valid-key set.
const actionFields = "run, needs, produces, sources, every, timeout, unless, " +
	"env, dir, allow-exit, optional, progress, progress-every, description"

// Parse reads a cascade from YAML. An unknown field, a dependency on an
// action that is not defined, and a dependency cycle are all errors.
func Parse(data []byte) (*Plan, error) {
	f, err := decodeFile(data)
	if err != nil {
		return nil, err
	}
	return build(f)
}

var (
	dupKeyRe  = regexp.MustCompile(`line (\d+): mapping key "([^"]*)" already defined at line (\d+)`)
	badTypeRe = regexp.MustCompile(`line (\d+): cannot unmarshal .+? into (.+)`)
	typeNames = strings.NewReplacer(
		"[]string", "a list", "[]int", "a list of whole numbers",
		"map[string]string", "a map", "string", "text",
		"int", "a whole number", "bool", "true or false")
	unwrapErrs = "yaml: unmarshal errors:\n  "
)

// friendlyParseError turns yaml.v3's lower-level messages into ones that talk
// about the cascade file rather than Go types.
func friendlyParseError(err error) error {
	if errors.Is(err, io.EOF) {
		return errors.New("the cascade is empty")
	}
	s := strings.TrimPrefix(err.Error(), unwrapErrs)
	if m := dupKeyRe.FindStringSubmatch(s); m != nil {
		return fmt.Errorf("line %s: %q is defined twice (first at line %s)", m[1], m[2], m[3])
	}
	if m := badTypeRe.FindStringSubmatch(s); m != nil {
		return fmt.Errorf("line %s: expected %s here", m[1], typeNames.Replace(strings.TrimPrefix(m[2], "type ")))
	}
	if s != err.Error() {
		return errors.New(s)
	}
	return err
}

// Load reads a cascade from a file. A cascade with no name of its own takes the
// file's base name.
func Load(path string) (*Plan, error) {
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		return nil, fmt.Errorf("%s is a directory, not a cascade file", path)
	}
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("no cascade file at %s (point at one with -f)", path)
	case err != nil:
		return nil, fmt.Errorf("cannot read %s: %w", path, err)
	}
	p, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if p.name == "" {
		base := filepath.Base(path)
		p.name = strings.TrimSuffix(base, filepath.Ext(base))
	}
	return p, nil
}

func build(f file) (*Plan, error) {
	if len(f.Actions) == 0 {
		return nil, errors.New("the cascade defines no actions")
	}
	r := &Plan{name: f.Name, actions: make(map[string]Action, len(f.Actions))}
	names := make([]string, 0, len(f.Actions))
	for name, a := range f.Actions {
		switch {
		case strings.TrimSpace(name) == "":
			return nil, errors.New("an action has a blank name")
		case name != strings.TrimSpace(name):
			return nil, fmt.Errorf("action name %q has leading or trailing whitespace", name)
		case strings.Contains(name, "/"):
			return nil, fmt.Errorf("action name %q cannot contain '/'", name)
		}
		for _, d := range []struct{ field, val string }{
			{"every", a.Every}, {"timeout", a.Timeout}, {"progress-every", a.ProgressEvery},
		} {
			if d.val == "" {
				continue
			}
			if _, err := time.ParseDuration(d.val); err != nil {
				return nil, fmt.Errorf("action %q: bad %s %q: %w", name, d.field, d.val, err)
			}
		}
		for _, pat := range a.Sources {
			if _, err := filepath.Match(pat, ""); err != nil {
				return nil, fmt.Errorf("action %q: bad sources pattern %q: %w", name, pat, err)
			}
		}
		r.actions[name] = a
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		for _, dep := range r.actions[name].Needs {
			if dep == name {
				return nil, fmt.Errorf("action %q needs itself", name)
			}
			if _, ok := r.actions[dep]; !ok {
				return nil, fmt.Errorf("action %q needs %q, which is not an action in this cascade", name, dep)
			}
		}
	}

	order, waves, err := schedule(r.actions, names)
	if err != nil {
		return nil, err
	}
	r.order, r.waves = order, waves
	r.warnings = lint(r, names)
	return r, nil
}

// schedule returns one valid topological order and the topological waves —
// each wave being the actions whose dependencies are all in earlier waves,
// sorted for a stable result. A cycle is an error naming the loop.
func schedule(actions map[string]Action, names []string) (order []string, waves [][]string, err error) {
	indeg := make(map[string]int, len(names))
	dependents := make(map[string][]string, len(names))
	for _, n := range names {
		seen := map[string]bool{}
		for _, dep := range actions[n].Needs {
			if seen[dep] {
				continue
			}
			seen[dep] = true
			indeg[n]++
			dependents[dep] = append(dependents[dep], n)
		}
	}

	var frontier []string
	for _, n := range names {
		if indeg[n] == 0 {
			frontier = append(frontier, n)
		}
	}
	for len(frontier) > 0 {
		sort.Strings(frontier)
		waves = append(waves, frontier)
		var next []string
		for _, n := range frontier {
			order = append(order, n)
			for _, d := range dependents[n] {
				indeg[d]--
				if indeg[d] == 0 {
					next = append(next, d)
				}
			}
		}
		frontier = next
	}

	if len(order) != len(names) {
		if cycle := findCycle(actions, names); len(cycle) > 0 {
			return nil, nil, fmt.Errorf("the cascade has a dependency cycle: %s", strings.Join(cycle, " → "))
		}
		return nil, nil, errors.New("the cascade has a dependency cycle")
	}
	return order, waves, nil
}

// findCycle returns the actions on one dependency cycle, as a → b → … → a, or
// nil if there is none.
func findCycle(actions map[string]Action, names []string) []string {
	const (
		white = iota
		gray
		black
	)
	color := make(map[string]int, len(names))
	var stack []string
	var found []string
	var visit func(string) bool
	visit = func(n string) bool {
		color[n], stack = gray, append(stack, n)
		for _, dep := range actions[n].Needs {
			switch color[dep] {
			case gray:
				for i := len(stack) - 1; i >= 0; i-- {
					if stack[i] == dep {
						found = append(append([]string(nil), stack[i:]...), dep)
						return true
					}
				}
			case white:
				if visit(dep) {
					return true
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
		return false
	}
	for _, n := range names {
		if color[n] == white && visit(n) {
			return found
		}
	}
	return nil
}

// Name is the cascade's name: its own name field, or the file's base name.
func (r *Plan) Name() string { return r.name }

// Order is one valid order to run the actions in — every action after the
// ones it needs. It is stable across calls.
func (r *Plan) Order() []string { return append([]string(nil), r.order...) }

// Actions returns a copy of the actions, keyed by name.
func (r *Plan) Actions() map[string]Action {
	out := make(map[string]Action, len(r.actions))
	for k, v := range r.actions {
		out[k] = v
	}
	return out
}
