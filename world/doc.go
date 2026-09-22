// Package world provides abstractions for intercepting external side effects,
// supporting dry-run execution and interactive user confirmation.
//
// The dependency between work and world is strictly one-way: world imports
// [github.com/m-d-brown/cascade/work] (effects receive a *work.Context for
// scoped logging and cancellation), but the core work package has no dependency
// on world. Tasks can execute standard Go operations directly, or route
// mutations through a [World] implementation when interception is desired.
//
// [Real] executes side effects directly against the operating environment.
// [DryRun] suppresses mutations, logging planned actions and returning
// simulated values. [Confirm] wraps an underlying World to prompt the user
// interactively before executing each effect.
package world
