package webhookdelivery

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Standard Webhooks headers, the signing scheme AAP asynchronous delivery
// uses. The message signed is "<id>.<unix seconds>.<raw body>" and the key
// is the base64 payload of a whsec_ secret.
const (
	StandardIDHeader        = "webhook-id"
	StandardTimestampHeader = "webhook-timestamp"
	StandardSignatureHeader = "webhook-signature"
	standardSecretPrefix    = "whsec_"
	standardKeyBytes        = 32
)

var ErrInvalidStandardSecret = errors.New("signing secret must be whsec_ followed by 32 bytes of canonical padded base64")

// StandardSigningKey decodes a whsec_ secret into its 32-byte key.
// Canonical encoding is required so one key has exactly one spelling.
func StandardSigningKey(secret string) ([]byte, error) {
	encoded, found := strings.CutPrefix(secret, standardSecretPrefix)
	if !found {
		return nil, ErrInvalidStandardSecret
	}
	key, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(key) != standardKeyBytes || base64.StdEncoding.EncodeToString(key) != encoded {
		return nil, ErrInvalidStandardSecret
	}
	return key, nil
}

// NewStandardSecret generates a whsec_ secret: 32 random bytes in
// canonical padded base64.
func NewStandardSecret() (string, error) {
	key := make([]byte, standardKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	return standardSecretPrefix + base64.StdEncoding.EncodeToString(key), nil
}

// StandardHeaders signs body for one attempt at timestamp and returns
// the three Standard Webhooks headers.
func StandardHeaders(secret, messageID string, timestamp time.Time, body []byte) (http.Header, error) {
	key, err := StandardSigningKey(secret)
	if err != nil {
		return nil, err
	}
	seconds := strconv.FormatInt(timestamp.Unix(), 10)
	headers := http.Header{}
	headers.Set(StandardIDHeader, messageID)
	headers.Set(StandardTimestampHeader, seconds)
	headers.Set(StandardSignatureHeader, standardSignature(key, messageID, seconds, body))
	return headers, nil
}

func standardSignature(key []byte, messageID, seconds string, body []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(messageID + "." + seconds + "."))
	mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
