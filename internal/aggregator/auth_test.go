package aggregator

import "testing"

func TestHashTokenDeterministicAndDistinct(t *testing.T) {
	a := hashToken("secret-1")
	b := hashToken("secret-1")
	c := hashToken("secret-2")

	if a != b {
		t.Fatal("expected same token to hash identically")
	}
	if a == c {
		t.Fatal("expected different tokens to hash differently")
	}
}

func TestTokensMatch(t *testing.T) {
	h := hashToken("secret")
	if !tokensMatch(hashToken("secret"), h) {
		t.Fatal("expected matching tokens to compare equal")
	}
	if tokensMatch(hashToken("other"), h) {
		t.Fatal("expected non-matching tokens to compare unequal")
	}
}
