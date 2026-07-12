package main

import (
	"strings"
	"testing"
)

func TestRequiredFlags(t *testing.T) {
	if err := run([]string{}); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("missing flags should fail: %v", err)
	}
}
