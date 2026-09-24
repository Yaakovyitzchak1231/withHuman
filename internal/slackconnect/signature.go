package slackconnect

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

// verifyRequest authenticates the original bytes before either JSON or form
// decoding. Both Slack callback surfaces use the same signing secret and window.
func (s *Service) verifyRequest(timestamp, signature string, body []byte) error {
	if !s.Configured() || s.config.SigningSecret == "" || s.config.AppID == "" {
		return ErrNotConfigured
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return domain.ErrUnauthenticated
	}
	age := s.clock.Now().Sub(time.Unix(seconds, 0))
	if age < -5*time.Minute || age > 5*time.Minute {
		return domain.ErrUnauthenticated
	}
	mac := hmac.New(sha256.New, []byte(s.config.SigningSecret))
	mac.Write([]byte("v0:" + timestamp + ":"))
	mac.Write(body)
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(signature), []byte(expected)) {
		return domain.ErrUnauthenticated
	}
	return nil
}
