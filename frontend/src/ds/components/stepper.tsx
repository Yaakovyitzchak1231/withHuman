import type { ComponentProps } from "react";
import { cn } from "@/ds/lib/cn";
import { CheckIcon } from "@/ds/icons";
import { Badge, type Tone } from "./badge";

export type StepStatus = "done" | "current" | "upcoming";

export interface Step {
  title: string;
  description?: string;
  status: StepStatus;
  /* A done step the person can return to. The title becomes a button;
   * steps without it are a record, not a control. */
  onSelect?: () => void;
}

export type StepperOrientation = "vertical" | "horizontal";

export interface StepperProps extends ComponentProps<"ol"> {
  steps: Step[];
  /* Vertical is the rail beside a flow's content; horizontal is the
   * band across the top of a screen the flow has to itself. */
  orientation?: StepperOrientation;
}

const STATUS_LABEL: Record<StepStatus, string> = {
  done: "Done",
  current: "Current",
  upcoming: "Next",
};

const STATUS_TONE: Record<StepStatus, Tone> = {
  done: "approve",
  current: "accent",
  upcoming: "neutral",
};

const NODE: Record<StepStatus, string> = {
  done: "bg-approve-subtle text-approve",
  current: "bg-accent text-ink-on-accent",
  upcoming: "border-hairline border-edge bg-surface text-ink-muted",
};

/*
 * Progress through a multi-step flow: numbered nodes joined by hairline
 * connectors, the node carrying the tone. Vertical is a rail, one row
 * per step with the status as a labeled Badge beside the title and room
 * for a description. Horizontal is a band across the top of a screen the
 * flow has to itself: titles under the nodes and nothing else, so it
 * stays quiet above the content. There the check, the filled current
 * node, and the title weight say where the flow is, and the status word
 * is kept for assistive technology only. A done step with onSelect is a
 * way back; the flow decides which steps offer one.
 */
export function Stepper({
  steps,
  orientation = "vertical",
  className,
  ...props
}: StepperProps) {
  if (orientation === "horizontal") {
    return (
      <ol className={cn("flex items-start", className)} {...props}>
        {steps.map((step, index) => (
          <li
            key={step.title}
            aria-current={step.status === "current" ? "step" : undefined}
            className="group flex min-w-0 flex-1 flex-col items-center gap-2"
          >
            <div className="flex w-full items-center">
              <span aria-hidden className="h-px flex-1 bg-edge group-first:invisible" />
              <Node status={step.status} index={index} />
              <span aria-hidden className="h-px flex-1 bg-edge group-last:invisible" />
            </div>
            <div className="flex min-w-0 max-w-full px-1 text-center">
              <Title step={step} />
              <span className="sr-only">{STATUS_LABEL[step.status]}</span>
            </div>
          </li>
        ))}
      </ol>
    );
  }

  return (
    <ol className={cn("flex flex-col", className)} {...props}>
      {steps.map((step, index) => (
        <li
          key={step.title}
          aria-current={step.status === "current" ? "step" : undefined}
          className="group relative flex gap-3 pb-6 last:pb-0"
        >
          <span
            aria-hidden
            className="absolute top-6 bottom-0 left-[9.5px] w-px bg-edge group-last:hidden"
          />
          <Node status={step.status} index={index} className="mt-px" />
          <div className="flex min-w-0 flex-1 flex-col gap-0.5">
            <div className="flex items-center justify-between gap-3">
              <Title step={step} />
              <Badge
                tone={STATUS_TONE[step.status]}
                icon={
                  step.status === "done" ? <CheckIcon width={14} height={14} /> : undefined
                }
                className="-mr-2"
              >
                {STATUS_LABEL[step.status]}
              </Badge>
            </div>
            {step.description ? (
              <p className="text-label font-normal text-ink-muted">{step.description}</p>
            ) : null}
          </div>
        </li>
      ))}
    </ol>
  );
}

function Node({
  status,
  index,
  className,
}: {
  status: StepStatus;
  index: number;
  className?: string;
}) {
  return (
    <span
      className={cn(
        "flex size-5 shrink-0 items-center justify-center rounded-full text-micro tabular-nums",
        NODE[status],
        className,
      )}
    >
      {status === "done" ? <CheckIcon width={11} height={11} strokeWidth={2.25} /> : index + 1}
    </span>
  );
}

function Title({ step }: { step: Step }) {
  if (step.onSelect) {
    return (
      <button
        type="button"
        onClick={step.onSelect}
        aria-label={`Go back to ${step.title}`}
        className="max-w-full truncate text-left text-body font-medium text-ink underline decoration-edge underline-offset-4 hover:decoration-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
      >
        {step.title}
      </button>
    );
  }
  return (
    <span
      className={cn(
        "max-w-full truncate text-body",
        step.status === "upcoming" ? "text-ink-muted" : "font-medium text-ink",
      )}
    >
      {step.title}
    </span>
  );
}
