import { useMemo, type ReactNode } from "react";
import { Toast as BaseToast } from "@base-ui/react/toast";
import { cn } from "@/ds/lib/cn";
import { focusRing } from "@/ds/lib/focus";
import { XIcon } from "@/ds/icons";
import { ToneIcon, type Tone } from "./badge";

/* Imperative API: const toast = useToast(); toast.add({ title, ... }).
 * Pass type: "approve" | "deny" | "warn" | "accent" for a leading status
 * icon.
 *
 * The returned object keeps one identity for the life of the provider.
 * Base UI's manager is rebuilt whenever the toast list changes, so a
 * load effect that depends on it and toasts its own failure would run
 * again after every toast, and again after every auto-dismiss: with the
 * backend down that is an endless stream of error toasts. Screens can
 * put this in dependency arrays freely. */
export function useToast(): ToastManager {
  const { add, close, update, promise } = BaseToast.useToastManager();
  return useMemo(() => ({ add, close, update, promise }), [add, close, update, promise]);
}

export type ToastManager = Omit<ReturnType<typeof BaseToast.useToastManager>, "toasts">;

const TONES: Tone[] = ["neutral", "accent", "approve", "deny", "warn"];

function toneOf(type: string | undefined): Tone | null {
  return type && (TONES as string[]).includes(type) ? (type as Tone) : null;
}

function ToastList() {
  const { toasts } = BaseToast.useToastManager();
  return toasts.map((toast) => {
    const tone = toneOf(toast.type);
    return (
      <BaseToast.Root
        key={toast.id}
        toast={toast}
        className={cn(
          /* Stacking arithmetic from the Base UI reference implementation:
           * newest toast in front, older ones peek out behind, hover
           * expands the stack. */
          "[--gap:10px] [--peek:10px]",
          "[--scale:calc(max(0,1-(var(--toast-index)*0.08)))]",
          "[--shrink:calc(1-var(--scale))]",
          "[--height:var(--toast-frontmost-height,var(--toast-height))]",
          "[--offset-y:calc(var(--toast-offset-y)*-1+calc(var(--toast-index)*var(--gap)*-1)+var(--toast-swipe-movement-y))]",
          "absolute right-0 bottom-0 w-full origin-bottom select-none",
          "z-[calc(1000-var(--toast-index))]",
          "h-(--height) data-expanded:h-(--toast-height)",
          "[transform:translateX(var(--toast-swipe-movement-x))_translateY(calc(var(--toast-swipe-movement-y)-(var(--toast-index)*var(--peek))-(var(--shrink)*var(--height))))_scale(var(--scale))]",
          "data-expanded:[transform:translateX(var(--toast-swipe-movement-x))_translateY(var(--offset-y))]",
          "data-starting-style:[transform:translateY(150%)]",
          "[&[data-ending-style]:not([data-limited]):not([data-swipe-direction])]:[transform:translateY(150%)]",
          "data-ending-style:data-[swipe-direction=down]:[transform:translateY(calc(var(--toast-swipe-movement-y)+150%))]",
          "data-ending-style:data-[swipe-direction=right]:[transform:translateX(calc(var(--toast-swipe-movement-x)+150%))_translateY(var(--offset-y))]",
          "data-ending-style:opacity-0 data-limited:opacity-0",
          "rounded-lg bg-surface-raised shadow-overlay ring-hairline",
          "[transition:transform_400ms_var(--ds-ease-out),opacity_160ms,height_160ms_var(--ds-ease-standard)]",
          /* Invisible bridge so the pointer can travel between stacked
           * toasts without collapsing the expanded state. */
          "after:absolute after:top-full after:left-0 after:h-[calc(var(--gap)+1px)] after:w-full after:content-['']",
        )}
      >
        <BaseToast.Content
          className={cn(
            "flex items-start gap-2.5 overflow-hidden p-3.5",
            "transition-opacity duration-160 ease-standard",
            "data-behind:opacity-0 data-expanded:opacity-100",
          )}
        >
          {/* Internal anatomy: the toned status icon sits beside the
           * toast's own title, so it never stands without a label. */}
          {tone ? <ToneIcon tone={tone} className="mt-[3px] shrink-0" /> : null}
          <div
            data-base-ui-swipe-ignore
            className="flex min-w-0 flex-1 flex-col gap-0.5 select-text"
          >
            <BaseToast.Title className="text-body font-medium text-ink" />
            <BaseToast.Description className="text-body text-ink-muted" />
          </div>
          <BaseToast.Close
            aria-label="Dismiss notification"
            className={cn(
              "flex size-6 shrink-0 cursor-pointer items-center justify-center rounded-sm",
              "text-ink-muted transition-colors duration-100",
              "hover:bg-fill-hover hover:text-ink",
              focusRing,
            )}
          >
            <XIcon width={13} height={13} />
          </BaseToast.Close>
        </BaseToast.Content>
      </BaseToast.Root>
    );
  });
}

/* Mount once near the app root; toasts stack in the bottom right. */
export function ToastProvider({ children }: { children: ReactNode }) {
  return (
    <BaseToast.Provider>
      {children}
      <BaseToast.Portal>
        <BaseToast.Viewport
          className={cn(
            "ds-root fixed right-6 bottom-6 z-40 w-[min(360px,calc(100vw-32px))]",
            "bg-transparent",
          )}
        >
          <ToastList />
        </BaseToast.Viewport>
      </BaseToast.Portal>
    </BaseToast.Provider>
  );
}
