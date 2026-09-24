import type { Tone } from "@/ds/components/badge";
import type { ApprovalStatus } from "../types";

/* One vocabulary for a request's status wherever it is shown: the queue
 * list, the detail view, and an agent's history. */
export const STATUS_TONE: Record<ApprovalStatus, Tone> = {
  pending: "warn",
  approved: "approve",
  denied: "deny",
  expired: "neutral",
  cancelled: "neutral",
};

export const STATUS_LABEL: Record<ApprovalStatus, string> = {
  pending: "Pending",
  approved: "Approved",
  denied: "Denied",
  expired: "Expired",
  cancelled: "Withdrawn",
};
