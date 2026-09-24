package sealbox

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func testKey(t *testing.T) string {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(key)
}

func TestSealRoundTrip(t *testing.T) {
	sealer, err := ParseKey(testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte(`{"headers":{"Authorization":"Bearer sk_live_x"}}`)
	associated := []byte("org|server|static_headers")

	envelope, err := sealer.Seal(plaintext, associated)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := sealer.Open(envelope, associated)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Fatalf("roundtrip mismatch: %q", opened)
	}

	second, err := sealer.Seal(plaintext, associated)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(envelope, second) {
		t.Fatal("two seals produced identical envelopes; nonce reuse")
	}
}

func TestOpenRejectsTamperAndWrongContext(t *testing.T) {
	sealer, err := ParseKey(testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := sealer.Seal([]byte("secret"), []byte("row-a"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := sealer.Open(envelope, []byte("row-b")); err == nil {
		t.Fatal("opened with mismatched associated data")
	}

	tampered := append([]byte(nil), envelope...)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := sealer.Open(tampered, []byte("row-a")); err == nil {
		t.Fatal("opened a tampered envelope")
	}

	if _, err := sealer.Open(envelope[:4], []byte("row-a")); err == nil {
		t.Fatal("opened a truncated envelope")
	}

	versioned := append([]byte(nil), envelope...)
	versioned[0] = 9
	if _, err := sealer.Open(versioned, []byte("row-a")); err == nil {
		t.Fatal("opened an unknown envelope version")
	}

	other, err := ParseKey(testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Open(envelope, []byte("row-a")); err == nil {
		t.Fatal("opened with the wrong key")
	}
}

func TestParseKeyRejectsBadInput(t *testing.T) {
	for name, encoded := range map[string]string{
		"empty":      "",
		"not-base64": "!!!",
		"short":      base64.RawURLEncoding.EncodeToString(make([]byte, 16)),
	} {
		if _, err := ParseKey(encoded); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}
}
