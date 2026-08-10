package main

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDecodeResponseUsesUnifiedEnvelope(t *testing.T) {
	response := &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"INVALID","message":"bad input"}}`))}
	err := decodeResponse(response, nil)
	if err == nil || err.Error() != "INVALID: bad input" {
		t.Fatalf("unexpected error: %v", err)
	}
}
