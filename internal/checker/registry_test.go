package checker

import (
	"context"
	"testing"
)

type fakeChecker struct{ platform string }

func (f fakeChecker) Platform() string { return f.platform }
func (f fakeChecker) Check(_ context.Context, _ *Status) (Status, error) {
	return Status{Platform: f.platform}, nil
}

func TestRegisterAndNew(t *testing.T) {
	var gotFields Fields
	Register("test-register-and-new", func(f Fields) (Checker, error) {
		gotFields = f
		return fakeChecker{platform: "test-register-and-new"}, nil
	})

	chk, err := New("test-register-and-new", Fields{"hostname": "web01"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chk.Platform() != "test-register-and-new" {
		t.Fatalf("got platform %q", chk.Platform())
	}
	if gotFields["hostname"] != "web01" {
		t.Fatalf("factory did not receive the fields passed to New, got %#v", gotFields)
	}
}

func TestNewUnknownPlatformReturnsError(t *testing.T) {
	if _, err := New("bogus-platform-does-not-exist", Fields{}); err == nil {
		t.Fatal("expected an error for an unregistered platform")
	}
}

// TestRegisterDuplicatePanics locks in that a duplicate registration is a
// startup-time programming error (two packages registering the same
// name), not a runtime condition to handle gracefully -- same posture as
// e.g. http.ServeMux panicking on a duplicate pattern.
func TestRegisterDuplicatePanics(t *testing.T) {
	factory := func(Fields) (Checker, error) { return nil, nil }
	Register("test-register-duplicate-panics", factory)
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected a panic on duplicate registration")
		}
	}()
	Register("test-register-duplicate-panics", factory)
}
