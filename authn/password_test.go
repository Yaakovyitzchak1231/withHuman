package authn

import (
	"strings"
	"testing"
)

func TestArgon2idPasswordHasherRoundTrip(t *testing.T) {
	hasher := &Argon2idPasswordHasher{memory: 8 * 1024, iterations: 1, parallelism: 1, saltLength: 16, keyLength: 32}
	encoded, err := hasher.Hash("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$") {
		t.Fatalf("unexpected encoding %q", encoded)
	}
	valid, err := hasher.Verify(encoded, "correct horse battery staple")
	if err != nil || !valid {
		t.Fatalf("correct password was rejected: valid=%t error=%v", valid, err)
	}
	valid, err = hasher.Verify(encoded, "incorrect password")
	if err != nil || valid {
		t.Fatalf("incorrect password was accepted: valid=%t error=%v", valid, err)
	}
}

func TestArgon2idPasswordHasherRejectsMalformedEncoding(t *testing.T) {
	hasher := NewArgon2idPasswordHasher()
	for _, encoded := range []string{
		"",
		"$argon2i$v=19$m=65536,t=3,p=2$salt$hash",
		"$argon2id$v=18$m=65536,t=3,p=2$salt$hash",
		"$argon2id$v=19junk$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0c2FsdA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=65536,t=3,p=2junk$c2FsdHNhbHRzYWx0c2FsdA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	} {
		if _, err := hasher.Verify(encoded, "password"); err == nil {
			t.Fatalf("malformed encoding %q was accepted", encoded)
		}
	}
}

func TestValidateNewPassword(t *testing.T) {
	if err := validateNewPassword("short"); err == nil {
		t.Fatal("short password was accepted")
	}
	if err := validateNewPassword("correct horse battery staple"); err != nil {
		t.Fatalf("valid password was rejected: %v", err)
	}
}
