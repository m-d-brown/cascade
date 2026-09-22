package world

import (
	"fmt"

	"github.com/m-d-brown/cascade/work"
)

// Effect is one thing a call does to the world: what it is, how it happens,
// and what a dry run hands back in its place. Its type parameter is what the
// effect produces, so [Perform] hands that same type straight back: no
// assertion, no conversion:
//
//	stats, err := world.Perform(ctx, w, world.Effect[resticStats]{
//	    What:    "run restic backup /srv",
//	    Do:      func() (resticStats, error) { return backup(ctx) },
//	    Instead: resticStats{Files: 4128},
//	})
//
// Use [Change] for the common case: a change to the world that produces
// nothing but the fact that it happened.
type Effect[T any] struct {
	// What describes the effect in one line, as an instruction: "upload
	// dist/widget-1.4.0 to example.com". It is the log line in a real run
	// and the "would …" line in a dry run, so it is worth writing well.
	What string
	// Do performs the effect. It is not called in a dry run.
	Do func() (T, error)
	// Instead is what a dry run returns in place of Do's value, so the
	// code after the effect has something of the right shape to work with.
	Instead T
}

// World is where a call's effects land.
//
// A call describes what it is about to do as an [Effect] and hands it,
// along with the world it should land in, to [Perform] or [Change], and the
// world decides what that means. There are two:
//
//	world.Real()     // effects happen
//	world.DryRun()   // nothing happens; each effect hands back its Instead
//
// That is the whole of "what if I don't want this to really run": one
// choice, made by whoever builds the World a run uses, rather than a flag
// every call has to remember to honour. A call never asks which world it is
// in: if a call branches on the answer, the effect was described in the
// wrong place.
//
// An application can supply its own by wrapping one of these: a world that
// asks before every effect ([Confirm]), one that records them for a test,
// one that refuses anything touching production. The interface method sees
// the effect with its type erased to Effect[any]; [Perform] is the typed
// front door that erases on the way in and restores the type on the way
// out.
type World interface {
	// Perform carries out one effect, or stands in for it, and returns what
	// it produced.
	Perform(call *work.Context, e Effect[any]) (any, error)
}

// Real returns the world where effects happen. It is the default.
func Real() World { return realWorld{} }

// DryRun returns a world where nothing happens: every effect is logged as
// "would …" and hands back its [Effect.Instead] value.
func DryRun() World { return dryRunWorld{} }

type realWorld struct{}

func (realWorld) Perform(call *work.Context, e Effect[any]) (any, error) {
	if e.What != "" {
		call.Logf("%s", e.What)
	}
	if e.Do == nil {
		return e.Instead, nil
	}
	return e.Do()
}

type dryRunWorld struct{}

func (dryRunWorld) Perform(call *work.Context, e Effect[any]) (any, error) {
	what := e.What
	if what == "" {
		what = "have an effect on the world"
	}
	call.Logf("would %s", what)
	return e.Instead, nil
}

// Perform hands an effect to w and returns what it produced, as the T the
// effect declared. In a real run that is [Effect.Do]'s value; in a dry run,
// [Effect.Instead]. A nil w means [Real].
//
//	data, err := world.Perform(ctx, w, world.Effect[[]byte]{
//	    What:    "read " + path,
//	    Do:      func() ([]byte, error) { return os.ReadFile(path) },
//	    Instead: []byte("example\n"),
//	})
func Perform[T any](call *work.Context, w World, e Effect[T]) (T, error) {
	var zero T
	if w == nil {
		w = Real()
	}
	erased := Effect[any]{What: e.What, Instead: e.Instead}
	if e.Do != nil {
		erased.Do = func() (any, error) { return e.Do() }
	}
	v, err := w.Perform(call, erased)

	what := e.What
	if what == "" {
		what = "effect"
	}
	val, ok := v.(T)
	if !ok && v != nil {
		// A World handed back a value of a type the effect never declared:
		// a custom World's bug. Fail closed rather than quietly returning
		// the zero value.
		return zero, fmt.Errorf("%s: world returned %T, want %T", what, v, zero)
	}
	if err != nil {
		return val, fmt.Errorf("%s: %w", what, err)
	}
	return val, nil
}

// Change performs an effect that produces nothing but its own happening: the
// common case, and the one worth keeping short.
//
//	err := world.Change(ctx, w, "create "+dir, func() error { return os.MkdirAll(dir, 0o755) })
func Change(call *work.Context, w World, what string, do func() error) error {
	_, err := Perform(call, w, Effect[any]{
		What: what,
		Do:   func() (any, error) { return nil, do() },
	})
	return err
}
