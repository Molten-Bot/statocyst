package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSanitizeErrorHandlesContextSentinels(t *testing.T) {
	if got := SanitizeError(nil); got != "" {
		t.Fatalf("expected empty string for nil error, got %q", got)
	}

	canceled := fmt.Errorf("wrapped: %w", context.Canceled)
	if got := SanitizeError(canceled); got != "request canceled" {
		t.Fatalf("expected request canceled, got %q", got)
	}

	timedOut := errors.Join(errors.New("outer"), context.DeadlineExceeded)
	if got := SanitizeError(timedOut); got != "request timed out" {
		t.Fatalf("expected request timed out, got %q", got)
	}
}

func TestSanitizeErrorDelegatesToSanitizeErrorText(t *testing.T) {
	if got := SanitizeError(errors.New("dial tcp 127.0.0.1:443: connect: connection refused")); got != "connection failed" {
		t.Fatalf("expected connection failed summary, got %q", got)
	}
}

func TestSanitizeErrorWithDetailHandlesContextSentinels(t *testing.T) {
	if got := SanitizeErrorWithDetail(nil); got != "" {
		t.Fatalf("expected empty string for nil error, got %q", got)
	}

	canceled := fmt.Errorf("wrapped: %w", context.Canceled)
	if got := SanitizeErrorWithDetail(canceled); got != "request canceled" {
		t.Fatalf("expected request canceled, got %q", got)
	}

	timedOut := fmt.Errorf("wrapped: %w", context.DeadlineExceeded)
	if got := SanitizeErrorWithDetail(timedOut); got != "request timed out" {
		t.Fatalf("expected request timed out, got %q", got)
	}
}

func TestSanitizeErrorWithDetailDelegatesToTextAndDetail(t *testing.T) {
	input := `request failed status 500 body={"request_id":"req-123","cf-ray":"ray-abc"}`
	got := SanitizeErrorWithDetail(errors.New(input))
	if !strings.HasPrefix(got, "request failed") {
		t.Fatalf("expected request failed summary, got %q", got)
	}
	if !strings.Contains(got, "status=500") || !strings.Contains(got, "request_id=req-123") || !strings.Contains(got, "cf_ray=ray-abc") {
		t.Fatalf("expected structured detail fields in output, got %q", got)
	}
}
