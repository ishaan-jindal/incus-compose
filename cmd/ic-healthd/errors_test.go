package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ----------------------------------------------------------------------------
// newError Tests
// ----------------------------------------------------------------------------

func TestNewError_CreatesError(t *testing.T) {
	err := newError("test error")
	assert.NotNil(t, err)
	assert.Equal(t, "test error", err.Error())
}

func TestNewError_DifferentSentinels(t *testing.T) {
	err1 := newError("error one")
	err2 := newError("error one") // Same text, different sentinel

	assert.False(t, errors.Is(err1, err2), "different newError calls should create different sentinels")
}

// ----------------------------------------------------------------------------
// Error() Tests
// ----------------------------------------------------------------------------

func TestError_Error_Simple(t *testing.T) {
	err := newError("simple error")
	assert.Equal(t, "simple error", err.Error())
}

func TestError_Error_WithFailures(t *testing.T) {
	err := newError("checks failed").WithFailures(3)
	assert.Equal(t, "checks failed (failures: 3)", err.Error())
}

func TestError_Error_ZeroFailuresOmitted(t *testing.T) {
	err := newError("checks failed").WithFailures(0)
	assert.Equal(t, "checks failed", err.Error())
}

func TestError_Error_WithWrapped(t *testing.T) {
	inner := fmt.Errorf("inner error")
	err := newError("outer error").Wrap(inner)
	assert.Equal(t, "outer error: inner error", err.Error())
}

func TestError_Error_WithFailuresAndWrapped(t *testing.T) {
	inner := fmt.Errorf("inner error")
	err := newError("outer error").WithFailures(2).Wrap(inner)
	assert.Equal(t, "outer error (failures: 2): inner error", err.Error())
}

// ----------------------------------------------------------------------------
// Is() Tests
// ----------------------------------------------------------------------------

func TestError_Is_SameSentinel(t *testing.T) {
	sentinel := newError("sentinel")
	derived := sentinel.WithFailures(5)

	assert.True(t, errors.Is(derived, sentinel))
}

func TestError_Is_DifferentSentinel(t *testing.T) {
	err1 := newError("error one")
	err2 := newError("error two")

	assert.False(t, errors.Is(err1, err2))
}

func TestError_Is_WithChainedContext(t *testing.T) {
	sentinel := newError("sentinel")
	derived := sentinel.WithFailures(1).WithFailures(2)

	assert.True(t, errors.Is(derived, sentinel))
}

func TestError_Is_WithWrappedError(t *testing.T) {
	sentinel := newError("sentinel")
	inner := fmt.Errorf("inner")
	derived := sentinel.Wrap(inner)

	assert.True(t, errors.Is(derived, sentinel))
}

func TestError_Is_NonErrorTarget(t *testing.T) {
	err := newError("test")
	other := fmt.Errorf("other")

	assert.False(t, errors.Is(err, other))
}

// ----------------------------------------------------------------------------
// As() Tests
// ----------------------------------------------------------------------------

func TestError_As_ExtractsError(t *testing.T) {
	original := newError("original").WithFailures(4)

	var extracted *Error
	ok := errors.As(original, &extracted)

	assert.True(t, ok)
	assert.Equal(t, original, extracted)
}

func TestError_As_ExtractsFromChain(t *testing.T) {
	sentinel := newError("sentinel")
	derived := sentinel.WithFailures(1).Wrap(fmt.Errorf("cause"))

	var extracted *Error
	ok := errors.As(derived, &extracted)

	assert.True(t, ok)
	assert.Equal(t, derived, extracted)
	assert.True(t, errors.Is(extracted, sentinel))
}

// errors.As() short-circuits via plain type-assignability when the target
// type matches the error's concrete type exactly (*Error here), so it never
// actually calls our custom As() method - the two tests above exercise
// errors.As() the function, not (*Error).As() the method. Call the method
// directly to cover its own two branches.

func TestError_As_DirectCall_MatchingTarget(t *testing.T) {
	err := newError("direct")

	var extracted *Error
	ok := err.As(&extracted)

	assert.True(t, ok)
	assert.Same(t, err, extracted)
}

func TestError_As_DirectCall_WrongTargetType(t *testing.T) {
	err := newError("direct")

	var wrongTarget string
	ok := err.As(&wrongTarget)

	assert.False(t, ok)
	assert.Empty(t, wrongTarget)
}

// ----------------------------------------------------------------------------
// Unwrap() Tests
// ----------------------------------------------------------------------------

func TestError_Unwrap_NoWrapped(t *testing.T) {
	err := newError("test")
	assert.Nil(t, errors.Unwrap(err))
}

func TestError_Unwrap_WithWrapped(t *testing.T) {
	inner := fmt.Errorf("inner error")
	err := newError("outer").Wrap(inner)

	unwrapped := errors.Unwrap(err)
	assert.Equal(t, inner, unwrapped)
}

func TestError_Unwrap_NestedChain(t *testing.T) {
	innermost := fmt.Errorf("innermost")
	middle := fmt.Errorf("middle: %w", innermost)
	outer := newError("outer").Wrap(middle)

	unwrapped := errors.Unwrap(outer)
	assert.Equal(t, middle, unwrapped)

	unwrapped = errors.Unwrap(unwrapped)
	assert.Equal(t, innermost, unwrapped)
}

// ----------------------------------------------------------------------------
// Wrap() Tests
// ----------------------------------------------------------------------------

func TestError_Wrap_PreservesSentinel(t *testing.T) {
	sentinel := newError("sentinel")
	inner := fmt.Errorf("inner")
	wrapped := sentinel.Wrap(inner)

	assert.True(t, errors.Is(wrapped, sentinel))
}

func TestError_Wrap_IncludesInnerMessage(t *testing.T) {
	sentinel := newError("outer")
	inner := fmt.Errorf("inner details")
	wrapped := sentinel.Wrap(inner)

	assert.Contains(t, wrapped.Error(), "outer")
	assert.Contains(t, wrapped.Error(), "inner details")
}

func TestError_Wrap_PreservesFailures(t *testing.T) {
	sentinel := newError("sentinel").WithFailures(7)
	wrapped := sentinel.Wrap(fmt.Errorf("inner"))

	assert.Contains(t, wrapped.Error(), "failures: 7")
}

func TestError_Wrap_MultipleWraps(t *testing.T) {
	sentinel := newError("sentinel")
	inner1 := fmt.Errorf("inner1")
	inner2 := fmt.Errorf("inner2")

	wrapped1 := sentinel.Wrap(inner1)
	wrapped2 := sentinel.Wrap(inner2)

	assert.True(t, errors.Is(wrapped1, sentinel))
	assert.True(t, errors.Is(wrapped2, sentinel))

	assert.NotEqual(t, errors.Unwrap(wrapped1), errors.Unwrap(wrapped2))
}

// ----------------------------------------------------------------------------
// WithFailures() Tests
// ----------------------------------------------------------------------------

func TestError_WithFailures_AddsContext(t *testing.T) {
	sentinel := newError("base")
	derived := sentinel.WithFailures(9)

	assert.Contains(t, derived.Error(), "base")
	assert.Contains(t, derived.Error(), "9")
}

func TestError_WithFailures_PreservesSentinel(t *testing.T) {
	sentinel := newError("sentinel")
	derived := sentinel.WithFailures(3)

	assert.True(t, errors.Is(derived, sentinel))
}

func TestError_WithFailures_Overwrites(t *testing.T) {
	sentinel := newError("base")
	derived := sentinel.WithFailures(1).WithFailures(2)

	assert.Equal(t, "base (failures: 2)", derived.Error())
}

func TestError_WithFailures_PreservesWrapped(t *testing.T) {
	inner := fmt.Errorf("inner")
	derived := newError("base").Wrap(inner).WithFailures(2)

	assert.Equal(t, inner, errors.Unwrap(derived))
}

// ----------------------------------------------------------------------------
// Immutability Tests
// ----------------------------------------------------------------------------

func TestError_MethodsDoNotMutateSentinel(t *testing.T) {
	sentinel := newError("immutable")
	originalMsg := sentinel.Error()

	_ = sentinel.WithFailures(5)
	_ = sentinel.Wrap(fmt.Errorf("inner"))

	assert.Equal(t, originalMsg, sentinel.Error())
	assert.Nil(t, errors.Unwrap(sentinel))
}

// ----------------------------------------------------------------------------
// ErrRetriesExhausted Tests
// ----------------------------------------------------------------------------

func TestErrRetriesExhausted_MatchesItself(t *testing.T) {
	assert.True(t, errors.Is(ErrRetriesExhausted, ErrRetriesExhausted))
}

func TestErrRetriesExhausted_MatchesDerived(t *testing.T) {
	derived := ErrRetriesExhausted.WithFailures(10)
	assert.True(t, errors.Is(derived, ErrRetriesExhausted))
}

func TestErrRetriesExhausted_DoesNotMatchOtherSentinels(t *testing.T) {
	other := newError("healthcheck retries exhausted")
	assert.False(t, errors.Is(ErrRetriesExhausted, other))
}
