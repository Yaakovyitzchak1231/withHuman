package webhookdelivery

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// SignatureHeader carries "t=<unix seconds>,v1=<hex HMAC-SHA256>" where
	// the message is "<t>.<raw body>" and the key is the endpoint secret.
	SignatureHeader = "WithHuman-Signature"
	// DeliveryIDHeader is unique per attempt.
	DeliveryIDHeader = "WithHuman-Delivery-Id"
	// IdempotencyKeyHeader is stable across the attempts of one evaluation.
	IdempotencyKeyHeader = "WithHuman-Idempotency-Key"
	// SignatureTolerance is how far a receiver should let the signed
	// timestamp drift from its own clock.
	SignatureTolerance = 5 * time.Minute
)

var ErrInvalidSignature = errors.New("webhook signature is invalid")

// Sign produces the signature header value for body at timestamp.
func Sign(secret string, timestamp time.Time, body []byte) string {
	t := strconv.FormatInt(timestamp.Unix(), 10)
	return "t=" + t + ",v1=" + hex.EncodeToString(mac(secret, t, body))
}

// VerifySignature is the receiver-side check: it parses the header,
// rejects timestamps outside the tolerance around now, and compares the
// HMAC in constant time. Receivers written in Go can use it directly; the
// tests use it as the reference verifier.
func VerifySignature(secret, header string, body []byte, now time.Time, tolerance time.Duration) error {
	var timestamp, signature string
	for _, part := range strings.Split(header, ",") {
		key, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if !found {
			continue
		}
		switch key {
		case "t":
			timestamp = value
		case "v1":
			signature = value
		}
	}
	if timestamp == "" || signature == "" {
		return fmt.Errorf("signature header is missing t or v1: %w", ErrInvalidSignature)
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("signature timestamp is not an integer: %w", ErrInvalidSignature)
	}
	issued := time.Unix(seconds, 0)
	if issued.Before(now.Add(-tolerance)) || issued.After(now.Add(tolerance)) {
		return fmt.Errorf("signature timestamp is outside the acceptance window: %w", ErrInvalidSignature)
	}
	provided, err := hex.DecodeString(signature)
	if err != nil || !hmac.Equal(mac(secret, timestamp, body), provided) {
		return ErrInvalidSignature
	}
	return nil
}

func mac(secret, timestamp string, body []byte) []byte {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(timestamp))
	h.Write([]byte("."))
	h.Write(body)
	return h.Sum(nil)
}
