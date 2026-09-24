// Package sealbox seals small secrets with AES-256-GCM for storage at
// rest. The envelope is one version byte, the random nonce, then the
// ciphertext. Callers bind each ciphertext to its storage row through the
// additional authenticated data, so a sealed value copied into another
// row fails to open. The package never reads configuration; keys arrive
// parsed from the caller.
package sealbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

const (
	envelopeVersion = 1
	keySize         = 32
)

type Sealer struct {
	aead cipher.AEAD
}

// ParseKey builds a Sealer from a URL-safe base64 encoding of 32 random
// bytes.
func ParseKey(encoded string) (*Sealer, error) {
	key, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("sealbox: key is not URL-safe base64: %w", err)
	}
	if len(key) != keySize {
		return nil, fmt.Errorf("sealbox: key must be %d bytes, got %d", keySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("sealbox: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("sealbox: %w", err)
	}
	return &Sealer{aead: aead}, nil
}

func (s *Sealer) Seal(plaintext, associatedData []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("sealbox: %w", err)
	}
	envelope := make([]byte, 0, 1+len(nonce)+len(plaintext)+s.aead.Overhead())
	envelope = append(envelope, envelopeVersion)
	envelope = append(envelope, nonce...)
	return s.aead.Seal(envelope, nonce, plaintext, associatedData), nil
}

func (s *Sealer) Open(envelope, associatedData []byte) ([]byte, error) {
	if len(envelope) < 1+s.aead.NonceSize()+s.aead.Overhead() {
		return nil, fmt.Errorf("sealbox: envelope too short")
	}
	if envelope[0] != envelopeVersion {
		return nil, fmt.Errorf("sealbox: unknown envelope version %d", envelope[0])
	}
	nonce := envelope[1 : 1+s.aead.NonceSize()]
	plaintext, err := s.aead.Open(nil, nonce, envelope[1+s.aead.NonceSize():], associatedData)
	if err != nil {
		return nil, fmt.Errorf("sealbox: decryption failed")
	}
	return plaintext, nil
}
