import { useState, type FormEvent } from "react";
import { useNavigate } from "@tanstack/react-router";
import { Button } from "@/ds/components/button";
import { Card, CardBody, CardFooter } from "@/ds/components/card";
import { EmptyState } from "@/ds/components/empty-state";
import { TextField } from "@/ds/components/text-field";
import { useToast } from "@/ds/components/toast";
import { Page } from "@/ds/components/page";
import { PageHeader, PageBackLink } from "@/ds/components/page-header";
import { AgentIcon } from "@/ds/icons";
import { api } from "../api";
import type { Agent } from "../types";
import { useAccess } from "./authorization";
import { useTitle } from "./use-title";

/* The slug pattern the server enforces: a lowercase letter or digit,
 * then up to 62 letters, digits, dots, dashes or underscores. */
export const AGENT_SLUG_PATTERN = /^[a-z0-9][a-z0-9._-]{0,62}$/;
const AGENT_SLUG_MAX_LENGTH = 63;

/* A slug proposed from a display name: lowercased, every run of other
 * characters folded to one dash, leading and trailing dashes dropped,
 * cut to the pattern's length. "Support bot" becomes "support-bot". */
export function slugFromName(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9._-]+/g, "-")
    .replace(/^[^a-z0-9]+/, "")
    .slice(0, AGENT_SLUG_MAX_LENGTH)
    .replace(/[^a-z0-9]+$/, "");
}

/* An agent is named once by its slug, which its pipeline, URLs and
 * role scopes carry for good, and labeled by a display name that can
 * change. Instances and their credentials are added separately from the
 * agent page. */
export function ConnectScreen({ canCreate }: { canCreate: boolean }) {
  useTitle("Add New Agent");
  const navigate = useNavigate();
  return (
    <Page width="narrow">
      <PageHeader back={<PageBackLink to="/agents">Agents</PageBackLink>}>
        <div>
          <h1 className="text-title-lg text-ink">Add New Agent</h1>
          <p className="mt-1.5 max-w-[560px] text-body text-ink-muted">
            An agent is one identity whose instances share an approval
            pipeline. For example, you might have a single agent for your
            coding assistants, with an instance for each machine they run
            on, such as claude-code-desktop and codex-laptop.
          </p>
        </div>
      </PageHeader>

      {!canCreate ? (
        <Card>
          <EmptyState
            icon={<AgentIcon width={18} height={18} />}
            title="You do not have permission to add agents"
            description="Ask an administrator to add the agent or give you permission to create it."
          />
        </Card>
      ) : (
        <CreateAgentForm
          canCreate={canCreate}
          onCreated={(agent) =>
            navigate({ to: "/agents/$agentSlug", params: { agentSlug: agent.slug } })
          }
        />
      )}
    </Page>
  );
}

// Shared by standalone creation and the CLI enrollment's inline creation step.
export function CreateAgentForm({
  canCreate,
  forEnrollment = false,
  onCreated,
}: {
  canCreate: boolean;
  forEnrollment?: boolean;
  onCreated: (agent: Agent) => void | Promise<void>;
}) {
  const { can } = useAccess();
  const toast = useToast();
  const [name, setName] = useState("");
  const [slugInput, setSlugInput] = useState("");
  /* The slug follows the name until the person edits it themselves. */
  const [slugTouched, setSlugTouched] = useState(false);
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState("");
  const slug = slugTouched ? slugInput.trim() : slugFromName(name);
  const trimmedName = name.trim();
  const validSlug = AGENT_SLUG_PATTERN.test(slug);
  const allowed =
    canCreate &&
    can("agent.write", { kind: "agent", id: slug }) &&
    (!forEnrollment ||
      can("agent.credential.issue", { kind: "agent", id: slug }));
  const slugError =
    slug === ""
      ? undefined
      : !validSlug
        ? "Start with a lowercase letter or number, then use letters, numbers, dots, dashes or underscores."
        : !allowed
          ? "You do not have permission to create an agent with this slug."
          : undefined;
  const ready = trimmedName !== "" && validSlug && allowed;

  async function create(event: FormEvent) {
    event.preventDefault();
    if (creating || !ready) return;
    setCreating(true);
    setError("");
    try {
      const agent = await api.createAgent(slug, trimmedName);
      toast.add({ type: "approve", title: "Agent created" });
      await onCreated(agent);
    } catch (reason) {
      setError(
        reason instanceof Error
          ? reason.message
          : "Unable to create the agent. Try again.",
      );
    } finally {
      setCreating(false);
    }
  }

  return (
    <form onSubmit={create}>
      <Card>
        <CardBody className="flex flex-col gap-4">
          <TextField
            label="Name"
            value={name}
            onChange={(event) => {
              setName(event.target.value);
              setError("");
            }}
            placeholder="Support bot"
            description="What reviewers see on requests. You can rename the agent later."
            maxLength={120}
            disabled={creating}
            required
            autoFocus
          />
          <TextField
            label="Slug"
            mono
            value={slugTouched ? slugInput : slug}
            onChange={(event) => {
              setSlugTouched(true);
              setSlugInput(event.target.value);
              setError("");
            }}
            placeholder="support-bot"
            description="The permanent identifier for your agent. It cannot be changed or reused once the agent exists."
            error={slugError ?? (error || undefined)}
            maxLength={AGENT_SLUG_MAX_LENGTH}
            disabled={creating}
            required
          />
        </CardBody>
        <CardFooter>
          <Button
            type="submit"
            variant="primary"
            loading={creating}
            disabled={!ready}
          >
            Create agent
          </Button>
        </CardFooter>
      </Card>
    </form>
  );
}
