package main

import (
	"errors"
)

// Error is a sentinel error whose identity is the pointer it was built with, so
// two sentinels with the same text still compare as different errors.
type Error struct {
	sentinel error
}

// newError creates a new sentinel error.
func newError(text string) *Error {
	return &Error{sentinel: errors.New(text)}
}

// Error implements the error interface.
func (e *Error) Error() string {
	return e.sentinel.Error()
}

// Is implements errors.Is() support by comparing sentinel pointers.
func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	if !ok {
		return false
	}

	return other.sentinel == e.sentinel
}

// ErrInstanceIgnored indicates an instance is ignored.
var ErrInstanceIgnored = newError("instance is ignored")

// ErrInstanceNoHealthcheck indicates an instance has no healthcheck.
var ErrInstanceNoHealthcheck = newError("instance has no healthcheck")

// ErrInstanceNotEnabled means the instance looks like it wants health checking but never opted in.
var ErrInstanceNotEnabled = newError("instance has a healthcheck but is not enabled")

// ErrIntentionallyStopped means the user stopped the instance, so no restart
// policy may bring it back.
var ErrIntentionallyStopped = newError("the instance has been intentionally stopped")

// ErrNotRunning is an internal sentinel error.
var ErrNotRunning = newError("not running")
