package main

import (
	"crypto/sha256"
	"os"
	"time"
)

func hashBytes(b []byte) [32]byte { return sha256.Sum256(b) }
func recoveryTimeout() time.Duration {
	d := 45 * time.Second
	if raw := os.Getenv("RECOVERY_TIMEOUT"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > d {
			d = parsed
		}
	}
	return d
}
