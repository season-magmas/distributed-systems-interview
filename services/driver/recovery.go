package main

import "time"

func recoveryWindow(requested, normal, hang time.Duration) time.Duration {
	minimum := max(normal, hang) + 15*time.Second
	return max(requested, minimum)
}
