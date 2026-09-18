package interpreter

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// lint finds the things in a cascade that parse and run but are very likely
// mistakes. Everything here is a warning, not an error: the cascade still
// runs. names is the sorted action list, so the output is deterministic.
func lint(p *Plan, names []string) []string {
	var w []string
	add := func(format string, a ...any) { w = append(w, fmt.Sprintf(format, a...)) }

	producedBy := map[string]string{} // output path -> first action that declares it

	for _, name := range names {
		a := p.actions[name]
		barrier := strings.TrimSpace(a.Run) == ""

		switch {
		case barrier && len(a.Needs) == 0:
			add("action %q does nothing: it has no run command and no needs", name)
		case barrier:
			if ignored := barrierIgnoredFields(a); len(ignored) > 0 {
				add("action %q has no run command, so %s %s ignored",
					name, joinWords(ignored), plural(len(ignored), "is", "are"))
			}
		}

		if !barrier && len(a.Sources) > 0 && len(a.Produces) == 0 {
			add("action %q lists sources but no produces, so a change to those "+
				"files is never noticed", name)
		}

		if !barrier && a.Every != "" {
			if d, err := time.ParseDuration(a.Every); err == nil && d <= 0 {
				add("action %q: every %q is not positive, so it never skips the action", name, a.Every)
			}
		}
		if !barrier && a.Timeout != "" {
			if d, err := time.ParseDuration(a.Timeout); err == nil && d <= 0 {
				add("action %q: timeout %q is not positive, so it does nothing", name, a.Timeout)
			}
		}

		for _, code := range a.AllowExit {
			switch {
			case code == 0:
				add("action %q: allow-exit lists 0, which already counts as success", name)
			case code < 0 || code > 255:
				add("action %q: allow-exit %d is outside the range a process can exit with (0-255)", name, code)
			}
		}

		seenNeed := map[string]bool{}
		for _, dep := range a.Needs {
			if seenNeed[dep] {
				add("action %q needs %q more than once", name, dep)
			}
			seenNeed[dep] = true
		}

		seenOut := map[string]bool{}
		for _, out := range a.Produces {
			if strings.ContainsAny(out, "*?[") {
				add("action %q: produces %q looks like a glob, but produces entries are literal paths", name, out)
			}
			key := filepath.Clean(joinDir(a.Dir, out))
			switch {
			case seenOut[key]:
				add("action %q lists %q in produces more than once", name, out)
			case producedBy[key] != "":
				add("actions %q and %q both declare they produce %q", producedBy[key], name, out)
			default:
				producedBy[key] = name
			}
			seenOut[key] = true
		}

		for _, src := range a.Sources {
			if base, ok := walkBase(joinDir(a.Dir, src)); ok && (base == "/" || base == expandHome("~") || base == expandHome("~")+"/") {
				add("action %q: sources %q walks the entire filesystem or home tree on every check", name, src)
			}
		}

		for k := range a.Env {
			if k == "" || strings.Contains(k, "=") {
				add("action %q: env has a name %q that is not a usable variable name", name, k)
			}
		}

		if a.Dir != "" && !isDir(expandHome(a.Dir)) && !anyoneProduces(p, a.Dir) {
			add("action %q: dir %q does not exist; the command will fail unless an "+
				"earlier action creates it", name, a.Dir)
		}
	}
	return w
}

// barrierIgnoredFields lists the fields a barrier (empty run) set that do
// nothing without a command.
func barrierIgnoredFields(a Action) []string {
	var f []string
	check := func(set bool, label string) {
		if set {
			f = append(f, label)
		}
	}
	check(len(a.Produces) > 0, "produces")
	check(len(a.Sources) > 0, "sources")
	check(a.Every != "", "every")
	check(a.Timeout != "", "timeout")
	check(a.Unless != "", "unless")
	check(a.Progress != "", "progress")
	check(a.ProgressEvery != "", "progress-every")
	check(len(a.AllowExit) > 0, "allow-exit")
	check(len(a.Env) > 0, "env")
	check(a.Dir != "", "dir")
	check(a.Optional, "optional")
	sort.Strings(f)
	return f
}

func anyoneProduces(p *Plan, path string) bool {
	want := filepath.Clean(path)
	for _, a := range p.actions {
		for _, out := range a.Produces {
			if filepath.Clean(joinDir(a.Dir, out)) == want {
				return true
			}
		}
	}
	return false
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// walkBase is the directory a "**" pattern's tree walk would start from — the
// static prefix up to the last "/" before the "**". ok is false when the
// pattern has no "**".
func walkBase(pat string) (base string, ok bool) {
	i := strings.Index(pat, "**")
	if i < 0 {
		return "", false
	}
	base = pat[:i]
	if s := strings.LastIndexByte(base, '/'); s >= 0 {
		return base[:s+1], true
	}
	return base, true
}

func joinWords(s []string) string {
	q := make([]string, len(s))
	for i, w := range s {
		q[i] = "`" + w + "`"
	}
	switch len(q) {
	case 1:
		return q[0]
	case 2:
		return q[0] + " and " + q[1]
	default:
		return strings.Join(q[:len(q)-1], ", ") + " and " + q[len(q)-1]
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
