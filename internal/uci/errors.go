package uci

// ContextErrorCode is one of the closed context-resolution outcomes.
type ContextErrorCode string

const (
	ContextRequired  ContextErrorCode = "CONTEXT_REQUIRED"
	ContextMismatch  ContextErrorCode = "CONTEXT_MISMATCH"
	PermissionDenied ContextErrorCode = "PERMISSION_DENIED"
)

// ContextError retains an internal cause while exposing only its closed outcome.
type ContextError struct {
	code  ContextErrorCode
	cause error
}

// Code returns the closed outcome code.
func (err *ContextError) Code() ContextErrorCode {
	if err == nil || !err.code.valid() {
		return ""
	}
	return err.code
}

// Error never discloses the wrapped diagnostic.
func (err *ContextError) Error() string {
	return string(err.Code())
}

// Unwrap preserves the diagnostic cause for trusted internal classification.
func (err *ContextError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

func (code ContextErrorCode) valid() bool {
	switch code {
	case ContextRequired, ContextMismatch, PermissionDenied:
		return true
	default:
		return false
	}
}

func newContextError(code ContextErrorCode, cause error) *ContextError {
	return &ContextError{code: code, cause: cause}
}
