import { useMemo, useState } from "react";
import { Link, useNavigate, useRouter } from "@tanstack/react-router";
import { Badge } from "@/ds/components/badge";
import { Button } from "@/ds/components/button";
import { Callout } from "@/ds/components/callout";
import { Card } from "@/ds/components/card";
import { IconButton } from "@/ds/components/icon-button";
import { Menu, MenuContent, MenuTrigger } from "@/ds/components/menu";
import {
  PipelineConnector,
  PipelineEntryCard,
  PipelineTerminusCard,
} from "@/ds/components/policy-block";
import { Referenced } from "@/ds/components/reference";
import { ReferenceChip } from "./reference-chip";
import type { ReviewerTarget } from "@/ds/components/reviewer-target";
import { Table, type TableColumn } from "@/ds/components/table";
import { TextField } from "@/ds/components/text-field";
import { ToggleGroup, ToggleGroupItem } from "@/ds/components/toggle-group";
import { Page } from "@/ds/components/page";
import { useToast } from "@/ds/components/toast";
import { BranchIcon, EllipsisIcon } from "@/ds/icons";
import { api } from "../api";
import { useAccess } from "./authorization";
import { formatRelative } from "./format";
import {
  defaultSample,
  definitionsFromStored,
  handOffTestSample,
  asksAgentOwner,
  loadPathDirectory,
  pathKeyOf,
  pathTarget,
  routeKeyOf,
} from "./pipeline-blocks";
import { TestRequestDialog } from "./pipeline-view";
import type {
  RequestApprovalPipelineRevision,
  RequestApprovalPipelineSummary,
} from "../types";
import { useTitle } from "./use-title";
import { useRevisionActivation } from "./use-revision-activation";

/*
 * The approval pipelines overview, drawn as the pipeline every request
 * experiences: entry, the organization pipeline (runs first), one
 * labeled fan-out into the table of agents, and the human terminus.
 */

/* What the pipelines route loader delivers. A failed load is reported
 * by the screen rather than thrown, so an expired session reaches the
 * sign-in gate, not the router's error boundary. */
export interface PipelinesPage {
  pipelines: RequestApprovalPipelineSummary[];
  /* Each agent's display name by slug, from the inventory the viewer may
   * read; a pipeline whose agent is missing here shows its slug alone. */
  agentNames: Record<string, string>;
  organizationRevision: RequestApprovalPipelineRevision | null;
  /* The organization's escalation paths, the choices for the default. */
  directory: ReviewerTarget[];
  defaultReviewers: ReviewerTarget | null;
  error: string;
}

/* The agent table's filter: the pipelines still in service (the
 * default), the ones archived with their agent, or both. */
type PipelineFilter = "all" | "active" | "archived";

export async function loadPipelinesPage(): Promise<PipelinesPage> {
  try {
    const response = await api.requestApprovalPipelines("all");
    const pipelines = response.request_approval_pipelines;
    const organization = pipelines.find((pipeline) => pipeline.scope === "organization");
    /* Read the active revision to resolve the organization default
     * reviewers, and the paths so the terminus card can offer them. */
    const [organizationRevision, directory, agents] = await Promise.all([
      organization?.active_revision
        ? api.requestApprovalPipelineRevision("", organization.active_revision)
        : Promise.resolve(null),
      loadPathDirectory(),
      /* Names are decoration on the rows; the pipelines list stands
       * without them. */
      api.agents("all").then((response) => response.agents, () => []),
    ]);
    return {
      pipelines,
      agentNames: Object.fromEntries(agents.map((agent) => [agent.slug, agent.name])),
      organizationRevision,
      directory,
      defaultReviewers: pathTarget(organizationRevision?.default_escalation_path, directory, organizationRevision?.default_ask_agent_owner),
      error: "",
    };
  } catch (reason) {
    return {
      pipelines: [],
      agentNames: {},
      organizationRevision: null,
      directory: [],
      defaultReviewers: null,
      error: reason instanceof Error ? reason.message : "Unable to load approval pipelines.",
    };
  }
}

export function PipelinesScreen({ page }: { page: PipelinesPage }) {
  const navigate = useNavigate();
  const router = useRouter();
  const { can, holdsAnywhere, scopeOf } = useAccess();
  /* null when the viewer reads every pipeline; otherwise the slugs of
   * the agents they manage. The server filters the list the same way,
   * and the organization pipeline is out of a scoped reader's reach. */
  const managedAgents = useMemo<string[] | null>(() => {
    const scope = scopeOf("pipeline.read");
    if (scope === "all") return null;
    return scope.filter((entry) => entry.kind === "agent").map((entry) => entry.id);
  }, [scopeOf]);
  const toast = useToast();
  const revisionActivation = useRevisionActivation();
  const { pipelines, agentNames, organizationRevision, directory, defaultReviewers, error } = page;
  const [filter, setFilter] = useState("");
  const [status, setStatus] = useState<PipelineFilter>("active");
  useTitle("Approval pipelines");

  const agentPipelines = useMemo(
    () => pipelines.filter((pipeline) => pipeline.scope === "agent"),
    [pipelines],
  );
  const archivedCount = agentPipelines.filter((pipeline) => pipeline.archived_at).length;
  const agentRows = useMemo(() => {
    const query = filter.trim().toLowerCase();
    return agentPipelines.filter((pipeline) => {
      if (status === "active" && pipeline.archived_at) return false;
      if (status === "archived" && !pipeline.archived_at) return false;
      const slug = pipeline.agent_slug ?? "";
      return (
        slug.toLowerCase().includes(query) ||
        (agentNames[slug] ?? "").toLowerCase().includes(query)
      );
    });
  }, [agentPipelines, agentNames, filter, status]);
  const agentTotal = agentPipelines.length;

  /* Testing from the overview: the sample names an agent by slug, and
   * the run opens that pipeline, which replays the whole order
   * (organization blocks first) on its own rail. */
  const canTest = holdsAnywhere("pipeline.write");
  const [testOpen, setTestOpen] = useState(false);
  const [sampleText, setSampleText] = useState(() => {
    const first = pipelines.find(
      (pipeline) => pipeline.scope === "agent" && !pipeline.archived_at,
    )?.agent_slug;
    return JSON.stringify(defaultSample(first ?? "example"), null, 2);
  });
  async function runTest(): Promise<string | null> {
    const sample = JSON.parse(sampleText) as { agent?: { slug?: unknown } };
    const slug = typeof sample.agent?.slug === "string" ? sample.agent.slug.trim() : "";
    if (!slug) return "Set agent.slug in the sample to the agent sending the request.";
    const target = pipelines.find((pipeline) => pipeline.agent_slug === slug);
    if (!target) {
      return `No agent has the slug ${slug}. Use one of the agents listed below.`;
    }
    if (target.archived_at) {
      return `The ${slug} pipeline was archived with its agent. Restore the agent to test it.`;
    }
    if (!can("pipeline.write", { kind: "agent", id: slug })) {
      return `You cannot test the ${slug} pipeline.`;
    }
    handOffTestSample(slug, sampleText);
    setTestOpen(false);
    await navigate({ to: "/pipelines/agents/$agentSlug", params: { agentSlug: slug } });
    return null;
  }

  /* The shortcut saves an organization pipeline revision, then uses the
   * same activation choice as the full pipeline editor. */
  const canEditDefault =
    organizationRevision !== null && can("pipeline.write", { kind: "organization" });
  const [editingDefault, setEditingDefault] = useState(false);
  const [draftDefault, setDraftDefault] = useState<ReviewerTarget | null>(null);
  const [savingDefault, setSavingDefault] = useState(false);
  function startEditDefault() {
    setDraftDefault(defaultReviewers);
    setEditingDefault(true);
  }
  function cancelDefault() {
    setEditingDefault(false);
  }
  async function saveDefault() {
    if (!organizationRevision) return;
    if (
      routeKeyOf(draftDefault) ===
      routeKeyOf(pathTarget(organizationRevision.default_escalation_path, [], organizationRevision.default_ask_agent_owner))
    ) {
      setEditingDefault(false);
      return;
    }
    setSavingDefault(true);
    try {
      /* The blocks go back exactly as stored; only the default changes. */
      const key = pathKeyOf(draftDefault);
      const revision = await api.createRequestApprovalPipelineRevision("", {
        blocks: definitionsFromStored(organizationRevision.blocks),
        ...(key ? { default_escalation_path: key } : {}),
        ...(key && asksAgentOwner(draftDefault) ? { default_ask_agent_owner: true } : {}),
      });
      await revisionActivation.offer({
        revision: revision.revision,
        activeRevision: organizationRevision.revision,
        canActivate: can("pipeline.activate", { kind: "organization" }),
        activationDescription: "Once active, new requests use these default reviewers.",
        activate: () => api.activateRequestApprovalPipelineRevision("", revision.revision, organizationRevision.revision),
      });
      setEditingDefault(false);
      await router.invalidate();
    } catch (reason) {
      toast.add({
        type: "deny",
        title: "The default reviewers could not be saved",
        description: reason instanceof Error ? reason.message : undefined,
      });
    } finally {
      setSavingDefault(false);
    }
  }

  const columns = useMemo<TableColumn<RequestApprovalPipelineSummary>[]>(
    () => [
      {
        id: "agent",
        label: "Agent",
        grow: true,
        cell: (pipeline) => {
          const slug = pipeline.agent_slug ?? "";
          const name = agentNames[slug];
          return (
            <span
              className="flex min-w-0 flex-col items-start gap-0.5"
              onClick={(event) => event.stopPropagation()}
              onKeyDown={(event) => event.stopPropagation()}
            >
              <ReferenceChip
                reference={{ kind: "pipeline", agentSlug: slug }}
                link={{ to: "/pipelines/agents/$agentSlug", params: { agentSlug: slug } }}
              >
                {name ?? <span className="font-mono text-[12px]">{slug}</span>}
              </ReferenceChip>
              {name ? (
                <span className="pl-2 font-mono text-[12px] text-ink-muted">{slug}</span>
              ) : null}
            </span>
          );
        },
      },
      {
        id: "status",
        label: "Status",
        info: "The version used for new requests, or when this pipeline was archived.",
        docsLinks: [{ href: "https://withhuman.ai/docs/web-app/approval-pipelines#drafts-and-activation", label: "Activating a pipeline" }],
        width: 170,
        cell: (pipeline) =>
          pipeline.archived_at ? (
            <Badge tone="warn">Archived {new Date(pipeline.archived_at).toLocaleDateString()}</Badge>
          ) : (
            <Badge tone="approve">Active revision {pipeline.active_revision}</Badge>
          ),
      },
      {
        id: "revisions",
        label: "Revisions",
        info: "Saved versions of this pipeline.",
        docsLinks: [{ href: "https://withhuman.ai/docs/web-app/approval-pipelines#revision-history-and-rollback", label: "Revision history and rollback" }],
        width: 84,
        cell: (pipeline) => pipeline.revision_count,
      },
      {
        id: "updated",
        label: "Updated",
        info: "When changes to this pipeline were last saved.",
        docsLinks: [{ href: "https://withhuman.ai/docs/web-app/approval-pipelines#drafts-and-activation", label: "Saving pipeline changes" }],
        width: 110,
        cell: (pipeline) => (
          <span className="text-ink-muted">
            {formatRelative(pipeline.latest_created_at)} ago
          </span>
        ),
      },
    ],
    [agentNames],
  );

  return (
    <Page width="wide">
      {revisionActivation.dialog}
      <div className="flex flex-col items-start justify-between gap-4 sm:flex-row sm:gap-6">
        <div className="min-w-0">
          <h1 className="text-title-lg text-ink">Approval pipelines</h1>
          <p className="mt-1.5 max-w-[620px] text-body text-ink-muted">
            Approval pipelines decide what to do with an agent’s request.{" "}
            {managedAgents === null
              ? "Define rules that automatically approve, deny or route requests to a human."
              : "You see the rules for the agents you manage."}
          </p>
        </div>
        {canTest ? (
          <Button
            variant="ghost"
            className="shrink-0 self-end sm:self-auto"
            onClick={() => setTestOpen(true)}
          >
            Test request
          </Button>
        ) : null}
      </div>

      {error ? (
        <Callout tone="deny" title="Pipelines could not be loaded">
          <div className="flex items-center justify-between gap-4">
            <span>{error}</span>
            <Button size="sm" onClick={() => void router.invalidate()}>
              Try again
            </Button>
          </div>
        </Callout>
      ) : null}

      <div className="flex flex-col">
        <PipelineEntryCard />
        <PipelineConnector
          label={managedAgents === null ? "First, check organization rules" : "Check rules for your agents"}
        />

        {managedAgents === null ? (
        <Card
          role="button"
          tabIndex={0}
          onClick={() => void navigate({ to: "/pipelines/organization" })}
          onKeyDown={(event) => {
            if (event.key === "Enter" || event.key === " ") {
              event.preventDefault();
              void navigate({ to: "/pipelines/organization" });
            }
          }}
          className="flex cursor-pointer items-center gap-3 px-4 py-2.5 transition-colors duration-100 hover:bg-fill-hover"
        >
          <BranchIcon width={14} height={14} className="shrink-0 text-ink-muted" />
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
              <h3 className="text-label font-medium text-ink" onClick={(event) => event.stopPropagation()} onKeyDown={(event) => event.stopPropagation()}>
                <Referenced reference={{ kind: "pipeline", agentSlug: "" }} inline focusable={false}>
                  <Link to="/pipelines/organization">Organization pipeline</Link>
                </Referenced>
              </h3>
            </div>
            <p className="mt-0.5 text-micro text-ink-muted">
              These approval rules are shared across all agents and run for every request first.
            </p>
          </div>
          <span
            className="ml-auto flex items-center"
            onClick={(event) => event.stopPropagation()}
            onKeyDown={(event) => event.stopPropagation()}
          >
            <Menu
              actions={[
                {
                  id: "view",
                  group: "open",
                  label: "View pipeline",
                  action: () => void navigate({ to: "/pipelines/organization" }),
                },
                ...(canTest
                  ? [{ id: "test", label: "Test request", action: () => setTestOpen(true) }]
                  : []),
              ]}
            >
              <MenuTrigger
                render={
                  <IconButton aria-label="Actions for the organization pipeline" size="sm">
                    <EllipsisIcon width={15} height={15} />
                  </IconButton>
                }
              />
              <MenuContent align="end" />
            </Menu>
          </span>
        </Card>
        ) : null}

        {managedAgents === null ? (
          <PipelineConnector label="If no decision is made">
            <span className="text-micro text-ink-muted">
              continue to the pipeline for the agent that sent the request
            </span>
          </PipelineConnector>
        ) : null}

        <Card className="overflow-x-auto">
          <div className="flex flex-wrap items-center gap-3 border-b-hairline border-edge px-4 py-3">
            <TextField
              aria-label="Filter agents"
              placeholder="Filter agents"
              value={filter}
              onChange={(event) => setFilter(event.target.value)}
              className="w-[220px]"
            />
            <span className="text-micro text-ink-muted">
              {agentRows.length} of {agentTotal} agents
            </span>
            <span className="ml-auto flex items-center gap-3">
              {archivedCount > 0 ? (
                <ToggleGroup
                  aria-label="Filter pipelines"
                  value={status}
                  onValueChange={(value) => setStatus(value as PipelineFilter)}
                >
                  <ToggleGroupItem value="all">All</ToggleGroupItem>
                  <ToggleGroupItem value="active">Active</ToggleGroupItem>
                  <ToggleGroupItem value="archived">Archived ({archivedCount})</ToggleGroupItem>
                </ToggleGroup>
              ) : null}
            </span>
          </div>
          {agentRows.length ? (
            <Table
              aria-label="Agent pipelines"
              items={agentRows}
              getId={(pipeline) => pipeline.agent_slug ?? ""}
              selected={new Set()}
              onSelectedChange={() => undefined}
              selectable={false}
              openOnClick
              openLabel="View pipeline"
              onOpen={(pipeline) =>
                void navigate({
                  to: "/pipelines/agents/$agentSlug",
                  params: { agentSlug: pipeline.agent_slug ?? "" },
                })
              }
              columns={columns}
              rowLabel={(pipeline) => agentNames[pipeline.agent_slug ?? ""] ?? pipeline.agent_slug ?? "organization"}
              rowActions={(pipeline) => [
                {
                  id: "agent",
                  label: "View agent",
                  action: () =>
                    void navigate({
                      to: "/agents/$agentSlug",
                      params: { agentSlug: pipeline.agent_slug ?? "" },
                    }),
                },
              ]}
              className="min-w-[620px]"
            />
          ) : (
            <p className="px-4 py-6 text-body text-ink-muted">
              {agentTotal === 0 && managedAgents !== null ? (
                managedAgents.length > 0 ? (
                  <>
                    No pipelines for the agents you manage yet. You can manage{" "}
                    <span className="font-mono text-[12px] text-ink-secondary">
                      {managedAgents.join(", ")}
                    </span>
                    .
                  </>
                ) : (
                  "No pipelines for the agents you manage yet."
                )
              ) : agentTotal === 0 ? (
                "Connect an agent to start setting up its approval rules."
              ) : status === "archived" ? (
                "No archived pipeline matches. A pipeline is archived with its agent and restored with it."
              ) : status === "active" && agentRows.length === 0 && !filter.trim() ? (
                "Every pipeline is archived. Restore an agent or connect a new one to start a fresh pipeline."
              ) : (
                <>
                  No agent matches{" "}
                  <span className="font-mono text-[12px] text-ink-secondary">{filter.trim()}</span>.
                </>
              )}
            </p>
          )}
        </Card>

        <PipelineConnector label="If no decision is made" />
        <PipelineTerminusCard
          target={editingDefault ? draftDefault : defaultReviewers}
          directory={editingDefault ? directory : undefined}
          onTargetChange={editingDefault ? setDraftDefault : undefined}
          onSave={editingDefault ? () => void saveDefault() : undefined}
          onCancel={editingDefault ? cancelDefault : undefined}
          saving={savingDefault}
          actions={
            canEditDefault && !editingDefault
              ? [{ id: "edit", label: "Change default reviewers", action: startEditDefault }]
              : undefined
          }
        />
      </div>

      <TestRequestDialog
        open={testOpen}
        onOpenChange={setTestOpen}
        sampleText={sampleText}
        onSampleTextChange={setSampleText}
        onRun={runTest}
        description="See what would happen to a sample request, one block at a time. This does not create a real approval request."
      />
    </Page>
  );
}
