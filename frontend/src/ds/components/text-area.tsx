import { useId, type ComponentProps, type ReactNode } from "react";
import { cn } from "@/ds/lib/cn";
import type { InfoCardProps } from "./info-card";
import { InfoLabel } from "./info-label";

export interface TextAreaProps extends ComponentProps<"textarea"> {
  label?: string;
  info?: ReactNode;
  docsLinks?: InfoCardProps["docsLinks"];
  description?: string;
  error?: string;
}

/* Multiline sibling of TextField; grows with its content. */
export function TextArea({
  label,
  info,
  docsLinks,
  description,
  error,
  className,
  id: idProp,
  rows = 3,
  ...props
}: TextAreaProps) {
  const generatedId = useId();
  const id = idProp ?? generatedId;
  const detailId = `${id}-detail`;

  return (
    <div className="flex flex-col gap-1.5">
      {label ? (
        info ? (
          <InfoLabel info={info} title={label} docsLinks={docsLinks} className="text-label text-ink-secondary">
            <label htmlFor={id}>{label}</label>
          </InfoLabel>
        ) : <label htmlFor={id} className="text-label text-ink-secondary">{label}</label>
      ) : null}
      <textarea
        id={id}
        rows={rows}
        aria-invalid={error ? true : undefined}
        aria-describedby={description || error ? detailId : undefined}
        className={cn(
          "w-full rounded-sm border-hairline border-edge bg-surface px-2.5 py-1.5",
          "field-sizing-content resize-none",
          "text-body text-ink shadow-sm outline-none",
          "placeholder:text-ink-faint",
          "transition-[border-color,box-shadow] duration-100 ease-standard",
          "hover:border-edge-strong",
          "focus:border-accent focus:shadow-[0_0_0_2.5px_var(--ds-accent-subtle)] focus:hover:border-accent",
          "disabled:pointer-events-none disabled:opacity-50",
          error &&
            "border-deny hover:border-deny focus:border-deny focus:shadow-[0_0_0_2.5px_var(--ds-deny-subtle)]",
          className,
        )}
        {...props}
      />
      {error ? (
        <p id={detailId} className="text-micro font-normal text-deny">
          {error}
        </p>
      ) : description ? (
        <p id={detailId} className="text-micro font-normal text-ink-muted">
          {description}
        </p>
      ) : null}
    </div>
  );
}
