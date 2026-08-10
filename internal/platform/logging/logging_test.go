package logging

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestJSONLoggerIncludesRequiredFields(t *testing.T) {
	var output bytes.Buffer
	logger, err := NewWithWriter("info", "api", &output)
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("started", "request_id", "request-1")
	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatalf("invalid JSON log: %v", err)
	}
	for _, field := range []string{"time", "level", "component", "msg", "request_id"} {
		if _, ok := entry[field]; !ok {
			t.Errorf("missing field %q", field)
		}
	}
}
