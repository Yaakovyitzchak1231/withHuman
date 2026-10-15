import { cn } from "@/ds/lib/cn";
import { Button } from "./button";

export interface SaveBarProps {
  /* Settings that differ from their saved values; the bar renders
   * only while > 0. Dirty means different-from-saved, not touched. */
  count: number;
  onDiscard: () => void;
  onSave: () => void;
  /* Commit verb when plain "Save" undersells it, e.g. an immutable
   * pipeline revision saves as "Create revision". */
  saveLabel?: string;
  /* What the bar says with nothing unsaved. Given, the bar stays up so
   * the save still runs, e.g. to activate a draft as it is. */
  idle?: string;
  /* With nothing unsaved, the Discard button appears under this label
   * (a way out of the idle state) only when it is given. */
  idleDiscardLabel?: string;
  saving?: boolean;
  className?: string;
}

/* Floating commit bar for explicit-save forms, the SelectionBar
 * grammar applied to pending edits: rises while dirty, Discard
 * resets, Save commits the batch. Escape never discards; that is a
 * deliberate click. */
export function SaveBar({
  count,
  onDiscard,
  onSave,
  saveLabel = "Save",
  idle,
  idleDiscardLabel,
  saving = false,
  className,
}: SaveBarProps) {
  if (count === 0 && idle === undefined) return null;
  return (
    <div
      className={cn(
        "ds-root fixed bottom-6 left-1/2 z-30 -translate-x-1/2",
        "flex items-center gap-1 rounded-lg bg-surface-overlay py-1.5 pr-1.5 pl-3",
        "shadow-overlay ring-hairline",
        "animate-[ds-rise_160ms_var(--ds-ease-out)]",
        className,
      )}
    >
      <span className="text-body font-medium whitespace-nowrap text-ink">
        {count === 0 ? idle : `${count} unsaved ${count === 1 ? "change" : "changes"}`}
      </span>
      <span className="mx-1.5 h-4 w-px shrink-0 bg-edge" />
      <div className="flex items-center gap-1.5">
        {count > 0 || idleDiscardLabel ? (
          <Button size="sm" variant="ghost" disabled={saving} onClick={onDiscard}>
            {count > 0 ? "Discard" : idleDiscardLabel}
          </Button>
        ) : null}
        <Button size="sm" variant="primary" loading={saving} onClick={onSave}>
          {saveLabel}
        </Button>
      </div>
    </div>
  );
}
