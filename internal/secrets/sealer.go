// Package secrets seals credentials that must be stored reversibly, such
// as a Slack bot token. Hashed tokens elsewhere in the system need no
// sealing; this exists only for material the system has to present back
// to a provider later. The key comes from the deployment environment, the
// environment-key adapter the provider architecture names as the open
// edition's default ahead of an external secret manager.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"github.com/withHumanAI/withHuman/domain"
)

// Sealer encrypts and decrypts small secrets with AES-256-GCM.
type Sealer struct {
	aead cipher.AEAD
}

// NewSealer derives a fixed-size key from the configured secret. Hashing
// the input means operators can paste any sufficiently long string rather
// than exactly 32 bytes, while short keys are still refused.
func NewSealer(key string) (*Sealer, error) {
	if len(key) < 32 {
		return nil, fmt.Errorf("secrets key must be at least 32 characters: %w", domain.ErrInvalid)
	}
	derived := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(derived[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

// Seal returns nonce plus ciphertext. The label is bound as associated
// data, so a value sealed for one purpose cannot be replayed as another.
func (s *Sealer) Seal(label, plaintext string) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, []byte(plaintext), []byte(label)), nil
}

func (s *Sealer) Open(label string, sealed []byte) (string, error) {
	size := s.aead.NonceSize()
	if len(sealed) < size {
		return "", errors.New("sealed value is too short")
	}
	plaintext, err := s.aead.Open(nil, sealed[:size], sealed[size:], []byte(label))
	if err != nil {
		return "", fmt.Errorf("sealed value did not open: %w", err)
	}
	return string(plaintext), nil
}
