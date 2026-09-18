package work

import (
	"errors"
	"fmt"
)

// skipped marks work a call decided not to do; see [Skip].
type skipped struct{ reason string }

func (s skipped) Error() string { return s.reason }

// Skip returns an error meaning a call chose not to do its work — a disabled
// feature, a case that does not apply. Return it from [Do]'s function, with
// T's zero value:
//
//	if len(repos) == 0 {
//	    return "", "", flow.Skip("no repositories found under %s", roots)
//	}
//
// It counts as success for the run's exit code but is reported apart from an
// ordinary ok. It is not how a call says "this was already done" — that is
// an ordinary check written with an if statement, using [LastRecord] to ask
// what a previous run recorded.
func Skip(format string, a ...any) error {
	return skipped{reason: fmt.Sprintf(format, a...)}
}

// fatalError marks an error that aborts the whole run, not only the branch
// of it that returned the error.
type fatalError struct{ err error }

func (e fatalError) Error() string { return e.err.Error() }
func (e fatalError) Unwrap() error { return e.err }

// Fatal wraps err so that it aborts the entire run: every other call still
// in flight is canceled rather than only the caller of the one that failed.
func Fatal(err error) error {
	if err == nil {
		return nil
	}
	return fatalError{err}
}

// isFatal reports whether err was wrapped by [Fatal].
func isFatal(err error) bool {
	var f fatalError
	return errors.As(err, &f)
}
