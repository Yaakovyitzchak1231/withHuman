package approvalblock_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/approvalblock"
)

type editionConfig struct {
	Instructions string `json:"instructions"`
	Reason       string `json:"reason"`
	Path         string `json:"escalation_path,omitempty"`
}

func (config *editionConfig) Validate() error {
	if strings.TrimSpace(config.Instructions) == "" {
		return errors.New("instructions are required")
	}
	return nil
}

func (config *editionConfig) EscalationPath() string  { return config.Path }
func (config *editionConfig) SetReason(reason string) { config.Reason = reason }

// An edition registers a kind once; its documents then decode, validate,
// encode, and answer the DSL like core's own.
func TestRegisteredKindsJoinTheGrammar(t *testing.T) {
	kind := approvalblock.Kind{
		Type:   "edition_check",
		Limits: approvalblock.TypeLimits{DefaultTimeout: 20 * time.Second, MaximumTimeout: time.Minute, MinimumAttempts: 1, MaximumAttempts: 3},
		New:    func() approvalblock.Config { return &editionConfig{} },
	}
	if err := approvalblock.RegisterKind(kind); err != nil {
		t.Fatal(err)
	}
	if err := approvalblock.RegisterKind(kind); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("registering twice = %v, want a refusal", err)
	}
	for name, bad := range map[string]approvalblock.Kind{
		"core type":    {Type: approvalblock.TypeCEL, Limits: kind.Limits, New: kind.New},
		"bad name":     {Type: "Edition Check", Limits: kind.Limits, New: kind.New},
		"no limits":    {Type: "other", New: kind.New},
		"no construct": {Type: "other", Limits: kind.Limits},
	} {
		if err := approvalblock.RegisterKind(bad); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	if limits, known := approvalblock.LimitsFor("edition_check"); !known || limits.DefaultTimeout != 20*time.Second {
		t.Fatalf("limits = %+v, %v", limits, known)
	}

	block := approvalblock.DefineKind("check", "edition_check", &editionConfig{Instructions: "Look twice.", Path: "payments"},
		approvalblock.Name("Check"), approvalblock.Reason("Checked."))
	if block.Timeout != "20s" || block.MaxAttempts != 1 || block.EscalationPath() != "payments" {
		t.Fatalf("definition envelope = %+v", block)
	}
	if err := block.Validate(); err != nil {
		t.Fatal(err)
	}
	document, err := block.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(document, &wire); err != nil {
		t.Fatal(err)
	}
	if string(wire["type"]) != `"edition_check"` || !strings.Contains(string(wire["config"]), `"reason":"Checked."`) {
		t.Fatalf("wire form = %s", document)
	}
	parsed, err := approvalblock.Parse(document)
	if err != nil {
		t.Fatal(err)
	}
	config, ok := parsed.Config.(*editionConfig)
	if !ok || config.Instructions != "Look twice." || config.Reason != "Checked." || parsed.EscalationPath() != "payments" {
		t.Fatalf("parsed = %+v (%T)", parsed, parsed.Config)
	}
	extra := strings.Replace(string(document), `"config":{`, `"config":{"unexpected":1,`, 1)
	if _, err := approvalblock.Parse([]byte(extra)); err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("unknown field accepted: %v", err)
	}
	empty := approvalblock.DefineKind("check", "edition_check", &editionConfig{}, approvalblock.Name("Check"))
	if err := empty.Validate(); err == nil || !strings.Contains(err.Error(), "instructions") {
		t.Fatalf("the kind's validation did not run: %v", err)
	}
	if _, known := approvalblock.LimitsFor("never_registered"); known {
		t.Fatal("an unregistered type is known")
	}
}
