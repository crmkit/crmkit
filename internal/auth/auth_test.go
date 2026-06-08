package auth

import "testing"

func TestHashCodeKeyed(t *testing.T) {
	a := HashCode("secret1", "u@x.com", "123456")
	if a != HashCode("secret1", "u@x.com", "123456") {
		t.Fatal("identical inputs must hash the same")
	}
	if a == HashCode("secret2", "u@x.com", "123456") {
		t.Fatal("a different secret must change the hash (keyed)")
	}
	if a == HashCode("secret1", "v@x.com", "123456") {
		t.Fatal("a different email must change the hash")
	}
	if a == HashCode("secret1", "u@x.com", "654321") {
		t.Fatal("a different code must change the hash")
	}
}

func TestHashStepUpScoped(t *testing.T) {
	a := HashStepUp("s", "u1", "workspace.delete", "ws1", "111111")
	if a == HashStepUp("s", "u1", "member.promote", "ws1", "111111") {
		t.Fatal("a code must not verify across actions")
	}
	if a == HashStepUp("s", "u1", "workspace.delete", "ws2", "111111") {
		t.Fatal("a code must not verify across targets")
	}
	if a == HashStepUp("k2", "u1", "workspace.delete", "ws1", "111111") {
		t.Fatal("a different secret must change the hash (keyed)")
	}
}

func TestGenerateSecret(t *testing.T) {
	s := GenerateSecret()
	if len(s) != 64 { // 32 bytes hex
		t.Fatalf("expected 64 hex chars, got %d", len(s))
	}
	if GenerateSecret() == s {
		t.Fatal("secrets must be random")
	}
}
