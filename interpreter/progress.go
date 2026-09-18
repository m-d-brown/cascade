package interpreter

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mdbrown/cascade/history"
)

// ActionStatus is where one action of a run has got to, folded from the run's
// event log.
type ActionStatus struct {
	Name   string
	Status history.Status
	// Detail is the action's latest status line while it runs, or its summary
	// once it finishes.
	Detail   string
	Started  time.Time
	Updated  time.Time
	Finished bool
}

// Elapsed is how long the action has been running, or how long it ran for if
// it has finished.
func (s ActionStatus) Elapsed() time.Duration {
	if s.Started.IsZero() {
		return 0
	}
	end := time.Now()
	if s.Finished && !s.Updated.IsZero() {
		end = s.Updated
	}
	return end.Sub(s.Started)
}

// RunProgress is a run's state as read from its event log — current even for a
// run still in flight, since the log is written as events happen.
type RunProgress struct {
	Run      string
	Started  time.Time
	Finished bool
	Actions  []ActionStatus
}

// logLine is one line of the run's event log (history.LogEvents' format).
type logLine struct {
	Time    string `json:"time"`
	Run     string `json:"run"`
	Event   string `json:"event"`
	Task    string `json:"task"`
	Status  string `json:"status"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

// ReadProgress folds the event log under logDir (its run.jsonl) into the
// current state of each top-level action, in the order they started.
func ReadProgress(logDir string) (*RunProgress, error) {
	f, err := os.Open(filepath.Join(logDir, "run.jsonl"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no event log under %s — the run's logs may have been cleaned up", logDir)
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()

	rp := &RunProgress{}
	byName := map[string]*ActionStatus{}
	var order []string
	var root string

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var l logLine
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue // a half-written trailing line while the run is live
		}
		when, _ := time.Parse(time.RFC3339Nano, l.Time)

		switch l.Event {
		case "run-started":
			rp.Started, rp.Run = when, l.Run
		case "run-finished":
			rp.Finished = true
		}
		if l.Task == "" {
			continue
		}
		if root == "" && parent(l.Task) == "" {
			root = l.Task
		}
		name, ok := childName(root, l.Task)
		if !ok {
			continue
		}

		a := byName[name]
		if a == nil {
			a = &ActionStatus{Name: name}
			byName[name] = a
			order = append(order, name)
		}
		switch l.Event {
		case "task-started":
			a.Started, a.Updated = when, when
			setStatus(a, l.Status)
		case "task-status":
			if l.Message != "" {
				a.Detail, a.Updated = l.Message, when
			}
		case "task-log":
			if l.Level != "" && l.Message != "" { // a warning is worth surfacing
				a.Detail, a.Updated = l.Message, when
			}
		case "task-finished":
			a.Finished, a.Updated = true, when
			if l.Message != "" {
				a.Detail = l.Message
			}
			setStatus(a, l.Status)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if rp.Run == "" {
		rp.Run = filepath.Base(logDir)
	}
	for _, name := range order {
		rp.Actions = append(rp.Actions, *byName[name])
	}
	return rp, nil
}

func setStatus(a *ActionStatus, s string) {
	if s == "" {
		return
	}
	var st history.Status
	if st.UnmarshalText([]byte(s)) == nil {
		a.Status = st
	}
}

// parent is everything before the last "/" of a call path.
func parent(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return ""
}

// childName returns the action name if task is a direct child of root.
func childName(root, task string) (string, bool) {
	if root == "" || len(task) <= len(root)+1 || task[:len(root)] != root || task[len(root)] != '/' {
		return "", false
	}
	rest := task[len(root)+1:]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '/' {
			return "", false
		}
	}
	return rest, true
}
