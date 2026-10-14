package httpapi

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/domain"
	"gopkg.in/yaml.v3"
)

type schemaShape struct {
	Required   []string                  `yaml:"required"`
	Properties map[string]map[string]any `yaml:"properties"`
}

func schemaShapes(t *testing.T, document []byte) map[string]schemaShape {
	t.Helper()
	var parsed struct {
		Components struct {
			Schemas map[string]schemaShape `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(document, &parsed); err != nil {
		t.Fatalf("parse schema document: %v", err)
	}
	return parsed.Components.Schemas
}

// protocolSchemas reads the AAP repository's OpenAPI document from the
// pinned module, so these responses follow the protocol version we build
// against.
func protocolSchemas(t *testing.T) map[string]schemaShape {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/agentapprovalprotocol/agentapprovalprotocol").Output()
	if err != nil {
		t.Fatalf("locate the AAP module: %v", err)
	}
	document, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(out)), "openapi.yaml"))
	if err != nil {
		t.Fatalf("read the AAP OpenAPI document: %v", err)
	}
	return schemaShapes(t, document)
}

// conforms checks that value has every required field of the named schema
// and no field it does not define, following $ref into nested objects.
func conforms(t *testing.T, schemas map[string]schemaShape, name string, value any) {
	t.Helper()
	schema, ok := schemas[name]
	if !ok {
		t.Fatalf("schema %s is not defined", name)
	}
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s: got %T, want an object", name, value)
	}
	for _, field := range schema.Required {
		if _, present := object[field]; !present {
			t.Errorf("%s: required field %q is missing", name, field)
		}
	}
	for field, nested := range object {
		property, defined := schema.Properties[field]
		if !defined {
			t.Errorf("%s: field %q is not in the protocol", name, field)
			continue
		}
		if ref, _ := property["$ref"].(string); ref != "" {
			if _, isObject := nested.(map[string]any); isObject {
				conforms(t, schemas, strings.TrimPrefix(ref, "#/components/schemas/"), nested)
			}
		}
	}
}

func asJSONValue(t *testing.T, value any) any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAAPInstanceResponsesFollowTheProtocol(t *testing.T) {
	if testing.Short() {
		t.Skip("reads the AAP module through the go command")
	}
	protocol := protocolSchemas(t)
	instance := domain.AgentInstance{
		ID: "b29a43a7-1949-4b9f-9d61-b7ae66f85d31", OrganizationID: "0d8c1c1e-9a43-4f3b-9d1e-3f5d2b8a6c71", AgentSlug: "support-bot",
		Name: "ci-runner-07", Status: domain.AgentActive, Metadata: json.RawMessage(`{"region":"eu-west"}`),
		CreatedAt: time.Now(), LastSeenAt: time.Now(),
		Delivery: &domain.AgentInstanceDelivery{Type: "webhook", URL: "https://harness.example.com/aap/events", RegistrationID: "8246931c-4ce0-4c15-a94a-e9c078ad06e2"},
	}
	issued := domain.AgentInstanceCredential{AgentInstance: instance, Credential: domain.IssuedCredential{ID: "6d0f4c55-41b4-4a0e-8f0c-1c8a2a6f93d2", Token: "whc_test", ExpiresAt: time.Now().Add(time.Hour)}}

	conforms(t, protocol, "ProvisionInstanceResponse", asJSONValue(t, newAAPProvisionedInstanceResponse(issued)))
	conforms(t, protocol, "Instance", asJSONValue(t, newAAPInstanceResponse(instance)))

	bare := instance
	bare.Delivery = nil
	unexpiring := issued
	unexpiring.AgentInstance, unexpiring.Credential.ExpiresAt = bare, time.Time{}
	provisioned := asJSONValue(t, newAAPProvisionedInstanceResponse(unexpiring)).(map[string]any)
	conforms(t, protocol, "ProvisionInstanceResponse", provisioned)
	if _, present := provisioned["instance"].(map[string]any)["delivery"]; present {
		t.Error("an instance without a receiver must omit delivery")
	}
	if _, present := provisioned["credential"].(map[string]any)["expires_at"]; present {
		t.Error("a credential without an expiry must omit expires_at")
	}
}

// The contract documents the same fields the protocol defines.
func TestAAPInstanceSchemasMatchTheProtocol(t *testing.T) {
	if testing.Short() {
		t.Skip("reads the AAP module through the go command")
	}
	protocol, ours := protocolSchemas(t), schemaShapes(t, openAPISpec)
	fields := func(schema schemaShape) []string {
		names := make([]string, 0, len(schema.Properties))
		for name := range schema.Properties {
			names = append(names, name)
		}
		sort.Strings(names)
		return names
	}
	pairs := map[string]string{"AAPInstance": "Instance", "AAPProvisionedInstance": "ProvisionInstanceResponse"}
	for mine, theirs := range pairs {
		if got, want := fields(ours[mine]), fields(protocol[theirs]); !slices.Equal(got, want) {
			t.Errorf("%s fields = %v, protocol %s has %v", mine, got, theirs, want)
		}
		if got, want := slices.Sorted(slices.Values(ours[mine].Required)), slices.Sorted(slices.Values(protocol[theirs].Required)); !slices.Equal(got, want) {
			t.Errorf("%s requires %v, protocol %s requires %v", mine, got, theirs, want)
		}
	}
}
