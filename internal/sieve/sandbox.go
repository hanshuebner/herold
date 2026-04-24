package sieve

import (
	"errors"
	"fmt"
)

// SandboxLimits bounds interpreter resource use so a hostile or buggy
// script cannot stall a delivery worker.
//
// The CPU bound is expressed as an instruction (operation) counter rather
// than wall-clock time: tests remain deterministic, and the counter is a
// tight enough proxy for runtime that a 100 000-op budget corresponds to
// well under 100 ms on a commodity core for Sieve workloads.
//
// Memory bounds cap the number of distinct variables, the maximum length
// of any single variable's string value, and the total bytes of variable
// storage. Each bound has a paired test that drives the interpreter
// over the bound and asserts the error.
type SandboxLimits struct {
	// MaxInstructions is the hard cap on evaluated command + test steps.
	// Exceeding it returns ErrInstructionBudget.
	MaxInstructions int
	// MaxVariables bounds the number of distinct variables the `set`
	// command may allocate in an invocation.
	MaxVariables int
	// MaxVariableBytes bounds the length in bytes of any one variable
	// value. `set` and string concatenation both check this.
	MaxVariableBytes int
	// MaxTotalVariableBytes bounds the aggregate bytes of all live
	// variables.
	MaxTotalVariableBytes int
	// MaxRedirects caps outbound redirect intents the script may record.
	MaxRedirects int
	// MaxNotifies caps enotify invocations recorded.
	MaxNotifies int
	// MaxOutcomeActions caps total actions appended to the Outcome.
	MaxOutcomeActions int
}

// DefaultSandboxLimits returns the conservative defaults used by the
// production delivery path.
func DefaultSandboxLimits() SandboxLimits {
	return SandboxLimits{
		MaxInstructions:       100_000,
		MaxVariables:          256,
		MaxVariableBytes:      4096,
		MaxTotalVariableBytes: 64 * 1024,
		MaxRedirects:          5,
		MaxNotifies:           2,
		MaxOutcomeActions:     64,
	}
}

// Sandbox errors. Callers use errors.Is to classify bounded-execution
// rejections.
var (
	// ErrInstructionBudget is returned when a script runs longer than
	// SandboxLimits.MaxInstructions.
	ErrInstructionBudget = errors.New("sieve: instruction budget exhausted")
	// ErrVariableBudget is returned when the script allocates more
	// variables than the sandbox permits.
	ErrVariableBudget = errors.New("sieve: variable budget exhausted")
	// ErrVariableSize is returned when a single variable value exceeds
	// MaxVariableBytes.
	ErrVariableSize = errors.New("sieve: variable value too large")
	// ErrTotalVariableBytes is returned when the aggregate bytes of all
	// variables exceeds MaxTotalVariableBytes.
	ErrTotalVariableBytes = errors.New("sieve: total variable bytes exceeded")
	// ErrRedirectBudget is returned when the script attempts more
	// redirects than MaxRedirects permits.
	ErrRedirectBudget = errors.New("sieve: redirect budget exhausted")
	// ErrNotifyBudget is returned when the script attempts more notifies
	// than MaxNotifies permits.
	ErrNotifyBudget = errors.New("sieve: notify budget exhausted")
	// ErrOutcomeActions is returned when the outcome accumulator exceeds
	// MaxOutcomeActions.
	ErrOutcomeActions = errors.New("sieve: outcome action budget exhausted")
)

// sandbox is the live per-invocation enforcement state.
type sandbox struct {
	limits SandboxLimits

	instructions int
	redirects    int
	notifies     int
	actions      int
	totalBytes   int
}

func newSandbox(l SandboxLimits) *sandbox { return &sandbox{limits: l} }

func (s *sandbox) tick() error {
	s.instructions++
	if s.instructions > s.limits.MaxInstructions {
		return fmt.Errorf("%w: %d", ErrInstructionBudget, s.instructions)
	}
	return nil
}

func (s *sandbox) recordRedirect() error {
	s.redirects++
	if s.redirects > s.limits.MaxRedirects {
		return ErrRedirectBudget
	}
	return nil
}

func (s *sandbox) recordNotify() error {
	s.notifies++
	if s.notifies > s.limits.MaxNotifies {
		return ErrNotifyBudget
	}
	return nil
}

func (s *sandbox) recordAction() error {
	s.actions++
	if s.actions > s.limits.MaxOutcomeActions {
		return ErrOutcomeActions
	}
	return nil
}

// checkVarStorage verifies an allocation of delta bytes fits under the
// total-bytes cap.
func (s *sandbox) checkVarStorage(delta int) error {
	if s.totalBytes+delta > s.limits.MaxTotalVariableBytes {
		return ErrTotalVariableBytes
	}
	s.totalBytes += delta
	return nil
}
