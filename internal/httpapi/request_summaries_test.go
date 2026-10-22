package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/withHumanAI/withHuman/application"
	"github.com/withHumanAI/withHuman/domain"
)

type stubRequestSummaries struct {
	summaries map[domain.ID]application.RequestSummary
	err       error
	reads     []application.ReadRequestSummariesInput
}

func (s *stubRequestSummaries) RequestSummaries(_ context.Context, input application.ReadRequestSummariesInput) (map[domain.ID]application.RequestSummary, error) {
	s.reads = append(s.reads, input)
	return s.summaries, s.err
}

func TestQueueResponsesCarryStoredSummaries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	generated := time.Unix(90_000, 0).UTC()
	reader := &stubRequestSummaries{summaries: map[domain.ID]application.RequestSummary{
		"r1": {Text: "Refund $49.00 on charge ch_3N1x for a faulty bag", GeneratedAt: generated},
	}}
	api := &API{requestSummaries: reader, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	requests := []domain.ApprovalRequest{
		{ID: "r1", OrganizationID: "organization-1", Tool: "stripe.refunds.create", Arguments: json.RawMessage(`{"amount":4900}`)},
		{ID: "r2", OrganizationID: "organization-1", Tool: "Bash", Arguments: json.RawMessage(`{"command":"ls"}`)},
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/requests", nil)
	setPrincipal(c, organizationHTTPPrincipal(domain.PermissionRequestRead))

	responses := api.presentMany(c, requests, time.Now())
	if summary := responses[0].Summary; summary == nil || summary.Text != "Refund $49.00 on charge ch_3N1x for a faulty bag" || !summary.GeneratedAt.Equal(generated) {
		t.Fatalf("summary = %+v", responses[0].Summary)
	}
	if responses[1].Summary != nil {
		t.Fatalf("request without a summary carries one: %+v", responses[1].Summary)
	}
	if len(reader.reads) != 1 || reader.reads[0].OrganizationID != "organization-1" || len(reader.reads[0].RequestIDs) != 2 {
		t.Fatalf("a page should be one read: %+v", reader.reads)
	}
	payload, _ := json.Marshal(newApprovalResponse(requests[0]))
	if strings.Contains(string(payload), "summary") {
		t.Fatalf("AAP representation carries a summary: %s", payload)
	}

	reader.err = errors.New("database unavailable")
	if failed := api.presentOne(c, requests[0], time.Now()); failed.Summary != nil {
		t.Fatalf("a failed read still carried a summary: %+v", failed.Summary)
	}
}
