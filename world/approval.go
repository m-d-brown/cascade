package world

import (
	"context"
	"fmt"
	"sync"

	"github.com/mdbrown/cascade/work"
)

// Request describes an effect awaiting confirmation. [Confirm] builds one
// per effect, which is the only way a run asks: a call never prompts for
// itself, because whether a run wants a human in the loop is a property of
// the run rather than of any call in it.
type Request struct {
	// Task is the path of the call whose effect is waiting.
	Task string
	// Target is the effect itself — the command, the path, the address —
	// the one line the prompt shows and wraps.
	Target string
}

// Decision is the response to a [Request].
type Decision int

const (
	// ApproveOnce executes the requested action once.
	ApproveOnce Decision = iota
	// SkipOnce skips the requested action once.
	SkipOnce
	// DryRunOnce performs the action against a dry-run world: it is logged
	// as "would …" and hands back its stand-in value, and nothing happens.
	DryRunOnce
	// ApproveAll approves all current and future mutating actions in this run.
	ApproveAll
	// AbortRun immediately halts the entire run.
	AbortRun
	// ApprovePermanent executes the requested action and indicates the action
	// should be permanently allowed (e.g. added to policy).
	ApprovePermanent
)

// Prompter handles interactive approvals during a run.
type Prompter interface {
	PromptApproval(ctx context.Context, req Request) (Decision, error)
}

// prompterKey is the context key [WithPrompter] stores a [Prompter] under.
type prompterKey struct{}

// WithPrompter returns a copy of ctx carrying p, so a workflow can retrieve
// the prompter that belongs to whatever display is running and hand it to
// [Confirm] — rather than build one of its own that writes straight to the
// terminal and fights the live display for the screen.
//
// The command-line front end ([github.com/mdbrown/cascade/cli]) does
// this before the run starts: the prompter is the live tree's inline
// approval modal in a terminal, the plain line-by-line prompt otherwise. A
// workflow assembling its own confirming world reads it back with
// [PrompterFromContext]:
//
//	w := world.Real()
//	if p := world.PrompterFromContext(ctx); p != nil {
//	    w = world.Confirm(w, p)
//	}
//
// Whether to wrap in [Confirm] at all stays the workflow's call — a human in
// the loop is a property of the run, not of the display — this only spares
// it from constructing the prompter.
func WithPrompter(ctx context.Context, p Prompter) context.Context {
	return context.WithValue(ctx, prompterKey{}, p)
}

// PrompterFromContext returns the [Prompter] stored in ctx by [WithPrompter],
// or nil if there is none.
func PrompterFromContext(ctx context.Context) Prompter {
	if ctx == nil {
		return nil
	}
	p, _ := ctx.Value(prompterKey{}).(Prompter)
	return p
}

// Confirm returns a world that asks before every effect and does what the
// answer says: perform it, dry-run it, or stop the run.
//
// It is the reason effects go through a [World] at all: a call describes
// what it is about to do, and whether that needs a human in the loop is a
// property of the run, not of the call, so no call has to remember to ask.
//
//	world.Confirm(world.Real(), prompter)
//
// The prompter belongs to whatever display is running, so take it from the
// context the run was started with rather than building one that fights the
// live display for the screen — see [PrompterFromContext]. A nil p makes
// Confirm a no-op and returns w unchanged.
func Confirm(w World, p Prompter) World {
	if p == nil {
		return w
	}
	return &confirmingWorld{inner: w, prompter: p}
}

type confirmingWorld struct {
	inner    World
	prompter Prompter

	mu  sync.Mutex
	all bool
}

func (c *confirmingWorld) Perform(call *work.Context, e Effect[any]) (any, error) {
	c.mu.Lock()
	all := c.all
	c.mu.Unlock()
	if all {
		return c.inner.Perform(call, e)
	}
	decision, err := c.prompter.PromptApproval(call, Request{
		Task: call.Path(), Target: e.What,
	})
	if err != nil {
		return e.Instead, work.Fatal(err)
	}
	switch decision {
	case ApproveAll:
		c.mu.Lock()
		c.all = true
		c.mu.Unlock()
		return c.inner.Perform(call, e)
	case SkipOnce:
		call.Warnf("skipped by you: %s", e.What)
		return e.Instead, nil
	case DryRunOnce:
		return DryRun().Perform(call, e)
	case AbortRun:
		return e.Instead, work.Fatal(fmt.Errorf("stopped by you at: %s", e.What))
	default:
		return c.inner.Perform(call, e)
	}
}
