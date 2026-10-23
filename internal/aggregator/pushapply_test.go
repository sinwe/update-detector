package aggregator

import (
	"errors"
	"testing"
)

func TestPushApplyRejectsUnknownAgent(t *testing.T) {
	s, _ := newTestServerWithSecret(t, "secret")

	_, _, err := s.PushApply("nope", ActionUpgrade, nil)
	if !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("err = %v, want ErrAgentNotFound", err)
	}
}

func TestPushApplyRejectsInvalidAction(t *testing.T) {
	s, reg := newTestServerWithSecret(t, "secret")
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	cases := []struct {
		name     string
		action   ActionType
		packages []string
	}{
		// Recheck deliberately has its own no-secret-required endpoint.
		{"recheck", ActionRecheck, nil},
		{"packages without a list", ActionPackages, nil},
		{"unknown type", ActionType("nonsense"), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := s.PushApply("a1", tc.action, tc.packages)
			if !errors.Is(err, ErrInvalidAction) {
				t.Fatalf("err = %v, want ErrInvalidAction", err)
			}
		})
	}
}

func TestPushApplyFailsWithoutConnectedCompanion(t *testing.T) {
	s, reg := newTestServerWithSecret(t, "secret")
	approvedAgent(t, s, reg, "a1", "web01", "tok")

	if _, _, err := s.PushApply("a1", ActionUpgrade, nil); err == nil {
		t.Fatal("expected an error with no connected companion")
	}
}
