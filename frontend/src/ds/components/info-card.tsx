import {
  useId,
  useRef,
  useState,
  type ComponentProps,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import { Popover } from "@base-ui/react/popover";
import { ArrowUpRightIcon, InfoIcon } from "@/ds/icons";
import { cn } from "@/ds/lib/cn";
import { focusRing } from "@/ds/lib/focus";
import { HoverCardBody, HoverCardSurface, HoverCardTitle } from "./hover-card";

export interface InfoCardProps {
  title: string;
  children: ReactNode;
  docsLinks?: readonly { href: string; label: string }[];
  /** Keep nested help inside its surrounding popup's focus and dismissal boundary. */
  portalContainer?: ComponentProps<typeof Popover.Portal>["container"];
}

/** Contextual help with optional links, opened from a small info button. */
export function InfoCard({ title, children, docsLinks = [], portalContainer }: InfoCardProps) {
  const triggerId = useId();
  const [handle] = useState(() => Popover.createHandle());
  const triggerRef = useRef<HTMLButtonElement>(null);
  const popupRef = useRef<HTMLDivElement>(null);
  const pinned = useRef(false);
  const keyboardFocus = useRef(false);
  const returningFocus = useRef(false);

  function focusTrigger() {
    returningFocus.current = true;
    triggerRef.current?.focus({ preventScroll: true });
    returningFocus.current = false;
  }

  function dismissOnEscape(event: KeyboardEvent) {
    keyboardFocus.current = true;
    if (event.key !== "Escape" || triggerRef.current?.getAttribute("aria-expanded") !== "true") return;
    event.preventDefault();
    event.stopPropagation();
    focusTrigger();
    handle.close();
  }

  return (
    <Popover.Root
      handle={handle}
      onOpenChange={(open, details) => {
        // Pointer departure must not interrupt reading or keyboard navigation.
        if (
          !open &&
          details.reason === "trigger-hover" &&
          (pinned.current ||
            (keyboardFocus.current &&
              (triggerRef.current === document.activeElement ||
                popupRef.current?.contains(document.activeElement))))
        ) {
          details.cancel();
          return;
        }
        if (!open) {
          pinned.current = false;
          keyboardFocus.current = false;
        }
      }}
    >
      <Popover.Trigger
        ref={triggerRef}
        id={triggerId}
        handle={handle}
        openOnHover
        delay={300}
        closeDelay={100}
        aria-label={`About ${title}`}
        onKeyDown={dismissOnEscape}
        onPointerDown={() => { keyboardFocus.current = false; }}
        onFocus={(event) => {
          if (!returningFocus.current && event.currentTarget.matches(":focus-visible")) {
            keyboardFocus.current = true;
            handle.open(triggerId);
          }
        }}
        onClick={(event) => {
          // Always pin on the first press, including after a long hover.
          event.preventBaseUIHandler();
          if (pinned.current) {
            handle.close();
          } else {
            pinned.current = true;
            handle.open(triggerId);
          }
        }}
        className={cn(
          "inline-flex size-5 shrink-0 cursor-pointer items-center justify-center rounded-xs text-ink-faint",
          "transition-colors duration-100 hover:bg-fill-hover hover:text-ink data-popup-open:bg-fill-hover data-popup-open:text-ink",
          focusRing,
        )}
      >
        <InfoIcon width={12} height={12} aria-hidden="true" />
      </Popover.Trigger>
      <Popover.Portal container={portalContainer}>
        <Popover.Positioner side="bottom" align="start" sideOffset={6} collisionPadding={12}>
          <Popover.Popup
            render={<HoverCardSurface />}
            ref={popupRef}
            onKeyDown={dismissOnEscape}
            onPointerDown={() => { keyboardFocus.current = false; }}
            onFocus={(event) => {
              keyboardFocus.current = event.target.matches(":focus-visible");
            }}
            initialFocus={false}
            // Escape restores focus explicitly. Hover dismissal must not focus the icon.
            finalFocus={false}
          >
            <Popover.Title render={<HoverCardTitle />}>{title}</Popover.Title>
            <HoverCardBody className="space-y-2 [overflow-wrap:anywhere]">
              {children}
            </HoverCardBody>
            {docsLinks.length > 0 ? (
              <HoverCardBody>
                <ul className="flex list-none flex-col items-start gap-1.5">
                  {docsLinks.map(({ href, label }) => (
                    <li key={href}>
                      <InfoCardLink href={href}>{label}</InfoCardLink>
                    </li>
                  ))}
                </ul>
              </HoverCardBody>
            ) : null}
          </Popover.Popup>
        </Popover.Positioner>
      </Popover.Portal>
    </Popover.Root>
  );
}

export interface InfoCardLinkProps {
  href: string;
  children: ReactNode;
}

/** The same documentation link treatment works in prose and in the footer. */
export function InfoCardLink({ href, children }: InfoCardLinkProps) {
  return (
    <a
      href={href}
      target="_blank"
      rel="noopener noreferrer"
      className={cn(
        "rounded-xs text-ink underline-offset-[3px] hover:underline",
        focusRing,
      )}
    >
      {children}
      <span className="whitespace-nowrap">
        {"\u00a0"}
        <ArrowUpRightIcon width={12} height={12} aria-hidden="true" className="inline-block align-[-2px] text-ink-faint" />
      </span>
      <span className="sr-only"> (opens in a new tab)</span>
    </a>
  );
}
