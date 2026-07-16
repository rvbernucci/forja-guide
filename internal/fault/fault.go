// Package fault defines stable machine-readable application errors.
package fault

import (
	"errors"
	"fmt"
)

// Code identifies a stable error category at trust boundaries.
type Code string

const (
	CodeInvalidArgument Code = "invalid_argument"
	CodeNotFound        Code = "not_found"
	CodeConflict        Code = "conflict"
	CodeUnavailable     Code = "unavailable"
	CodeInternal        Code = "internal"
)

// CodeOf returns the stable code from an error chain.
func CodeOf(err error) Code {
	var target *Error
	if errors.As(err, &target) {
		return target.Code
	}
	return CodeInternal
}

// Error carries a stable code while preserving the underlying cause.
type Error struct {
	Code    Code
	Op      string
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Op == "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%s: %s: %s", e.Code, e.Op, e.Message)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// New creates a categorized application error.
func New(code Code, op, message string) error {
	return &Error{Code: code, Op: op, Message: message}
}

// Wrap categorizes an existing error while preserving its cause.
func Wrap(code Code, op, message string, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Code: code, Op: op, Message: message, Err: err}
}

// IsCode reports whether an error chain contains the requested code.
func IsCode(err error, code Code) bool {
	var target *Error
	return errors.As(err, &target) && target.Code == code
}
