package approvalblock_test

import (
	"encoding/json"
	"testing"

	"github.com/withHumanAI/withHuman/approvalblock"
)

// Asking the owner needs no path: without one it falls back to the
// pipeline default. A path, when given, must still be key-shaped.
func TestValidateRouteChecksOnlyThePathShape(t *testing.T) {
	if err := approvalblock.ValidateRoute("Not A Key", false); err == nil {
		t.Fatal("a malformed path key was accepted")
	}
	for _, route := range []struct {
		path string
		ask  bool
	}{{"", false}, {"", true}, {"platform", false}, {"platform", true}} {
		if err := approvalblock.ValidateRoute(route.path, route.ask); err != nil {
			t.Fatalf("route %+v: %v", route, err)
		}
	}
}

// ask_agent_owner decodes strictly beside escalation_path in every core
// config that names a route, and survives a round trip.
func TestOwnerRoutesRoundTripInEveryRoutedConfig(t *testing.T) {
	documents := map[string]string{
		"outcome": `{"id":"ask","name":"Ask","type":"outcome","enabled":true,"timeout":"50ms","max_attempts":1,
			"config":{"outcome":"human","reason":"A person decides.","escalation_path":"platform","ask_agent_owner":true}}`,
		"webhook": `{"id":"ask","name":"Ask","type":"webhook","enabled":true,"timeout":"5s","max_attempts":1,
			"config":{"endpoint_key":"fraud","allowed_outcomes":["next","human"],
			"include":{"agent_reasoning":false,"context":false,"previous_metadata":false},
			"reason":"The service asked for a person.","escalation_path":"platform","ask_agent_owner":true}}`,
		"branch": `{"id":"ask","name":"Ask","type":"branch","enabled":true,"timeout":"50ms","max_attempts":1,
			"config":{"when":{"id":"6c1f0e2a-4b7d-4e35-9a1c-8d2b5f3e7a91","field":"/request/tool","operator":"equals","value":"push"},
			"blocks":[{"id":"inside","name":"Inside","type":"outcome","enabled":true,"timeout":"50ms","max_attempts":1,
				"config":{"outcome":"approve","reason":"Fine."}}],
			"reason":"Nothing decided.","escalation_path":"platform","ask_agent_owner":true}}`,
	}
	for name, document := range documents {
		t.Run(name, func(t *testing.T) {
			var definition approvalblock.Definition
			if err := json.Unmarshal([]byte(document), &definition); err != nil {
				t.Fatal(err)
			}
			if err := definition.Validate(); err != nil {
				t.Fatal(err)
			}
			if !definition.AskAgentOwner() || definition.EscalationPath() != "platform" {
				t.Fatalf("route = %q, ask %v", definition.EscalationPath(), definition.AskAgentOwner())
			}
			encoded, err := json.Marshal(definition)
			if err != nil {
				t.Fatal(err)
			}
			var again approvalblock.Definition
			if err := json.Unmarshal(encoded, &again); err != nil || !again.AskAgentOwner() {
				t.Fatalf("round trip lost the owner route: %s, %v", encoded, err)
			}
		})
	}
}

func TestAnOwnerRouteWithoutAPathIsValid(t *testing.T) {
	var definition approvalblock.Definition
	document := `{"id":"ask","name":"Ask","type":"outcome","enabled":true,"timeout":"50ms","max_attempts":1,
		"config":{"outcome":"human","reason":"A person decides.","ask_agent_owner":true}}`
	if err := json.Unmarshal([]byte(document), &definition); err != nil {
		t.Fatal(err)
	}
	if err := definition.Validate(); err != nil || !definition.AskAgentOwner() || definition.EscalationPath() != "" {
		t.Fatalf("an owner route without a path = ask %v, path %q, %v", definition.AskAgentOwner(), definition.EscalationPath(), err)
	}
}
