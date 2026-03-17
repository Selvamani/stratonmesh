package errors

import "fmt"

// ErrCode represents a typed error category
type ErrCode string

const (
	ErrValidation     ErrCode = "VALIDATION"
	ErrNotFound       ErrCode = "NOT_FOUND"
	ErrConflict       ErrCode = "CONFLICT"
	ErrPolicyDenied   ErrCode = "POLICY_DENIED"
	ErrAdapterFailure ErrCode = "ADAPTER_FAILURE"
	ErrSchedule       ErrCode = "SCHEDULE_FAILED"
	ErrTimeout        ErrCode = "TIMEOUT"
	ErrInternal       ErrCode = "INTERNAL"
)

// Error is the standard StratonMesh error with code, message, and optional cause.
type Error struct {
	Code    ErrCode
	Message string
	Cause   error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

// Convenience constructors

func Validation(msg string, args ...interface{}) *Error {
	return &Error{Code: ErrValidation, Message: fmt.Sprintf(msg, args...)}
}

func NotFound(msg string, args ...interface{}) *Error {
	return &Error{Code: ErrNotFound, Message: fmt.Sprintf(msg, args...)}
}

func Conflict(msg string, args ...interface{}) *Error {
	return &Error{Code: ErrConflict, Message: fmt.Sprintf(msg, args...)}
}

func PolicyDenied(msg string, args ...interface{}) *Error {
	return &Error{Code: ErrPolicyDenied, Message: fmt.Sprintf(msg, args...)}
}

func AdapterFailure(cause error, msg string, args ...interface{}) *Error {
	return &Error{Code: ErrAdapterFailure, Message: fmt.Sprintf(msg, args...), Cause: cause}
}

func ScheduleFailed(msg string, args ...interface{}) *Error {
	return &Error{Code: ErrSchedule, Message: fmt.Sprintf(msg, args...)}
}

func Internal(cause error, msg string, args ...interface{}) *Error {
	return &Error{Code: ErrInternal, Message: fmt.Sprintf(msg, args...), Cause: cause}
}
