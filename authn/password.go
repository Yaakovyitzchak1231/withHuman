package authn

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	minimumPasswordCharacters = 12
	maximumPasswordBytes      = 1024
)

type PasswordHasher interface {
	Hash(password string) (string, error)
	Verify(encodedHash, password string) (bool, error)
}

type Argon2idPasswordHasher struct {
	memory      uint32
	iterations  uint32
	parallelism uint8
	saltLength  uint32
	keyLength   uint32
}

func NewArgon2idPasswordHasher() *Argon2idPasswordHasher {
	return &Argon2idPasswordHasher{
		memory:      64 * 1024,
		iterations:  3,
		parallelism: 2,
		saltLength:  16,
		keyLength:   32,
	}
}

func (hasher *Argon2idPasswordHasher) Hash(password string) (string, error) {
	salt := make([]byte, hasher.saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, hasher.iterations, hasher.memory, hasher.parallelism, hasher.keyLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, hasher.memory, hasher.iterations, hasher.parallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

func (hasher *Argon2idPasswordHasher) Verify(encodedHash, password string) (bool, error) {
	parameters, salt, expected, err := parseArgon2idHash(encodedHash)
	if err != nil {
		return false, err
	}
	actual := argon2.IDKey([]byte(password), salt, parameters.iterations, parameters.memory, parameters.parallelism, uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}

func parseArgon2idHash(encoded string) (Argon2idPasswordHasher, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return Argon2idPasswordHasher{}, nil, nil, fmt.Errorf("invalid Argon2id password hash")
	}
	version, err := parseArgon2idParameter(parts[2], "v=", 8)
	if err != nil || version != argon2.Version {
		return Argon2idPasswordHasher{}, nil, nil, fmt.Errorf("unsupported Argon2id password hash version")
	}
	parameterParts := strings.Split(parts[3], ",")
	if len(parameterParts) != 3 {
		return Argon2idPasswordHasher{}, nil, nil, fmt.Errorf("invalid Argon2id password hash parameters")
	}
	memory, memoryErr := parseArgon2idParameter(parameterParts[0], "m=", 32)
	iterations, iterationsErr := parseArgon2idParameter(parameterParts[1], "t=", 32)
	parallelism, parallelismErr := parseArgon2idParameter(parameterParts[2], "p=", 8)
	if memoryErr != nil || iterationsErr != nil || parallelismErr != nil {
		return Argon2idPasswordHasher{}, nil, nil, fmt.Errorf("invalid Argon2id password hash parameters")
	}
	parameters := Argon2idPasswordHasher{memory: uint32(memory), iterations: uint32(iterations), parallelism: uint8(parallelism)}
	if parameters.memory < 8*1024 || parameters.memory > 1024*1024 || parameters.iterations == 0 || parameters.iterations > 10 || parameters.parallelism == 0 || parameters.parallelism > 16 {
		return Argon2idPasswordHasher{}, nil, nil, fmt.Errorf("Argon2id password hash parameters are outside supported bounds")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 16 || len(salt) > 64 {
		return Argon2idPasswordHasher{}, nil, nil, fmt.Errorf("invalid Argon2id password salt")
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(expected) < 16 || len(expected) > 64 {
		return Argon2idPasswordHasher{}, nil, nil, fmt.Errorf("invalid Argon2id password digest")
	}
	return parameters, salt, expected, nil
}

func parseArgon2idParameter(value, prefix string, bitSize int) (uint64, error) {
	if !strings.HasPrefix(value, prefix) || len(value) == len(prefix) {
		return 0, fmt.Errorf("missing %s parameter", strings.TrimSuffix(prefix, "="))
	}
	return strconv.ParseUint(value[len(prefix):], 10, bitSize)
}
