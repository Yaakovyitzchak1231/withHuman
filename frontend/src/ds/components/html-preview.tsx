import { useMemo, useState } from "react";
import { previewHTML } from "@/ds/lib/preview-html";
import { cn } from "@/ds/lib/cn";
import { useTheme } from "@/ds/theme/theme-provider";
import { CodeBlock } from "./code-block";
import { CopyButton } from "./copy-button";
import { ToggleGroup, ToggleGroupItem } from "./toggle-group";

/*
 * HTML an agent wrote (an email body, a snippet), shown the way its
 * recipient would see it. The document renders in an iframe with every
 * sandbox restriction on and a policy that loads nothing: no script
 * runs, no image or font is fetched (so no tracking pixel fires from
 * the review page), no link navigates. The source is one click away,
 * because the rendered view is a courtesy and the markup is the fact.
 */
export interface HtmlPreviewProps {
  html: string;
  label?: string;
  raised?: boolean;
  className?: string;
}

type View = "preview" | "source";

/* The preview's height comes from a rough count of block elements: the
 * frame cannot report its own height without running script. The
 * wrapper resizes by hand for the long ones. */
function estimateHeight(html: string): number {
  const blocks = (html.match(/<(?:div|p|br|li|tr|h[1-6]|blockquote|pre|hr)\b/gi) ?? []).length;
  const chars = html.replace(/<[^>]*>/g, "").length;
  return Math.min(560, Math.max(120, 40 + blocks * 22 + Math.floor(chars / 90) * 22));
}

export function HtmlPreview({ html, label, raised = false, className }: HtmlPreviewProps) {
  const [view, setView] = useState<View>("preview");
  const { resolvedTheme } = useTheme();
  const document = useMemo(() => {
    /* The product's own type inside the frame, from the tokens the
     * theme resolves to at this moment; the frame is opaque to the
     * page's stylesheet. */
    const style = getComputedStyle(window.document.documentElement);
    const token = (name: string, fallback: string) => style.getPropertyValue(name).trim() || fallback;
    const css = `
      :root { color-scheme: ${resolvedTheme}; }
      html, body { margin: 0; padding: 0; }
      body {
        padding: 14px 16px;
        font: 14px/1.5 ${token("--font-sans", "system-ui, sans-serif")};
        color: ${token("--ds-ink", "#111")};
        background: ${token(raised ? "--ds-surface" : "--ds-canvas", "#fff")};
        overflow-wrap: anywhere;
      }
      a { color: ${token("--ds-accent", "#2450c8")}; pointer-events: none; }
      img { max-width: 100%; }
      pre, code { font-family: ${token("--font-mono", "monospace")}; font-size: 12px; }
      blockquote { margin: 0.6em 0; padding-left: 0.8em; border-left: 2px solid ${token("--ds-edge-strong", "#ccc")}; color: ${token("--ds-ink-muted", "#666")}; }
      table { border-collapse: collapse; }
      td, th { padding: 0.2em 0.5em; }
    `;
    return `<!doctype html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; form-action 'none'; base-uri 'none'"><style>${css}</style></head><body>${previewHTML(html)}</body></html>`;
  }, [html, raised, resolvedTheme]);
  const height = useMemo(() => estimateHeight(html), [html]);

  return (
    <div
      className={cn(
        "overflow-hidden border-hairline border-edge",
        raised ? "rounded-lg bg-surface shadow-sm" : "rounded-md bg-canvas",
        className,
      )}
    >
      <div className="flex h-8 items-center justify-between border-b-hairline border-edge pr-1.5 pl-3">
        <span className="font-mono text-[11px] text-ink-muted">{label ?? "html"}</span>
        <span className="flex items-center gap-1.5">
          <ToggleGroup aria-label="HTML view" value={view} onValueChange={(next) => setView(next as View)}>
            <ToggleGroupItem value="preview">Preview</ToggleGroupItem>
            <ToggleGroupItem value="source">Source</ToggleGroupItem>
          </ToggleGroup>
          <CopyButton value={html} aria-label="Copy HTML source" />
        </span>
      </div>
      {view === "preview" ? (
        <div className="resize-y overflow-hidden" style={{ height }}>
          <iframe
            title={label ? `${label} preview` : "HTML preview"}
            sandbox=""
            referrerPolicy="no-referrer"
            srcDoc={document}
            className="block h-full w-full border-0"
          />
        </div>
      ) : (
        <CodeBlock code={html} copyable={false} wrap className="rounded-none border-0" />
      )}
    </div>
  );
}
