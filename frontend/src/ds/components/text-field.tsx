import type { ComponentProps, ReactNode } from "react";
import { Field } from "@base-ui/react/field";
import { Input } from "@base-ui/react/input";
import { cn } from "@/ds/lib/cn";
import type { InfoCardProps } from "./info-card";
import { InfoLabel } from "./info-label";

export interface TextFieldProps extends ComponentProps<typeof Input> {
  label?: string;
  /* A short explanation of the field, the way a table column explains
   * itself: the label grows an info mark and opens an info card. */
  info?: ReactNode;
  docsLinks?: InfoCardProps["docsLinks"];
  description?: string;
  error?: string;
  /* Machine content (ids, tokens, hostnames): Roboto Mono, declared 1px
   * smaller than the Inter body size it sits beside. */
  mono?: boolean;
  /* A control that acts on the value, usually a Button: it sits on the
   * input's row, so the label above and the description below span both. */
  action?: ReactNode;
  /* A small marker at the far end of the label row (a badge, a count):
   * about the field, not part of its name. */
  labelEnd?: ReactNode;
}

export function TextField({
  label,
  info,
  docsLinks,
  description,
  error,
  mono = false,
  action,
  labelEnd,
  className,
  ...props
}: TextFieldProps) {
  const input = (
    <Input
      aria-invalid={error ? true : undefined}
      className={cn(
        "h-[30px] w-full rounded-sm border-hairline border-edge bg-surface px-2.5",
        "text-body text-ink shadow-sm outline-none",
        "placeholder:text-ink-faint",
        "transition-[border-color,box-shadow] duration-100 ease-standard",
        "hover:border-edge-strong",
        "focus:border-accent focus:shadow-[0_0_0_2.5px_var(--ds-accent-subtle)] focus:hover:border-accent",
        "disabled:pointer-events-none disabled:opacity-50",
        mono && "font-mono text-[12px]",
        error &&
          "border-deny hover:border-deny focus:border-deny focus:shadow-[0_0_0_2.5px_var(--ds-deny-subtle)]",
        className,
      )}
      {...props}
    />
  );
  const labelNode = label ? (
    info ? (
      <InfoLabel info={info} title={label} docsLinks={docsLinks} className="text-label text-ink-secondary">
        <Field.Label>{label}</Field.Label>
      </InfoLabel>
    ) : <Field.Label className="text-label text-ink-secondary">{label}</Field.Label>
  ) : null;
  return (
    <Field.Root invalid={error ? true : undefined} className="flex flex-col gap-1.5">
      {labelNode && labelEnd ? (
        <div className="flex items-center justify-between gap-2">
          {labelNode}
          {labelEnd}
        </div>
      ) : (
        labelNode
      )}
      {action ? (
        <div className="flex items-center gap-2">
          {input}
          <div className="shrink-0">{action}</div>
        </div>
      ) : (
        input
      )}
      {error ? (
        <p className="text-micro font-normal text-deny">{error}</p>
      ) : description ? (
        <Field.Description className="text-micro font-normal text-ink-muted">
          {description}
        </Field.Description>
      ) : null}
    </Field.Root>
  );
}
