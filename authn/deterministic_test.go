package authn_test

import (
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/authn"
	"github.com/withHumanAI/withHuman/authn/contracttest"
)

func TestDeterministicAuthenticatorContract(t *testing.T) {
	now := time.Unix(100, 0)
	contracttest.Authenticator(t, func() contracttest.RegisteringAuthenticator {
		return authn.NewDeterministicAuthenticator(func() time.Time { return now })
	})
}

func TestMemorySessionContract(t *testing.T) {
	contracttest.Sessions(t, func(now *time.Time) authn.SessionPort {
		return authn.NewMemorySessionPort(func() time.Time { return *now }, time.Hour)
	})
}
