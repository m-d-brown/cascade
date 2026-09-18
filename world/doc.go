// Package world is what lets a call describe an effect on the outside
// world instead of just performing it — the addition a call reaches for if
// it wants [DryRun] or [Confirm] to be able to intercept that effect.
//
// The dependency runs one way: world imports
// [github.com/mdbrown/cascade/work] (an effect is handed a
// *work.Context to log through and to notice cancellation on), but work
// has no notion that world exists. Do, Go and resumption work whether or
// not a call ever imports this package, because a call is an ordinary Go
// function, free to touch the outside world however it likes. This package
// exists for the call that wants that touch to be interceptable — and for
// the application that wants a single choice, made once for the run, to
// decide whether it happens for real.
package world
