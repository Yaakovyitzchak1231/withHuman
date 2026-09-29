package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/domain"
)

// A stored branch comes back with its blocks nested under config.blocks,
// each carrying its own snapshot fields, and block_count counts them all.
func TestPipelineRevisionNestsBranchBlocks(t *testing.T) {
	now := time.Unix(80_000, 0).UTC()
	branchID := domain.ID("11111111-1111-4111-8111-111111111111")
	insideID := domain.ID("22222222-2222-4222-8222-222222222222")
	repository := &pipelineHTTPRepository{
		revision: domain.RequestApprovalPipelineRevision{
			ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", OrganizationID: "organization-1", PipelineKey: "billing", Name: "Billing",
			Revision: 1, BlockIDs: []domain.ID{branchID, insideID}, CreatedByActorID: "membership-1", CreatedAt: now,
		},
		blocks: []domain.RequestApprovalBlock{
			{
				ID: branchID, OrganizationID: "organization-1", BlockKey: "refunds", Name: "Refunds", BlockType: "branch",
				ConfigVersion: 1, Config: json.RawMessage(`{"when":{"id":"31111111-1111-4111-8111-111111111111","field":"/request/tool","operator":"equals","value":"stripe.refund"},"block_keys":["inside"],"reason":"Nothing decided.","escalation_path":"payments-review"}`),
				TimeoutMS: 50, MaxAttempts: 1, ContentHash: strings.Repeat("a", 64), CreatedByActorID: "membership-1", CreatedAt: now,
			},
			{
				ID: insideID, OrganizationID: "organization-1", BlockKey: "inside", Name: "Inside", BlockType: "outcome",
				ConfigVersion: 1, Config: json.RawMessage(`{"outcome":"approve","reason":"Small."}`),
				TimeoutMS: 50, MaxAttempts: 1, ContentHash: strings.Repeat("b", 64), CreatedByActorID: "membership-1", CreatedAt: now,
			},
		},
	}
	router := pipelineManagementTestRouter(repository, pipelineHTTPPrincipal())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/request-approval-pipelines/billing/revisions/1", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("got status %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		BlockCount int `json:"block_count"`
		Blocks     []struct {
			ID     string `json:"id"`
			Type   string `json:"type"`
			Config struct {
				BlockKeys      []string `json:"block_keys"`
				EscalationPath string   `json:"escalation_path"`
				Blocks         []struct {
					ID          string    `json:"id"`
					SnapshotID  domain.ID `json:"snapshot_id"`
					ContentHash string    `json:"content_hash"`
				} `json:"blocks"`
			} `json:"config"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.BlockCount != 2 || len(body.Blocks) != 1 || body.Blocks[0].Type != "branch" || body.Blocks[0].Config.EscalationPath != "payments-review" {
		t.Fatalf("unexpected revision: %s", response.Body.String())
	}
	nested := body.Blocks[0].Config.Blocks
	if len(nested) != 1 || nested[0].ID != "inside" || nested[0].SnapshotID != insideID || nested[0].ContentHash != strings.Repeat("b", 64) {
		t.Fatalf("the branch must nest its stored blocks: %s", response.Body.String())
	}
	if body.Blocks[0].Config.BlockKeys != nil {
		t.Fatalf("storage's block_keys must not reach the API: %s", response.Body.String())
	}
}

func TestPipelinePreviewReportsBranchSteps(t *testing.T) {
	repository := &pipelineHTTPRepository{}
	router := pipelineManagementTestRouter(repository, pipelineHTTPPrincipal())
	body := `{
		"blocks":[{
			"id":"refunds","name":"Refunds","type":"branch","enabled":true,"timeout":"50ms","max_attempts":1,
			"config":{
				"when":{"id":"21aa670a-734c-454c-a1e9-653d54be054b","field":"/request/tool","operator":"equals","value":"stripe.refund"},
				"blocks":[{
					"id":"small-refund","name":"Approve small refunds","type":"branch","enabled":true,"timeout":"50ms","max_attempts":1,
					"config":{
						"when":{"id":"5d79dc6a-5f7d-4e70-aeb1-dcd682e77322","field":"/request/arguments/amount_cents","operator":"less_than","value":1000},
						"blocks":[{
							"id":"approve-small-refund","name":"Approve","type":"outcome","enabled":true,"timeout":"50ms","max_attempts":1,
							"config":{"outcome":"approve","reason":"Small refunds are pre-authorized."}
						}],
						"reason":""
					}
				}],
				"reason":"Refunds nobody decided go to Payments.",
				"escalation_path":"payments-review"
			}
		}],
		"sample":{"request":{"tool":"stripe.refund","arguments":{"amount_cents":5000}},"agent":{"slug":"billing","name":"billing-agent"}}
	}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/request-approval-pipelines/billing/preview", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("got status %d: %s", response.Code, response.Body.String())
	}
	var preview struct {
		Outcome         string `json:"outcome"`
		ReasonCode      string `json:"reason_code"`
		TerminalBlockID string `json:"terminal_block_id"`
		EscalationPath  string `json:"escalation_path"`
		Blocks          []struct {
			BlockID     string `json:"block_id"`
			Outcome     string `json:"outcome"`
			Position    int    `json:"position"`
			EndOfBranch bool   `json:"end_of_branch"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Outcome != "human" || preview.ReasonCode != "end_of_branch" || preview.TerminalBlockID != "refunds" || preview.EscalationPath != "payments-review" || len(preview.Blocks) != 3 {
		t.Fatalf("unexpected preview: %s", response.Body.String())
	}
	// The inner branch does not match a large refund, so its outcome block
	// never runs and the outer branch runs out.
	if first, skipped, last := preview.Blocks[0], preview.Blocks[1], preview.Blocks[2]; first.Outcome != "enter" || first.EndOfBranch || skipped.BlockID != "small-refund" || skipped.Outcome != "next" || skipped.Position != 1 || last.BlockID != "refunds" || last.Outcome != "human" || !last.EndOfBranch || last.Position != 0 {
		t.Fatalf("unexpected branch steps: %s", response.Body.String())
	}

	nestedUnknown := strings.Replace(body, `"outcome":"approve"`, `"outcome":"approve","surprise":1`, 1)
	request = httptest.NewRequest(http.MethodPost, "/api/v1/request-approval-pipelines/billing/preview", strings.NewReader(nestedUnknown))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("an unknown field inside a branch must be refused: status=%d body=%s", response.Code, response.Body.String())
	}
}

// get_pipeline hands back stored blocks; writableBlocks must strip the
// stored fields at every depth so they can be saved again.
func TestWritableBlocksStripStoredFieldsInsideBranches(t *testing.T) {
	var stored []map[string]any
	if err := json.Unmarshal([]byte(`[{
		"id":"refunds","snapshot_id":"s1","config_version":1,"content_hash":"h1","type":"branch",
		"config":{"reason":"r","blocks":[
			{"id":"inner","snapshot_id":"s2","config_version":1,"content_hash":"h2","type":"branch",
			 "config":{"reason":"r","blocks":[{"id":"leaf","snapshot_id":"s3","config_version":1,"content_hash":"h3","type":"outcome","config":{}}]}}
		]}
	}]`), &stored); err != nil {
		t.Fatal(err)
	}
	cleaned, err := json.Marshal(writableBlocks(stored))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"snapshot_id", "config_version", "content_hash"} {
		if strings.Contains(string(cleaned), field) {
			t.Fatalf("%s survived: %s", field, cleaned)
		}
	}
	if !strings.Contains(string(cleaned), `"id":"leaf"`) {
		t.Fatalf("nested blocks were lost: %s", cleaned)
	}
}
