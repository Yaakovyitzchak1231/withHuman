import type { ComponentProps, ReactNode } from "react";
import { Dialog as BaseDialog } from "@base-ui/react/dialog";
import { cn } from "@/ds/lib/cn";
import { focusRing } from "@/ds/lib/focus";
import { XIcon } from "@/ds/icons";

export const Dialog = BaseDialog.Root;
export const DialogTrigger = BaseDialog.Trigger;
export const DialogClose = BaseDialog.Close;

export function DialogContent({
  className,
  children,
  ...props
}: ComponentProps<typeof BaseDialog.Popup>) {
  return (
    <BaseDialog.Portal>
      <BaseDialog.Backdrop
        className={cn(
          "fixed inset-0 bg-scrim",
          "transition-opacity duration-160 ease-standard",
          "data-starting-style:opacity-0 data-ending-style:opacity-0",
        )}
      />
      <BaseDialog.Popup
        className={cn(
          /* ds-root re-establishes the design-system baseline inside the
           * portal, which mounts outside the styleguide's ds-root. */
          "ds-root fixed top-1/2 left-1/2 flex -translate-x-1/2 -translate-y-1/2 flex-col",
          "max-h-[calc(100dvh-32px)] w-[min(440px,calc(100vw-32px))] rounded-xl bg-surface-overlay p-5",
          "shadow-overlay ring-hairline",
          "transition-[opacity,scale] duration-200 ease-out",
          "data-starting-style:scale-[0.97] data-starting-style:opacity-0",
          "data-ending-style:scale-[0.97] data-ending-style:opacity-0",
          className,
        )}
        {...props}
      >
        {children}
        <DialogClose
          aria-label="Close dialog"
          className={cn(
            "absolute top-3.5 right-3.5 flex size-[26px] cursor-pointer items-center justify-center rounded-sm",
            "text-ink-muted transition-colors duration-100",
            "hover:bg-fill-hover hover:text-ink",
            focusRing,
          )}
        >
          <XIcon width={14} height={14} />
        </DialogClose>
      </BaseDialog.Popup>
    </BaseDialog.Portal>
  );
}

export function DialogTitle({
  className,
  children,
}: {
  className?: string;
  children: ReactNode;
}) {
  return (
    <BaseDialog.Title className={cn("text-title text-ink", className)}>
      {children}
    </BaseDialog.Title>
  );
}

export function DialogDescription({
  className,
  children,
}: {
  className?: string;
  children: ReactNode;
}) {
  return (
    <BaseDialog.Description
      className={cn("mt-1.5 text-body text-ink-muted", className)}
    >
      {children}
    </BaseDialog.Description>
  );
}

export function DialogBody({
  className,
  children,
}: {
  className?: string;
  children: ReactNode;
}) {
  /* The body is the only region that scrolls when a dialog outgrows the
   * viewport; the title and footer stay pinned. Padding keeps focus rings
   * inside the scroll area on every edge; margins preserve content spacing
   * and let the scrollbar sit at the popup edge. */
  return (
    <div
      className={cn("-mx-5 -mb-1 mt-3 min-h-0 overflow-y-auto px-5 py-1", className)}
    >
      {children}
    </div>
  );
}

export function DialogFooter({
  className,
  children,
}: {
  className?: string;
  children: ReactNode;
}) {
  return (
    <div className={cn("mt-5 flex shrink-0 justify-end gap-2.5", className)}>
      {children}
    </div>
  );
}
