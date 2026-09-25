package webhookdelivery

import (
	"encoding/base64"
	"testing"
	"time"
)

func TestStandardSignatureMatchesTheStandardWebhooksVector(t *testing.T) {
	// The Standard Webhooks reference vector. Its 24-byte key is shorter
	// than AAP allows, so it exercises the signature, not key parsing.
	key, err := base64.StdEncoding.DecodeString("MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw")
	if err != nil {
		t.Fatal(err)
	}
	got := standardSignature(key, "msg_p5jXN8AQM9LWM0D4loKWxJek", "1614265330", []byte(`{"test": 2432232314}`))
	if want := "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE="; got != want {
		t.Fatalf("signature = %q, want %q", got, want)
	}
}

func TestStandardHeadersCarryTheAttempt(t *testing.T) {
	secret := "whsec_" + base64.StdEncoding.EncodeToString(make([]byte, 32))
	headers, err := StandardHeaders(secret, "event-1", time.Unix(1789560121, 0), []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if headers.Get(StandardIDHeader) != "event-1" || headers.Get(StandardTimestampHeader) != "1789560121" ||
		headers.Get(StandardSignatureHeader) != standardSignature(make([]byte, 32), "event-1", "1789560121", []byte(`{}`)) {
		t.Fatalf("headers: %v", headers)
	}
}

func TestStandardSigningKeyRejectsMalformedSecrets(t *testing.T) {
	for _, secret := range []string{
		"", "whsec_", "whsec_not base64", "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw",
		"sk_" + base64.StdEncoding.EncodeToString(make([]byte, 32)),
		"whsec_" + base64.RawStdEncoding.EncodeToString(make([]byte, 32)),
	} {
		if _, err := StandardSigningKey(secret); err == nil {
			t.Fatalf("%q must be rejected", secret)
		}
	}
}
