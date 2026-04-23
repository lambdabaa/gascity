package main

import (
	"testing"
	"time"
)

func TestProviderOpTimeout_LongRunningLifecycleOps(t *testing.T) {
	for _, op := range []string{"start", "init", "recover"} {
		if got := providerOpTimeout(op); got != 120*time.Second {
			t.Fatalf("providerOpTimeout(%q) = %s, want 120s", op, got)
		}
	}
}

func TestProviderOpTimeout_Default(t *testing.T) {
	if got := providerOpTimeout("health"); got != 30*time.Second {
		t.Fatalf("providerOpTimeout(%q) = %s, want 30s", "health", got)
	}
}
