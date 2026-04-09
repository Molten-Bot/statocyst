package api

import (
	"strings"
	"testing"
	"time"
)

func TestOpenClawWSPullTimeout(t *testing.T) {
	timeout, err := openClawWSPullTimeout(nil)
	if err != nil {
		t.Fatalf("unexpected error for nil timeout: %v", err)
	}
	if timeout != openClawWebSocketPullTimeoutDefault {
		t.Fatalf("expected default timeout %v, got %v", openClawWebSocketPullTimeoutDefault, timeout)
	}

	zero := 0
	timeout, err = openClawWSPullTimeout(&zero)
	if err != nil {
		t.Fatalf("unexpected error for zero timeout: %v", err)
	}
	if timeout != 0 {
		t.Fatalf("expected zero timeout duration, got %v", timeout)
	}

	max := maxPullTimeoutMS
	timeout, err = openClawWSPullTimeout(&max)
	if err != nil {
		t.Fatalf("unexpected error for max timeout: %v", err)
	}
	if timeout != time.Duration(max)*time.Millisecond {
		t.Fatalf("expected max timeout duration %v, got %v", time.Duration(max)*time.Millisecond, timeout)
	}

	negative := -1
	if _, err := openClawWSPullTimeout(&negative); err == nil || !strings.Contains(err.Error(), "between 0 and 30000") {
		t.Fatalf("expected bounds error for negative timeout, got %v", err)
	}

	tooHigh := maxPullTimeoutMS + 1
	if _, err := openClawWSPullTimeout(&tooHigh); err == nil || !strings.Contains(err.Error(), "between 0 and 30000") {
		t.Fatalf("expected bounds error for timeout above max, got %v", err)
	}
}
