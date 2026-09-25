package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/authn"
	"github.com/withHumanAI/withHuman/db/dbtest"
	"github.com/withHumanAI/withHuman/domain"
	"github.com/withHumanAI/withHuman/entitlements"
	"github.com/withHumanAI/withHuman/internal/postgres"
	"github.com/withHumanAI/withHuman/internal/toolcatalog"
)

func TestToolCatalogReportsAndVisibility(t *testing.T) {
	ctx := context.Background()
	store, err := postgres.Open(ctx, dbtest.Start(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	clock := &aapTestClock{}
	now := clock.Now()
	org, user, member := domain.ID(uuid.NewString()), domain.ID(uuid.NewString()), domain.ID(uuid.NewString())
	_, err = store.ActivateOrganization(ctx, application.ActivateOrganizationInput{OrganizationID: org, OrganizationSlug: "catalog", OrganizationName: "Catalog", UserID: user, MembershipID: member, DisplayName: "Owner", Email: "catalog@example.com", PasswordHash: "test", AdmissionSource: domain.AdmissionManual, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := store.CreateAgent(ctx, application.CreateAgentInput{Kind: domain.AgentService, OrganizationID: org, ActorID: member, Name: "Support", AgentSlug: "support", AgentQuota: application.UnlimitedQuota(), Now: now})
	if err != nil {
		t.Fatal(err)
	}
	credentialID := domain.ID(uuid.NewString())
	instance, err := store.CreateAgentInstanceWithCredential(ctx, application.CreateAgentInstanceWithCredentialInput{OrganizationID: org, AgentID: agent.ID, ActorID: user, ActorType: domain.ActorHuman, Name: "Laptop", Metadata: json.RawMessage(`{}`), Credential: application.AgentCredentialRecord{ID: credentialID, SecretHash: authn.HashToken("secret"), IssuedAt: now, ExpiresAt: now.Add(time.Hour)}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	service := toolcatalog.NewService(store, nil, clock, slog.New(slog.NewTextHandler(io.Discard, nil)))
	auth := authn.NewService(store, store, store, nil, entitlements.NewUnlimited(), nil, clock, authn.ServiceConfig{})
	api := &API{auth: auth, toolCatalog: service, logger: slog.Default()}
	reader := domain.Principal{ActorType: domain.ActorHuman, OrganizationID: org, MembershipID: member, Grants: []domain.Grant{{Permission: domain.PermissionAgentRead, ScopeKind: domain.ScopeAgent, ScopeID: "support"}}}
	router := gin.New()
	router.PUT("/report", api.machineAuth(), api.reportToolCatalog)
	router.GET("/catalog", func(c *gin.Context) { setPrincipal(c, reader); api.listToolCatalog(c) })
	report := domain.ToolCatalogReport{Runtime: "custom", DiscoveryContext: "project-a", Complete: true, Sources: []domain.ToolCatalogSourceReport{{Source: "mcp", Server: "payments", Complete: true, Tools: []domain.ToolDefinition{{Name: "refund", Description: "Refund a charge", InputSchema: json.RawMessage(`{"type":"object","properties":{"amount":{"type":"integer"},"currency":{"enum":["GBP","USD"],"type":"string"}},"required":["amount"]}`)}, {Name: "lookup"}}}}}
	token := "whc_" + string(credentialID) + "_secret"
	put := func(r domain.ToolCatalogReport, bearer string) int {
		t.Helper()
		raw, _ := json.Marshal(r)
		req := httptest.NewRequest("PUT", "/report", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+bearer)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		return out.Code
	}
	read := func(query string) domain.ToolCatalogPage {
		t.Helper()
		out := httptest.NewRecorder()
		router.ServeHTTP(out, httptest.NewRequest("GET", "/catalog"+query, nil))
		if out.Code != 200 {
			t.Fatalf("read: %d %s", out.Code, out.Body.String())
		}
		var page domain.ToolCatalogPage
		if err := json.Unmarshal(out.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	if status := put(report, token); status != 204 {
		t.Fatalf("report: %d", status)
	}
	first := read("")
	if first.Total != 2 || len(first.Tools) != 2 || first.Tools[0].AgentInstanceID != instance.ID || !first.Tools[0].Available {
		t.Fatalf("first: %+v", first)
	}
	id := first.Tools[1].ID
	fingerprint := first.Tools[1].SchemaFingerprint
	if status := put(report, token); status != 204 {
		t.Fatal(status)
	}
	if got := read(""); got.Total != 2 || got.Tools[1].ID != id {
		t.Fatalf("retry: %+v", got)
	}
	page := read("?limit=1")
	if page.NextOffset == nil || *page.NextOffset != 1 || page.Total != 2 || len(read("?limit=1&offset=1").Tools) != 1 {
		t.Fatal("pagination")
	}
	if got := read("?q=charge"); got.Total != 1 || got.Tools[0].Name != "refund" {
		t.Fatal("search")
	}
	// One project never replaces another; two sources may use the same name.
	report.DiscoveryContext = "project-b"
	report.Sources[0].Server = "other-payments"
	report.Sources[0].Tools = report.Sources[0].Tools[:1]
	if put(report, token) != 204 || read("").Total != 3 {
		t.Fatal("context isolation")
	}
	report.DiscoveryContext = "project-a"
	report.Sources[0].Server = "payments"
	report.Sources[0].Complete = false
	report.Sources[0].Error = "Cannot list tools"
	report.Sources[0].Tools = nil
	if put(report, token) != 204 {
		t.Fatal("failed refresh report")
	}
	stale := read("?status=stale")
	if stale.Total != 2 || stale.Tools[1].ID != id || stale.Tools[1].SchemaFingerprint != fingerprint {
		t.Fatalf("lost last successful snapshot: %+v", stale)
	}
	report.Sources[0].Complete = true
	report.Sources[0].Error = ""
	report.Sources[0].Tools = []domain.ToolDefinition{{Name: "refund", InputSchema: json.RawMessage(`{"type":"object","properties":{"amount":{"type":"string"}}}`)}}
	if put(report, token) != 204 {
		t.Fatal("changed schema")
	}
	if got := read("?status=unavailable"); got.Total != 1 || got.Tools[0].Name != "lookup" {
		t.Fatalf("removal: %+v", got)
	}
	if got := read("?status=available"); got.Total != 2 {
		t.Fatalf("context retained: %+v", got)
	}
	if got := read("?status=stale"); got.Total != 0 {
		t.Fatal("successful refresh remains stale")
	}
	reader.Grants[0].ScopeID = "other-agent"
	if read("").Total != 0 {
		t.Fatal("agent scope leaked")
	}
	reader.Grants[0].ScopeID = "support"
	reader.OrganizationID = domain.ID(uuid.NewString())
	if read("").Total != 0 {
		t.Fatal("organization leaked")
	}
	reader.OrganizationID = org
	forged := httptest.NewRequest("PUT", "/report", strings.NewReader(`{"organization_id":"other","runtime":"x","discovery_context":"x","sources":[]}`))
	forged.Header.Set("Authorization", "Bearer "+token)
	out := httptest.NewRecorder()
	router.ServeHTTP(out, forged)
	if out.Code != 400 {
		t.Fatalf("forged identity accepted: %d", out.Code)
	}
	report.Sources[0].Source = "gateway" // an edition's source, never a runtime's
	if put(report, token) != 400 {
		t.Fatal("instance forged gateway")
	}
	report.Sources[0].Source = "mcp"
	if put(report, "whk_"+string(credentialID)+"_secret") != 401 {
		t.Fatal("management key accepted")
	}
	if err := store.RevokeAgentCredential(ctx, application.RevokeAgentCredentialInput{OrganizationID: org, CredentialID: credentialID, ActorID: member, Now: now}); err != nil {
		t.Fatal(err)
	}
	if put(report, token) != 401 {
		t.Fatal("revoked credential accepted")
	}
	if got := read("?status=available"); got.Total != 0 {
		t.Fatalf("revoked instance claims availability: %+v", got)
	}
	secondCredential := domain.ID(uuid.NewString())
	second, err := store.CreateAgentInstanceWithCredential(ctx, application.CreateAgentInstanceWithCredentialInput{OrganizationID: org, AgentID: agent.ID, ActorID: user, ActorType: domain.ActorHuman, Name: "Server", Metadata: json.RawMessage(`{}`), Credential: application.AgentCredentialRecord{ID: secondCredential, SecretHash: authn.HashToken("second"), IssuedAt: now, ExpiresAt: now.Add(time.Hour)}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	report.Sources[0].Tools = []domain.ToolDefinition{{Name: "refund", InputSchema: json.RawMessage(`{"type":"object","properties":{"amount":{"type":"boolean"}}}`)}}
	secondToken := "whc_" + string(secondCredential) + "_second"
	if put(report, secondToken) != 204 {
		t.Fatal("second instance report")
	}
	if got := read("?q=refund"); got.Total != 3 {
		t.Fatalf("conflicting definitions across instances were lost: %+v", got)
	}
	if got := read("?status=available"); got.Total != 1 || got.Tools[0].AgentInstanceID != second.ID {
		t.Fatalf("instance isolation: %+v", got)
	}
	report.Sources = nil
	report.Complete = false
	if put(report, secondToken) != 204 || read("?status=available").Total != 1 {
		t.Fatal("incomplete enumeration removed a source")
	}
	report.Complete = true
	if put(report, secondToken) != 204 || read("?status=available").Total != 0 {
		t.Fatal("complete enumeration did not remove missing sources")
	}
}
