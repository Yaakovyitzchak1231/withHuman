import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useRef,
  useState,
  type ComponentProps,
  type KeyboardEvent,
  type RefCallback,
} from "react";
import { Combobox } from "@base-ui/react/combobox";

/*
 * Combobox popups with the input inside them swallow Escape while that
 * input has focus, which is exactly where focus lands on open. This
 * wrapper controls the open state so the popup can close itself on a
 * captured Escape and hand focus back to the trigger.
 */
interface ClosableContextValue {
  close: () => void;
  registerTrigger: RefCallback<HTMLElement>;
}

const ClosableContext = createContext<ClosableContextValue | null>(null);

export function ClosableComboboxRoot({
  onOpenChange,
  ...props
}: ComponentProps<typeof Combobox.Root>) {
  const [open, setOpen] = useState(false);
  const triggerRef = useRef<HTMLElement | null>(null);

  const context = useMemo<ClosableContextValue>(
    () => ({
      close: () => {
        setOpen(false);
        triggerRef.current?.focus();
      },
      registerTrigger: (element) => {
        triggerRef.current = element;
      },
    }),
    [],
  );

  return (
    <ClosableContext value={context}>
      <Combobox.Root
        open={open}
        onOpenChange={(next, details) => {
          setOpen(next);
          onOpenChange?.(next, details);
        }}
        {...props}
      />
    </ClosableContext>
  );
}

/* Capture handler for the popup: Escape closes and returns focus. */
export function useEscapeClose() {
  const context = useContext(ClosableContext);
  return useCallback(
    (event: KeyboardEvent) => {
      if (event.key !== "Escape" || !context) return;
      // A nested popover handles its own Escape before the surrounding menu.
      const target = event.target as HTMLElement;
      if (!event.currentTarget.contains(target)) return;
      if (target.closest('[role="dialog"]') !== event.currentTarget) return;
      if (target.closest('[aria-haspopup="dialog"][aria-expanded="true"]')) return;
      event.preventDefault();
      event.stopPropagation();
      context.close();
    },
    [context],
  );
}

export function useRegisterTrigger(): RefCallback<HTMLElement> {
  const context = useContext(ClosableContext);
  return context?.registerTrigger ?? (() => {});
}
