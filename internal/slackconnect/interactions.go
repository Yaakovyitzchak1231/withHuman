package slackconnect

import (
	"encoding/json"
	"net/url"

	"github.com/withHumanAI/withHuman/domain"
)

// ReceiveInteraction validates link-button callbacks without performing I/O or
// recording a decision. Slack requires acknowledgment even for URL buttons.
func (s *Service) ReceiveInteraction(timestamp, signature string, body []byte) error {
	if err := s.verifyRequest(timestamp, signature, body); err != nil {
		return err
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return domain.ErrInvalid
	}
	payloads := form["payload"]
	if len(payloads) != 1 || payloads[0] == "" {
		return domain.ErrInvalid
	}
	var envelope *struct {
		Type  string `json:"type"`
		AppID string `json:"api_app_id"`
	}
	if err := json.Unmarshal([]byte(payloads[0]), &envelope); err != nil || envelope == nil {
		return domain.ErrInvalid
	}
	if envelope.AppID != s.config.AppID {
		return domain.ErrUnauthenticated
	}
	if envelope.Type != "block_actions" {
		return domain.ErrInvalid
	}
	return nil
}
