package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

func newRunID(mode string) string {
	return fmt.Sprintf("%s-%s-%s", sanitizeIDPart(mode, "run"), time.Now().UTC().Format("20060102T150405Z"), randomIDSuffix())
}

func newCycleID(runID string) string {
	return fmt.Sprintf("%s-cycle-%s", sanitizeIDPart(runID, "run"), randomIDSuffix())
}

func sanitizeIDPart(raw string, fallback string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return fallback
	}
	builder := strings.Builder{}
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '-' || r == '_':
			builder.WriteRune(r)
		}
	}
	if builder.Len() == 0 {
		return fallback
	}
	return builder.String()
}

func randomIDSuffix() string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%08x", time.Now().UTC().UnixNano())
	}
	return hex.EncodeToString(buf)
}
