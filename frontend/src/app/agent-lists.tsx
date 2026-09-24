import { Button } from "@/ds/components/button";
import { Badge } from "@/ds/components/badge";
import { EmptyState } from "@/ds/components/empty-state";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
} from "@/ds/components/select";
import { Table, type TableColumn } from "@/ds/components/table";
import { TextField } from "@/ds/components/text-field";
import type { ApprovalRequest } from "../types";
import { STATUS_LABEL, STATUS_TONE } from "./approval-status";
import { AGENT_PAGE_SIZE, type AgentSearch } from "./agent-search";
import { argumentPreview, formatAbsolute, formatRelative } from "./format";
import { ReferenceChip } from "./reference-chip";

export function AgentListFilters({
  search,
  onChange,
  busy,
}: {
  search: AgentSearch;
  onChange: (search: AgentSearch) => void;
  busy: boolean;
}) {
  const instances = search.tab === "instances";
  const provisioners = search.tab === "provisioners";
  const searchLabel = provisioners
    ? "Search token identifiers"
    : instances
      ? "Search instances"
      : "Search tools or instances";
  const statuses = instances
    ? ["all", "active", "disabled"]
    : provisioners
      ? ["all", "active", "expired", "revoked"]
      : ["all", ...Object.keys(STATUS_LABEL)];
  const labels: Record<string, string> = {
    all: "All statuses",
    active: provisioners ? "Active" : "Enabled",
    revoked: "Revoked",
    disabled: "Disabled",
    ...STATUS_LABEL,
  };
  return (
    <div className="flex flex-wrap items-center justify-between gap-3">
      <h2 className="text-body font-medium text-ink">
        {provisioners
          ? "Provisioner tokens"
          : instances
            ? "Instances"
            : "Request history"}
      </h2>
      <div className="flex flex-wrap items-center gap-2">
        <form
          key={`${search.tab}:${search.q ?? ""}`}
          className="flex min-w-0 flex-wrap items-center gap-2"
          onSubmit={(event) => {
            event.preventDefault();
            const q = String(
              new FormData(event.currentTarget).get("q") ?? "",
            ).trim();
            onChange({ ...search, q: q || undefined, page: undefined });
          }}
        >
          <TextField
            name="q"
            aria-label={searchLabel}
            placeholder={searchLabel}
            defaultValue={search.q ?? ""}
            maxLength={200}
            className="w-[220px] max-w-full"
          />
          <Button type="submit" disabled={busy}>
            Search
          </Button>
        </form>
        <Select
          items={statuses}
          value={search.status ?? "all"}
          itemToStringLabel={(value) => labels[String(value)] ?? String(value)}
          onValueChange={(value) =>
            onChange({
              ...search,
              status:
                value === "all" ? undefined : (value as AgentSearch["status"]),
              page: undefined,
            })
          }
        >
          <SelectTrigger
            aria-label={
              provisioners
                ? "Filter token status"
                : instances
                  ? "Filter instance status"
                  : "Filter request status"
            }
            disabled={busy}
          />
          <SelectContent>
            {(value: string) => (
              <SelectItem key={value} value={value}>
                {labels[value]}
              </SelectItem>
            )}
          </SelectContent>
        </Select>
        {search.q || search.status ? (
          <Button
            variant="ghost"
            size="sm"
            disabled={busy}
            onClick={() => onChange({ tab: search.tab })}
          >
            Clear filters
          </Button>
        ) : null}
      </div>
    </div>
  );
}

export function AgentRequests({
  requests,
  filtered,
  onOpen,
}: {
  requests: ApprovalRequest[];
  filtered: boolean;
  onOpen: (request: ApprovalRequest) => void;
}) {
  const columns: TableColumn<ApprovalRequest>[] = [
    {
      id: "request",
      label: "Request",
      grow: true,
      cell: (request) => (
        <div className="min-w-0 py-1">
          <ReferenceChip
            reference={{ kind: "tool", name: request.tool, server: request.server }}
            className="max-w-full px-0"
          />
          <p className="truncate text-label font-normal text-ink-muted">
            {argumentPreview(request.arguments)}
          </p>
        </div>
      ),
    },
    {
      id: "instance",
      label: "Instance",
      width: 190,
      cell: (request) => (
        <p className="truncate text-body text-ink">
          {request.agent_instance_name || "No instance"}
        </p>
      ),
    },
    {
      id: "status",
      label: "Status",
      width: 115,
      cell: (request) => (
        <Badge className="px-0" tone={STATUS_TONE[request.status]}>
          {STATUS_LABEL[request.status]}
        </Badge>
      ),
    },
    {
      id: "received",
      label: "Received",
      width: 80,
      align: "right",
      cell: (request) => (
        <time
          dateTime={request.created_at}
          title={formatAbsolute(request.created_at)}
          className="text-label font-normal text-ink-muted"
        >
          {formatRelative(request.created_at)}
        </time>
      ),
    },
  ];
  return requests.length ? (
    <div className="overflow-x-auto">
      <Table
        aria-label="Agent requests"
        className="min-w-[720px]"
        items={requests}
        getId={(request) => request.id}
        selected={new Set()}
        onSelectedChange={() => undefined}
        selectable={false}
        columns={columns}
        rowLabel={(request) => request.tool}
        onOpen={onOpen}
      />
    </div>
  ) : (
    <EmptyState
      title={filtered ? "No matching requests" : "No requests yet"}
      description={
        filtered
          ? "Try another search or clear the filters."
          : "Requests appear here when this agent asks for approval."
      }
    />
  );
}
