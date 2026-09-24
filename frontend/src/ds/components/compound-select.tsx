import { useEffect, useRef, type ComponentProps, type ReactNode } from "react";
import { Combobox } from "@base-ui/react/combobox";
import { cn } from "@/ds/lib/cn";
import { useRegisterTrigger } from "@/ds/lib/closable-combobox";
import { ChevronUpDownIcon } from "@/ds/icons";
import { Select, SelectContent, SelectItem } from "./select";

/*
 * A Select whose chosen value unlocks a second control in the same box.
 * With nothing chosen the Select fills the box; choosing a value shrinks
 * it to a prefix segment on the left, a hairline divider appears, and
 * the tail (whatever the children render: an id input, another Select)
 * takes the rest of the row and receives focus. Clearing the value
 * removes the tail with it: the tail only means something against the
 * prefix.
 *
 * For a filter pass emptyLabel ("Any subject"): nothing chosen means
 * everything, so an "any" option is added and the label reads in ink as
 * a value. Otherwise pass placeholder and the empty state is a prompt.
 */
export interface CompoundSelectProps<Item> {
  items: readonly Item[];
  /* Identifies an item; defaults to String(item), enough for string
   * vocabularies. Required for object items. */
  itemKey?: (item: Item) => string;
  itemLabel?: (item: Item) => string;
  /* How an item draws in the list and, once chosen, in the prefix. */
  renderItem?: (item: Item) => ReactNode;
  emptyLabel?: string;
  placeholder?: string;
  value: Item | null;
  onValueChange: (value: Item | null) => void;
  /* The control unlocked by a value. */
  children?: ReactNode;
  "aria-label": string;
  disabled?: boolean;
  className?: string;
}

interface Option<Item> {
  key: string;
  item: Item | null;
}

/* A key no item can collide with: keys come from itemKey over real
 * items, and this one starts with a space. */
const ANY_KEY = " any";

export function CompoundSelect<Item>({
  items,
  itemKey = (item) => String(item),
  itemLabel = (item) => String(item),
  renderItem = itemLabel,
  emptyLabel,
  placeholder,
  value,
  onValueChange,
  children,
  "aria-label": ariaLabel,
  disabled = false,
  className,
}: CompoundSelectProps<Item>) {
  const tailRef = useRef<HTMLDivElement>(null);
  const hadValue = useRef(value !== null);
  const chosen = value !== null;

  /* Choosing a value is the first half of a two-part answer, so the
   * tail takes focus as soon as it appears. */
  useEffect(() => {
    if (chosen && !hadValue.current) {
      requestAnimationFrame(() => {
        tailRef.current
          ?.querySelector<HTMLElement>("input, button, [tabindex]")
          ?.focus();
      });
    }
    hadValue.current = chosen;
  }, [chosen]);

  const options: Option<Item>[] = [
    ...(emptyLabel ? [{ key: ANY_KEY, item: null }] : []),
    ...items.map((item) => ({ key: itemKey(item), item })),
  ];
  const current =
    value === null
      ? (options.find((option) => option.item === null) ?? null)
      : (options.find((option) => option.key === itemKey(value)) ?? null);
  const labelOf = (option: Option<Item>) =>
    option.item === null ? (emptyLabel ?? "") : itemLabel(option.item);

  return (
    <div
      className={cn(
        "flex h-[30px] items-stretch overflow-hidden",
        "rounded-sm border-hairline border-edge bg-control shadow-sm",
        "transition-[border-color,box-shadow] duration-100 ease-standard",
        "hover:border-edge-strong",
        /* The box carries the focus treatment for every segment, so the
         * pair reads as one control whichever part has focus. */
        "has-[input:focus]:border-accent has-[input:focus]:shadow-[0_0_0_2.5px_var(--ds-accent-subtle)]",
        "has-[button:focus-visible]:border-accent has-[button:focus-visible]:shadow-[0_0_0_2.5px_var(--ds-accent-subtle)]",
        "has-[input:focus]:hover:border-accent",
        disabled && "pointer-events-none opacity-50",
        className,
      )}
    >
      <Select
        items={options}
        itemToStringLabel={(option) => labelOf(option as Option<Item>)}
        value={current}
        disabled={disabled}
        onValueChange={(next) => onValueChange((next as Option<Item> | null)?.item ?? null)}
      >
        <CompoundSelectTrigger aria-label={ariaLabel} segment={chosen ? "prefix" : "whole"}>
          {chosen ? (
            renderItem(value)
          ) : emptyLabel ? (
            emptyLabel
          ) : (
            <span className="text-ink-faint">{placeholder}</span>
          )}
        </CompoundSelectTrigger>
        <SelectContent<Option<Item>>>
          {(option) => (
            <SelectItem key={option.key} value={option}>
              {option.item === null ? emptyLabel : renderItem(option.item)}
            </SelectItem>
          )}
        </SelectContent>
      </Select>
      {chosen ? (
        <div ref={tailRef} className="flex min-w-0 flex-1 items-stretch">
          {children}
        </div>
      ) : null}
    </div>
  );
}

/* A borderless Select trigger for use inside the box: the prefix
 * segment the CompoundSelect itself renders, or the trigger of a
 * Select placed in the tail. */
export function CompoundSelectTrigger({
  segment,
  children,
  className,
  ...props
}: Omit<ComponentProps<typeof Combobox.Trigger>, "children"> & {
  /* prefix: content-sized and tinted, capped so a long value leaves
   * room for the tail. tail and whole: fill what is left. */
  segment: "prefix" | "tail" | "whole";
  children: ReactNode;
}) {
  const registerTrigger = useRegisterTrigger();
  return (
    <Combobox.Trigger
      ref={registerTrigger}
      className={cn(
        "flex h-full cursor-pointer items-center gap-1.5 pr-1.5 pl-2.5",
        "text-body text-ink outline-none select-none",
        "transition-colors duration-100",
        segment === "prefix"
          ? [
              "max-w-[60%] shrink-0 border-r-hairline border-edge bg-fill-hover/60",
              "text-ink-secondary hover:bg-fill-hover",
            ]
          : "min-w-0 flex-1 justify-between",
        className,
      )}
      {...props}
    >
      <span className="truncate">{children}</span>
      <Combobox.Icon className="flex shrink-0 text-ink-muted">
        <ChevronUpDownIcon width={14} height={14} />
      </Combobox.Icon>
    </Combobox.Trigger>
  );
}

/* A borderless text input for the tail; mono because a value that
 * only makes sense against a chosen kind is usually an id. */
export function CompoundSelectInput({
  mono = true,
  className,
  ...props
}: ComponentProps<"input"> & { mono?: boolean }) {
  return (
    <input
      spellCheck={false}
      autoComplete="off"
      data-1p-ignore=""
      data-lpignore="true"
      data-bwignore=""
      className={cn(
        "h-full min-w-0 flex-1 bg-transparent px-2.5 outline-none",
        "text-body text-ink placeholder:text-ink-faint",
        mono && "font-mono text-[12px]",
        className,
      )}
      {...props}
    />
  );
}
