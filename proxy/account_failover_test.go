package proxy

import (
	"context"
	"errors"
	"testing"
)

func TestAccountFailureClassifiers(t *testing.T) {
	tests := []struct {
		name string
		fn   func(string) bool
		msg  string
	}{
		{name: "quota", fn: isQuotaErrorMessage, msg: "HTTP 429: quota exhausted"},
		{name: "overage", fn: isOverageErrorMessage, msg: "HTTP 402 from Kiro IDE: OVERAGE limit exceeded"},
		{name: "suspension", fn: isSuspensionErrorMessage, msg: "Your User ID temporarily is suspended"},
		{name: "profile", fn: isProfileUnavailableErrorMessage, msg: "no available Kiro profile"},
		{name: "auth", fn: isAuthErrorMessage, msg: "Authentication failed - token invalid or expired"},
	}

	for _, tc := range tests {
		if !tc.fn(tc.msg) {
			t.Fatalf("%s classifier did not match %q", tc.name, tc.msg)
		}
	}
}

type timeoutTestErr struct{}

func (timeoutTestErr) Error() string   { return "timeout awaiting response headers" }
func (timeoutTestErr) Timeout() bool   { return true }
func (timeoutTestErr) Temporary() bool { return true }

func TestShouldPenalizeAccountForError(t *testing.T) {
	if shouldPenalizeAccountForError(nil) {
		t.Fatal("nil error should not penalize")
	}
	if shouldPenalizeAccountForError(context.Canceled) {
		t.Fatal("context cancellation should not penalize")
	}
	if !shouldPenalizeAccountForError(ErrStreamIdleTimeout) {
		t.Fatal("stream idle timeout should still penalize")
	}
	if !shouldPenalizeAccountForError(context.DeadlineExceeded) {
		t.Fatal("deadline exceeded should still penalize")
	}
	if !shouldPenalizeAccountForError(timeoutTestErr{}) {
		t.Fatal("transport timeout should still penalize")
	}
	if !shouldPenalizeAccountForError(errors.New("HTTP 500 upstream failure")) {
		t.Fatal("generic upstream failure should penalize")
	}
}
