package main

import (
	"testing"
	"time"
)

func TestRecoveryWindowOutlastsConfiguredWork(t *testing.T) {
	cases := []struct{ requested, normal, hang, want time.Duration }{
		{45 * time.Second, 2 * time.Second, 30 * time.Second, 45 * time.Second},
		{45 * time.Second, 2 * time.Second, 60 * time.Second, 75 * time.Second},
		{45 * time.Second, 90 * time.Second, 30 * time.Second, 105 * time.Second},
		{120 * time.Second, 2 * time.Second, 30 * time.Second, 120 * time.Second},
	}
	for _, c := range cases {
		if got := recoveryWindow(c.requested, c.normal, c.hang); got != c.want {
			t.Errorf("recovery %s shorter than configured work: want %s", got, c.want)
		}
	}
}
