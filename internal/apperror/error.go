package apperror

import (
	"errors"
	"fmt"
)

type Error struct {
	Code       string
	Message    string
	HTTPStatus int
	cause      error
}

func New(status int, code, message string) *Error {
	return &Error{Code: code, Message: message, HTTPStatus: status}
}

func Wrap(status int, code, message string, cause error) *Error {
	return &Error{Code: code, Message: message, HTTPStatus: status, cause: cause}
}

func (e *Error) Error() string {
	if e.cause == nil {
		return e.Code
	}
	return fmt.Sprintf("%s: %v", e.Code, e.cause)
}

func (e *Error) Unwrap() error { return e.cause }

func As(err error) (*Error, bool) {
	var appErr *Error
	return appErr, errors.As(err, &appErr)
}
