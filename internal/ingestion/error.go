package ingestion

import (
	"errors"
	"fmt"
)

type ProcessingError struct {
	Code      string
	Message   string
	Retryable bool
	cause     error
}

func (e *ProcessingError) Error() string {
	if e.cause == nil {
		return e.Code
	}
	return fmt.Sprintf("%s: %v", e.Code, e.cause)
}

func (e *ProcessingError) Unwrap() error { return e.cause }

func permanent(code, message string, cause error) error {
	return &ProcessingError{Code: code, Message: message, cause: cause}
}

func transient(code, message string, cause error) error {
	return &ProcessingError{Code: code, Message: message, Retryable: true, cause: cause}
}

func classify(err error, fallbackCode, fallbackMessage string, retryable bool) *ProcessingError {
	var processing *ProcessingError
	if errors.As(err, &processing) {
		return processing
	}
	return &ProcessingError{Code: fallbackCode, Message: fallbackMessage, Retryable: retryable, cause: err}
}
