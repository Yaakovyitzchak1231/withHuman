package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/withHumanAI/withHuman/approvalblock"
	"github.com/withHumanAI/withHuman/internal/mcpreference"
)

// The tool catalog. Every tool is a thin, well-described door onto the
// product API: inputs mirror the HTTP request bodies, results are the
// HTTP answers, and the compositions (save then activate) exist because
// an agent should not have to choreograph If-Match by hand.

const (
	mcpAgentsPath       = "/api/v1/agents"
	mcpMembersPath      = "/api/v1/members"
	mcpTeamsPath        = "/api/v1/teams"
	mcpPipelinesPath    = "/api/v1/request-approval-pipelines"
	mcpPathsPath        = "/api/v1/escalation-paths"
	mcpWebhooksPath     = "/api/v1/webhook_endpoints"
	mcpNoActiveRevision = `"0"`
)

type mcpStatusInput struct {
	Status string `json:"status,omitempty" jsonschema:"Which to list: live (the default), archived, or all."`
}

type mcpPipelineKeyInput struct {
	PipelineKey string `json:"pipeline_key" jsonschema:"The pipeline's key. 'entry' is the entry pipeline every request starts in; any other key is a pipeline a delegate block hands requests to."`
}

type mcpPipelineRevisionInput struct {
	PipelineKey string `json:"pipeline_key" jsonschema:"The pipeline's key ('entry' for the entry pipeline)."`
	Revision    int64  `json:"revision" jsonschema:"The revision number, from list_pipeline_revisions."`
}

type mcpCreatePipelineInput struct {
	PipelineKey           string           `json:"pipeline_key" jsonschema:"The new pipeline's key: lowercase letters, digits, dots, underscores and hyphens, up to 63 characters. It is taken for good, archived pipelines included, and can never be 'entry'."`
	Name                  string           `json:"name" jsonschema:"The pipeline's display name, for example Payments."`
	Blocks                []map[string]any `json:"blocks,omitempty" jsonschema:"The first revision's blocks, in the shape update_pipeline takes. Omit or pass an empty list for a pipeline that passes every request that enters it to human review until it is edited."`
	DefaultEscalationPath string           `json:"default_escalation_path,omitempty" jsonschema:"Escalation path key for requests this pipeline hands to a person without a block naming a path, including requests that run out of it. Omit to inherit the defaults of the pipelines that delegate into it."`
}

type mcpUpdatePipelineInput struct {
	PipelineKey           string           `json:"pipeline_key" jsonschema:"The pipeline to replace ('entry' for the entry pipeline)."`
	Name                  string           `json:"name,omitempty" jsonschema:"A new display name for the pipeline. Omit to keep the current one."`
	Blocks                []map[string]any `json:"blocks" jsonschema:"The complete ordered block list for the new revision (it replaces the current blocks, so include the ones to keep). Each block: id (key-shaped, unique across the whole pipeline including blocks inside branches), name, type (always | cel | webhook | branch | delegate, or a kind this deployment adds; see read_reference), enabled: true, timeout (for example 50ms), max_attempts (1 for always, cel, branch and delegate), and config for its type; a branch holds its own ordered blocks in config.blocks, a delegate names the pipeline it hands matching requests to in config.pipeline_key. See read_reference topic pipeline_blocks. An empty list sends every request to human review."`
	DefaultEscalationPath string           `json:"default_escalation_path,omitempty" jsonschema:"Escalation path key that takes human exits which name no path of their own, and requests the pipeline hands to a person by running out of blocks. Omit to inherit the defaults of the pipelines that delegate into this one, or, for the entry pipeline, to leave them to any authorized reviewer."`
	DefaultAskAgentOwner  bool             `json:"default_ask_agent_owner,omitempty" jsonschema:"Send those requests to the person who owns the agent instance that made them instead, with default_escalation_path as the fallback for requests from agents without an owner (service agents). Requires default_escalation_path."`
	Activate              *bool            `json:"activate,omitempty" jsonschema:"Activate the new revision right away so new requests use it. Defaults to true. False saves a draft revision you can activate later with activate_pipeline_revision."`
}

type mcpPreviewSample struct {
	Tool           string         `json:"tool" jsonschema:"The tool name the sample request asks for, for example Bash or stripe__refund."`
	Arguments      map[string]any `json:"arguments,omitempty" jsonschema:"The tool call's arguments as the agent would send them."`
	AgentReasoning string         `json:"agent_reasoning,omitempty" jsonschema:"What the agent said it was trying to do, for blocks that include the agent's reasoning."`
	Context        map[string]any `json:"context,omitempty" jsonschema:"Extra request context, if the adapter would send any."`
	AgentSlug      string         `json:"agent_slug,omitempty" jsonschema:"The requesting agent's slug for the sample. Defaults to 'agent'."`
	AgentName      string         `json:"agent_name,omitempty" jsonschema:"The requesting agent's display name for the sample. Defaults to the slug."`
}

type mcpPreviewPipelineInput struct {
	PipelineKey           string           `json:"pipeline_key" jsonschema:"The pipeline the blocks stand in for ('entry' for the entry pipeline). Delegate blocks expand to their targets' current active revisions."`
	Blocks                []map[string]any `json:"blocks" jsonschema:"The blocks to run the sample through, in order; the same shape update_pipeline takes. Pass the current blocks from get_pipeline to test what is active."`
	DefaultEscalationPath string           `json:"default_escalation_path,omitempty" jsonschema:"The default escalation path to assume, as update_pipeline takes it."`
	DefaultAskAgentOwner  bool             `json:"default_ask_agent_owner,omitempty" jsonschema:"Whether the default asks the agent's owner first, as update_pipeline takes it."`
	Sample                mcpPreviewSample `json:"sample" jsonschema:"The request to run through the blocks. Nothing is created: no approval request, no notification."`
}

type mcpPathKeyInput struct {
	PathKey string `json:"path_key" jsonschema:"The escalation path's key."`
}

type mcpPathRevisionInput struct {
	PathKey  string `json:"path_key" jsonschema:"The escalation path's key."`
	Revision int64  `json:"revision" jsonschema:"The revision number, from list_escalation_path_revisions."`
}

type mcpEscalationPathInput struct {
	PathKey      string           `json:"path_key" jsonschema:"Stable key, lowercase letters, digits, dots, underscores or dashes (for example payments-review). It names the path from pipeline blocks and is never reused, even after archiving."`
	Name         string           `json:"name" jsonschema:"Display name people see, for example Payments review."`
	Nodes        []map[string]any `json:"nodes" jsonschema:"The path's nodes in order: level nodes name targets and how long to wait (escalate_after) before the next level, if_else nodes branch on conditions, defer nodes wait for working hours. Every node needs a UUID id. See read_reference topic escalation_paths."`
	WorkingHours []map[string]any `json:"working_hours,omitempty" jsonschema:"Named working-hours sets that defer nodes and working_hours conditions refer to by id: {id, timezone (IANA), intervals: [{weekdays: [mon, ...], start: '09:00', end: '17:30'}]}."`
	Repeat       map[string]any   `json:"repeat,omitempty" jsonschema:"What to do when every level has been paged and nobody answered: {times: 1..9, after: '15m'} walks the levels again."`
	Activate     *bool            `json:"activate,omitempty" jsonschema:"Activate the new revision right away. Defaults to true. A path with no active revision cannot be named by an active pipeline."`
}

type mcpTeamIDInput struct {
	TeamID string `json:"team_id" jsonschema:"The team's id, from list_teams."`
}

type mcpCreateTeamInput struct {
	Name          string   `json:"name" jsonschema:"The team's name, unique among live teams."`
	MembershipIDs []string `json:"membership_ids,omitempty" jsonschema:"Membership ids (from list_members) to place on the team right away."`
}

type mcpSetTeamMembersInput struct {
	TeamID        string   `json:"team_id" jsonschema:"The team's id, from list_teams."`
	MembershipIDs []string `json:"membership_ids" jsonschema:"The complete manual member list as membership ids from list_members; it replaces the current one, so include members to keep. Members a directory sync placed on the team are unaffected."`
	Force         bool     `json:"force,omitempty" jsonschema:"Also remove members the team's active escalation policy still names. Without it the call is refused and lists them."`
}

type mcpCreateAgentInput struct {
	Slug string `json:"slug" jsonschema:"Permanent identifier: lowercase letters, digits, dots, underscores or dashes, unique for good (for example support-assistant)."`
	Name string `json:"name" jsonschema:"Display name, unique among live agents."`
}

type mcpEndpointKeyInput struct {
	EndpointKey string `json:"endpoint_key" jsonschema:"The webhook endpoint's key."`
}

type mcpSaveWebhookEndpointInput struct {
	EndpointKey string `json:"endpoint_key" jsonschema:"Stable key a pipeline's webhook block names (config.endpoint_key). A new key creates the endpoint and mints its signing secret; an existing key gets a new revision and keeps its secret."`
	Name        string `json:"name" jsonschema:"Display name."`
	URL         string `json:"url" jsonschema:"Public HTTPS URL withHuman posts the signed request to."`
	Activate    *bool  `json:"activate,omitempty" jsonschema:"Activate the new revision right away. Defaults to true."`
}

type mcpReferenceInput struct {
	Topic string `json:"topic" jsonschema:"One of: pipeline_blocks (block envelope and each type's config), conditions (the condition tree cel blocks and request conditions use: fields, operators, values), escalation_paths (path document: nodes, targets, timing, working hours, repeat)."`
}

func (a *API) registerMCPTools(server *mcp.Server) {
	// Discovery.
	addMCPTool(a, server, &mcp.Tool{
		Name:        "list_agents",
		Description: "List the organization's agents: slug (what pipelines and conditions refer to), name, status, and recent request counts.",
		Annotations: mcpReadOnly,
	}, func(call *mcpCall, input mcpStatusInput) (*mcp.CallToolResult, any, error) {
		response, err := call.get(mcpAgentsPath, statusQuery(input.Status))
		if err != nil {
			return nil, nil, err
		}
		return mcpResult(response.Body)
	})
	addMCPTool(a, server, &mcp.Tool{
		Name:        "create_agent",
		Description: "Create an agent. Its approval pipeline exists immediately as an empty revision that sends every request to human review; use update_pipeline to give it rules. Connecting a runtime (instances and credentials) happens in the app or CLI, not here.",
		Annotations: mcpAdditive,
	}, func(call *mcpCall, input mcpCreateAgentInput) (*mcp.CallToolResult, any, error) {
		response, err := call.post(mcpAgentsPath, map[string]any{"slug": input.Slug, "name": input.Name}, nil)
		if err != nil {
			return nil, nil, err
		}
		return mcpResult(response.Body)
	})
	addMCPTool(a, server, &mcp.Tool{
		Name:        "list_members",
		Description: "List the organization's members with the membership id (the id an escalation path's user target carries), display name, email, status, roles and team ids.",
		Annotations: mcpReadOnly,
	}, func(call *mcpCall, _ struct{}) (*mcp.CallToolResult, any, error) {
		response, err := call.get(mcpMembersPath, nil)
		if err != nil {
			return nil, nil, err
		}
		return mcpResult(response.Body)
	})
	addMCPTool(a, server, &mcp.Tool{
		Name:        "list_teams",
		Description: "List teams with their id (what an escalation path's team target carries), name, member count and whether an escalation policy is active.",
		Annotations: mcpReadOnly,
	}, func(call *mcpCall, input mcpStatusInput) (*mcp.CallToolResult, any, error) {
		response, err := call.get(mcpTeamsPath, statusQuery(input.Status))
		if err != nil {
			return nil, nil, err
		}
		return mcpResult(response.Body)
	})
	addMCPTool(a, server, &mcp.Tool{
		Name:        "get_team",
		Description: "Read one team and its members.",
		Annotations: mcpReadOnly,
	}, func(call *mcpCall, input mcpTeamIDInput) (*mcp.CallToolResult, any, error) {
		response, err := call.get(mcpTeamsPath+"/"+url.PathEscape(input.TeamID), nil)
		if err != nil {
			return nil, nil, err
		}
		return mcpResult(response.Body)
	})
	addMCPTool(a, server, &mcp.Tool{
		Name:        "create_team",
		Description: "Create a team, optionally with its first members. Teams are escalation targets: a level that names a team reaches every current member. Requires a personal API key; organization keys cannot manage teams.",
		Annotations: mcpAdditive,
	}, func(call *mcpCall, input mcpCreateTeamInput) (*mcp.CallToolResult, any, error) {
		response, err := call.post(mcpTeamsPath, map[string]any{"name": input.Name}, nil)
		if err != nil {
			return nil, nil, err
		}
		if len(input.MembershipIDs) == 0 {
			return mcpResult(response.Body)
		}
		var team struct {
			Team json.RawMessage `json:"team"`
		}
		var identity struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(response.Body, &team); err != nil || json.Unmarshal(team.Team, &identity) != nil || identity.ID == "" {
			return nil, nil, fmt.Errorf("the team was created but its id could not be read; call list_teams and set_team_members")
		}
		members, err := call.do(http.MethodPut, mcpTeamsPath+"/"+url.PathEscape(identity.ID)+"/members", nil, map[string]any{"membership_ids": input.MembershipIDs}, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("team %s was created but its members were not set: %w", identity.ID, err)
		}
		var roster struct {
			Members json.RawMessage `json:"members"`
		}
		if err := json.Unmarshal(members.Body, &roster); err != nil {
			return nil, nil, err
		}
		return mcpObject(map[string]any{"team": team.Team, "members": roster.Members})
	})
	addMCPTool(a, server, &mcp.Tool{
		Name:        "set_team_members",
		Description: "Replace a team's manual member list. Requires a personal API key; organization keys cannot manage teams.",
		Annotations: mcpReplacing,
	}, func(call *mcpCall, input mcpSetTeamMembersInput) (*mcp.CallToolResult, any, error) {
		ids := input.MembershipIDs
		if ids == nil {
			ids = []string{}
		}
		response, err := call.do(http.MethodPut, mcpTeamsPath+"/"+url.PathEscape(input.TeamID)+"/members", nil, map[string]any{"membership_ids": ids, "force": input.Force}, nil)
		if err != nil {
			return nil, nil, err
		}
		return mcpResult(response.Body)
	})

	// Approval pipelines.
	addMCPTool(a, server, &mcp.Tool{
		Name:        "list_pipelines",
		Description: "List every approval pipeline: the entry pipeline every request starts in, then every pipeline a delegate block can hand requests to, each with its key, name, active revision number and history.",
		Annotations: mcpReadOnly,
	}, func(call *mcpCall, input mcpStatusInput) (*mcp.CallToolResult, any, error) {
		response, err := call.get(mcpPipelinesPath, statusQuery(input.Status))
		if err != nil {
			return nil, nil, err
		}
		return mcpResult(response.Body)
	})
	addMCPTool(a, server, &mcp.Tool{
		Name:        "get_pipeline",
		Description: "Read a pipeline's active revision with its blocks in order and its default escalation path. Blocks come back in the shape update_pipeline accepts, so you can edit and resubmit them.",
		Annotations: mcpReadOnly,
	}, func(call *mcpCall, input mcpPipelineKeyInput) (*mcp.CallToolResult, any, error) {
		response, err := call.get(pipelinePath(input.PipelineKey), nil)
		if err != nil {
			return nil, nil, err
		}
		return mcpResult(response.Body)
	})
	addMCPTool(a, server, &mcp.Tool{
		Name: "create_pipeline",
		Description: "Create a pipeline that a delegate block in another pipeline can hand matching requests to, with its first revision active at once. " +
			"Use it to give one team its own part of the approval flow: the entry pipeline decides which requests reach it, the new pipeline decides what happens to them. Grant the team pipeline.read, pipeline.write and pipeline.activate at pipeline:<key> so they can edit it and nothing else.",
		Annotations: mcpAdditive,
	}, func(call *mcpCall, input mcpCreatePipelineInput) (*mcp.CallToolResult, any, error) {
		body := map[string]any{"pipeline_key": input.PipelineKey, "name": input.Name, "blocks": writableBlocks(input.Blocks)}
		if input.DefaultEscalationPath != "" {
			body["default_escalation_path"] = input.DefaultEscalationPath
		}
		response, err := call.post(mcpPipelinesPath, body, nil)
		if err != nil {
			return nil, nil, err
		}
		return mcpResult(response.Body)
	})
	addMCPTool(a, server, &mcp.Tool{
		Name:        "archive_pipeline",
		Description: "Archive a pipeline: its revisions stay readable but none is active, its key is taken for good, and no request can reach it. Refused for the entry pipeline and while any active revision still delegates to it (remove those delegate blocks first).",
		Annotations: mcpReplacing,
	}, func(call *mcpCall, input mcpPipelineKeyInput) (*mcp.CallToolResult, any, error) {
		if _, err := call.do(http.MethodDelete, pipelinePath(input.PipelineKey), nil, nil, nil); err != nil {
			return nil, nil, err
		}
		return mcpObject(map[string]any{"pipeline_key": input.PipelineKey, "archived": true})
	})
	addMCPTool(a, server, &mcp.Tool{
		Name:        "list_pipeline_revisions",
		Description: "List a pipeline's revisions, newest first, with which one is active. Use it to find a revision to roll back to.",
		Annotations: mcpReadOnly,
	}, func(call *mcpCall, input mcpPipelineKeyInput) (*mcp.CallToolResult, any, error) {
		response, err := call.get(pipelinePath(input.PipelineKey)+"/revisions", nil)
		if err != nil {
			return nil, nil, err
		}
		return mcpResult(response.Body)
	})
	addMCPTool(a, server, &mcp.Tool{
		Name:        "get_pipeline_revision",
		Description: "Read one pipeline revision, active or not, with its blocks.",
		Annotations: mcpReadOnly,
	}, func(call *mcpCall, input mcpPipelineRevisionInput) (*mcp.CallToolResult, any, error) {
		response, err := call.get(pipelinePath(input.PipelineKey)+"/revisions/"+strconv.FormatInt(input.Revision, 10), nil)
		if err != nil {
			return nil, nil, err
		}
		return mcpResult(response.Body)
	})
	addMCPTool(a, server, &mcp.Tool{
		Name: "update_pipeline",
		Description: "Save a new revision of a pipeline (the entry pipeline, or one a delegate hands requests to) and activate it (unless activate is false). " +
			"Blocks run in order and the first to decide wins: always blocks decide every request, cel blocks decide when their condition matches, webhook blocks ask a service of yours (and a kind this deployment adds may ask a model), " +
			"branch blocks run their own blocks only for requests their condition matches, and delegate blocks hand matching requests to another pipeline by key; a request that enters a branch or a delegated pipeline is decided inside it or goes to a human, and nothing after the block runs for it. " +
			"A request no block decides falls through to human review, so end with an always block whose outcome is approve when the rest should be automatic. " +
			"Every escalation path a block or the default names, and every pipeline a delegate names, must exist; activation additionally requires each to be active and refuses a delegation cycle. Call read_reference (pipeline_blocks) first.",
		Annotations: mcpAdditive,
	}, func(call *mcpCall, input mcpUpdatePipelineInput) (*mcp.CallToolResult, any, error) {
		base := pipelinePath(input.PipelineKey)
		body := map[string]any{"blocks": writableBlocks(input.Blocks)}
		if input.Name != "" {
			body["name"] = input.Name
		}
		if input.DefaultEscalationPath != "" {
			body["default_escalation_path"] = input.DefaultEscalationPath
		}
		if input.DefaultAskAgentOwner {
			body["default_ask_agent_owner"] = true
		}
		created, err := call.post(base+"/revisions", body, nil)
		if err != nil {
			return nil, nil, err
		}
		if !activateWanted(input.Activate) {
			return mcpObject(map[string]any{"revision": json.RawMessage(created.Body), "activated": false})
		}
		number, err := revisionNumber(created.Body)
		if err != nil {
			return nil, nil, err
		}
		activated, err := call.activatePipeline(base, number)
		if err != nil {
			return nil, nil, fmt.Errorf("revision %d was saved as a draft but not activated; fix the cause and call activate_pipeline_revision: %w", number, err)
		}
		return mcpObject(map[string]any{"revision": json.RawMessage(activated), "activated": true})
	})
	addMCPTool(a, server, &mcp.Tool{
		Name:        "activate_pipeline_revision",
		Description: "Make an existing pipeline revision the active one (a rollback, or activating a draft). New requests use it immediately; requests already in flight keep the revision they started on.",
		Annotations: mcpReplacing,
	}, func(call *mcpCall, input mcpPipelineRevisionInput) (*mcp.CallToolResult, any, error) {
		activated, err := call.activatePipeline(pipelinePath(input.PipelineKey), input.Revision)
		if err != nil {
			return nil, nil, err
		}
		return mcpResult(activated)
	})
	addMCPTool(a, server, &mcp.Tool{
		Name: "preview_pipeline",
		Description: "Run a sample request through a set of blocks and report the outcome (approve, deny, human, or next), which block decided, its reason, and the escalation path a human exit would take. " +
			"Each step lists a block the request reached, with the pipeline it belongs to; a branch or delegate reports enter or next for its condition, and end_of_branch when the request ran out of its blocks (for a delegate, out of the target pipeline). " +
			"Delegate blocks expand to their targets' current active revisions, so the preview walks what a request would. Nothing is saved and no approval request is created. webhook blocks and other asynchronous kinds really call the endpoint or the model.",
		Annotations: mcpReadOnly,
	}, func(call *mcpCall, input mcpPreviewPipelineInput) (*mcp.CallToolResult, any, error) {
		arguments := input.Sample.Arguments
		if arguments == nil {
			arguments = map[string]any{}
		}
		slug := input.Sample.AgentSlug
		if slug == "" {
			slug = "agent"
		}
		name := input.Sample.AgentName
		if name == "" {
			name = slug
		}
		request := map[string]any{"tool": input.Sample.Tool, "arguments": arguments}
		if input.Sample.AgentReasoning != "" {
			request["agent_reasoning"] = input.Sample.AgentReasoning
		}
		if input.Sample.Context != nil {
			request["context"] = input.Sample.Context
		}
		body := map[string]any{
			"blocks": writableBlocks(input.Blocks),
			"sample": map[string]any{"request": request, "agent": map[string]any{"slug": slug, "name": name}},
		}
		if input.DefaultEscalationPath != "" {
			body["default_escalation_path"] = input.DefaultEscalationPath
		}
		if input.DefaultAskAgentOwner {
			body["default_ask_agent_owner"] = true
		}
		response, err := call.post(pipelinePath(input.PipelineKey)+"/preview", body, nil)
		if err != nil {
			return nil, nil, err
		}
		return mcpResult(response.Body)
	})

	// Escalation paths.
	addMCPTool(a, server, &mcp.Tool{
		Name:        "list_escalation_paths",
		Description: "List escalation paths with their key, name, active revision and latest revision.",
		Annotations: mcpReadOnly,
	}, func(call *mcpCall, input mcpStatusInput) (*mcp.CallToolResult, any, error) {
		response, err := call.get(mcpPathsPath, statusQuery(input.Status))
		if err != nil {
			return nil, nil, err
		}
		return mcpResult(response.Body)
	})
	addMCPTool(a, server, &mcp.Tool{
		Name:        "get_escalation_path",
		Description: "Read an escalation path's active revision: its nodes, working hours, repeat setting and warnings. Not found when the path has no active revision; use list_escalation_path_revisions then.",
		Annotations: mcpReadOnly,
	}, func(call *mcpCall, input mcpPathKeyInput) (*mcp.CallToolResult, any, error) {
		response, err := call.get(mcpPathsPath+"/"+url.PathEscape(input.PathKey), nil)
		if err != nil {
			return nil, nil, err
		}
		return mcpResult(response.Body)
	})
	addMCPTool(a, server, &mcp.Tool{
		Name:        "list_escalation_path_revisions",
		Description: "List an escalation path's revisions, newest first, with which one is active.",
		Annotations: mcpReadOnly,
	}, func(call *mcpCall, input mcpPathKeyInput) (*mcp.CallToolResult, any, error) {
		response, err := call.get(mcpPathsPath+"/"+url.PathEscape(input.PathKey)+"/revisions", nil)
		if err != nil {
			return nil, nil, err
		}
		return mcpResult(response.Body)
	})
	addMCPTool(a, server, &mcp.Tool{
		Name: "create_escalation_path",
		Description: "Create an escalation path with its first revision and activate it (unless activate is false). " +
			"A path says who reviews a request the pipeline hands to a person: level nodes page targets (user targets by membership id, team targets by team id) and wait escalate_after before the next level; if_else nodes branch on urgency, working hours or the request itself; defer nodes hold the request until working hours. " +
			"Call read_reference (escalation_paths) first. The key is permanent: pipelines name it, and it stays taken even after archiving.",
		Annotations: mcpAdditive,
	}, func(call *mcpCall, input mcpEscalationPathInput) (*mcp.CallToolResult, any, error) {
		body := escalationPathBody(input)
		body["path_key"] = input.PathKey
		created, err := call.post(mcpPathsPath, body, nil)
		if err != nil {
			return nil, nil, err
		}
		return call.finishEscalationPathRevision(input.PathKey, created.Body, input.Activate)
	})
	addMCPTool(a, server, &mcp.Tool{
		Name:        "update_escalation_path",
		Description: "Save a new revision of an existing escalation path (the whole document again, not a diff) and activate it unless activate is false. Pipelines that name the key pick up the active revision for new requests; requests already routed keep the revision they were pinned to.",
		Annotations: mcpAdditive,
	}, func(call *mcpCall, input mcpEscalationPathInput) (*mcp.CallToolResult, any, error) {
		created, err := call.post(mcpPathsPath+"/"+url.PathEscape(input.PathKey)+"/revisions", escalationPathBody(input), nil)
		if err != nil {
			return nil, nil, err
		}
		return call.finishEscalationPathRevision(input.PathKey, created.Body, input.Activate)
	})
	addMCPTool(a, server, &mcp.Tool{
		Name:        "activate_escalation_path_revision",
		Description: "Make an existing escalation path revision the active one (a rollback, or activating a draft).",
		Annotations: mcpReplacing,
	}, func(call *mcpCall, input mcpPathRevisionInput) (*mcp.CallToolResult, any, error) {
		activated, err := call.activateEscalationPath(input.PathKey, input.Revision)
		if err != nil {
			return nil, nil, err
		}
		return mcpResult(activated)
	})
	addMCPTool(a, server, &mcp.Tool{
		Name:        "archive_escalation_path",
		Description: "Archive an escalation path for good: it stops being active, refuses new revisions, and keeps its key and history. Refused while any active pipeline revision names it; update those pipelines first.",
		Annotations: mcpArchiving,
	}, func(call *mcpCall, input mcpPathKeyInput) (*mcp.CallToolResult, any, error) {
		response, err := call.do(http.MethodDelete, mcpPathsPath+"/"+url.PathEscape(input.PathKey), nil, nil, nil)
		if err != nil {
			return nil, nil, err
		}
		return mcpResult(response.Body)
	})

	// Webhook endpoints.
	addMCPTool(a, server, &mcp.Tool{
		Name:        "list_webhook_endpoints",
		Description: "List webhook endpoints (the URLs a pipeline's webhook block can ask for an outcome) with their key, active revision, and which pipelines use them.",
		Annotations: mcpReadOnly,
	}, func(call *mcpCall, _ struct{}) (*mcp.CallToolResult, any, error) {
		response, err := call.get(mcpWebhooksPath, nil)
		if err != nil {
			return nil, nil, err
		}
		return mcpResult(response.Body)
	})
	addMCPTool(a, server, &mcp.Tool{
		Name:        "get_webhook_endpoint",
		Description: "Read a webhook endpoint's active revision: name and URL. The signing secret is never returned here; it was shown once when the endpoint was created.",
		Annotations: mcpReadOnly,
	}, func(call *mcpCall, input mcpEndpointKeyInput) (*mcp.CallToolResult, any, error) {
		response, err := call.get(mcpWebhooksPath+"/"+url.PathEscape(input.EndpointKey), nil)
		if err != nil {
			return nil, nil, err
		}
		return mcpResult(response.Body)
	})
	addMCPTool(a, server, &mcp.Tool{
		Name: "save_webhook_endpoint",
		Description: "Create a webhook endpoint or save a new revision of one, and activate it unless activate is false. " +
			"A new key answers with signing_secret exactly once: relay it to whoever operates the receiving service, because it is what verifies withHuman's signature and it cannot be read back. " +
			"The URL must be public HTTPS.",
		Annotations: mcpAdditive,
	}, func(call *mcpCall, input mcpSaveWebhookEndpointInput) (*mcp.CallToolResult, any, error) {
		base := mcpWebhooksPath + "/" + url.PathEscape(input.EndpointKey)
		created, err := call.post(base+"/revisions", map[string]any{"name": input.Name, "url": input.URL}, nil)
		if err != nil {
			return nil, nil, err
		}
		fields := map[string]any{"revision": json.RawMessage(created.Body), "activated": false}
		var minted struct {
			SigningSecret string `json:"signing_secret"`
		}
		if json.Unmarshal(created.Body, &minted) == nil && minted.SigningSecret != "" {
			fields["signing_secret"] = minted.SigningSecret
		}
		if !activateWanted(input.Activate) {
			return mcpObject(fields)
		}
		number, err := revisionNumber(created.Body)
		if err != nil {
			return nil, nil, err
		}
		activated, err := call.activateRevisioned(base, number)
		if err != nil {
			return nil, nil, fmt.Errorf("revision %d was saved but not activated: %w", number, err)
		}
		fields["revision"], fields["activated"] = json.RawMessage(activated), true
		return mcpObject(fields)
	})

	// Reference.
	addMCPTool(a, server, &mcp.Tool{
		Name:        "read_reference",
		Description: "Read the exact grammar for pipeline blocks, conditions, or escalation path documents. Read the relevant topic before writing one: unknown fields are refused and ids must be UUIDs.",
		Annotations: mcpReadOnly,
	}, func(_ *mcpCall, input mcpReferenceInput) (*mcp.CallToolResult, any, error) {
		text, err := mcpreference.Read(input.Topic)
		if err != nil {
			return nil, nil, err
		}
		// The edition's block kinds append their grammar; their valid
		// dependencies (a judge model catalog) are deployment configuration
		// the edition renders into it.
		if strings.TrimSpace(input.Topic) == "pipeline_blocks" && a.compiledPipelines != nil {
			text += a.compiledPipelines.Grammar()
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
	})
}

func statusQuery(status string) url.Values {
	if status == "" {
		return nil
	}
	return url.Values{"status": {status}}
}

func pipelinePath(pipelineKey string) string {
	return mcpPipelinesPath + "/" + url.PathEscape(pipelineKey)
}

func activateWanted(activate *bool) bool { return activate == nil || *activate }

// writableBlocks strips the fields a stored block carries beyond its
// definition (snapshot id, config version, content hash), at every level of
// a branch, so an agent can pass back exactly what get_pipeline returned.
func writableBlocks(blocks []map[string]any) []map[string]any {
	if blocks == nil {
		return []map[string]any{}
	}
	cleaned := make([]map[string]any, len(blocks))
	for index, block := range blocks {
		cleaned[index] = writableBlock(block)
	}
	return cleaned
}

func writableBlock(block map[string]any) map[string]any {
	copied := make(map[string]any, len(block))
	for key, value := range block {
		switch key {
		case "snapshot_id", "config_version", "content_hash":
			continue
		}
		copied[key] = value
	}
	config, isObject := copied["config"].(map[string]any)
	if copied["type"] != string(approvalblock.TypeBranch) || !isObject {
		return copied
	}
	children, isList := config["blocks"].([]any)
	if !isList {
		return copied
	}
	nested := make(map[string]any, len(config))
	for key, value := range config {
		nested[key] = value
	}
	cleanedChildren := make([]any, len(children))
	for index, child := range children {
		if childBlock, isBlock := child.(map[string]any); isBlock {
			cleanedChildren[index] = writableBlock(childBlock)
		} else {
			cleanedChildren[index] = child
		}
	}
	nested["blocks"] = cleanedChildren
	copied["config"] = nested
	return copied
}

func revisionNumber(body []byte) (int64, error) {
	var created struct {
		Revision int64 `json:"revision"`
	}
	if err := json.Unmarshal(body, &created); err != nil || created.Revision <= 0 {
		return 0, fmt.Errorf("the revision was saved but its number could not be read; list the revisions and activate it by number")
	}
	return created.Revision, nil
}

func escalationPathBody(input mcpEscalationPathInput) map[string]any {
	nodes := input.Nodes
	if nodes == nil {
		nodes = []map[string]any{}
	}
	workingHours := input.WorkingHours
	if workingHours == nil {
		workingHours = []map[string]any{}
	}
	body := map[string]any{"name": input.Name, "nodes": nodes, "working_hours": workingHours}
	if input.Repeat != nil {
		body["repeat"] = input.Repeat
	}
	return body
}

// activatePipeline activates one revision against whatever is active
// now: a pipeline always has an active revision, and its number is the
// If-Match the API demands.
func (c *mcpCall) activatePipeline(base string, revision int64) ([]byte, error) {
	current, err := c.get(base, nil)
	if err != nil {
		return nil, err
	}
	etag := current.Header.Get("ETag")
	if etag == "" {
		return nil, fmt.Errorf("the active revision could not be read")
	}
	activated, err := c.post(base+"/revisions/"+strconv.FormatInt(revision, 10)+"/activate", nil, http.Header{"If-Match": {etag}})
	if err != nil {
		return nil, err
	}
	return activated.Body, nil
}

// activateRevisioned activates one revision of a resource that may have
// no active revision yet (an escalation path or webhook endpoint): the
// If-Match is the active revision's number, or "0" for none.
func (c *mcpCall) activateRevisioned(base string, revision int64) ([]byte, error) {
	etag := mcpNoActiveRevision
	current, err := c.get(base, nil)
	if err != nil {
		var refusal *mcpToolError
		if !errors.As(err, &refusal) || refusal.Status != http.StatusNotFound {
			return nil, err
		}
	} else if current.Header.Get("ETag") != "" {
		etag = current.Header.Get("ETag")
	}
	activated, err := c.post(base+"/revisions/"+strconv.FormatInt(revision, 10)+"/activate", nil, http.Header{"If-Match": {etag}})
	if err != nil {
		return nil, err
	}
	return activated.Body, nil
}

func (c *mcpCall) activateEscalationPath(pathKey string, revision int64) ([]byte, error) {
	return c.activateRevisioned(mcpPathsPath+"/"+url.PathEscape(pathKey), revision)
}

// finishEscalationPathRevision answers a create or update: the saved
// revision, activated when asked, with the draft number in the error
// when activation was refused.
func (c *mcpCall) finishEscalationPathRevision(pathKey string, created []byte, activate *bool) (*mcp.CallToolResult, any, error) {
	if !activateWanted(activate) {
		return mcpObject(map[string]any{"revision": json.RawMessage(created), "activated": false})
	}
	number, err := revisionNumber(created)
	if err != nil {
		return nil, nil, err
	}
	activated, err := c.activateEscalationPath(pathKey, number)
	if err != nil {
		return nil, nil, fmt.Errorf("revision %d was saved as a draft but not activated; fix the cause and call activate_escalation_path_revision: %w", number, err)
	}
	return mcpObject(map[string]any{"revision": json.RawMessage(activated), "activated": true})
}
