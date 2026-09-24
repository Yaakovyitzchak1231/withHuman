import type { ReactNode } from "react";
import { cn } from "@/ds/lib/cn";
import { InfoCard, type InfoCardProps } from "./info-card";

type InfoLabelProps = {
  info: ReactNode;
  docsLinks?: InfoCardProps["docsLinks"];
  className?: string;
} & ({ children: string; title?: string } | { children: ReactNode; title: string });

/** A field label or card title with contextual help and documentation. */
export function InfoLabel({ info, docsLinks, className, children, title }: InfoLabelProps) {
  const cardTitle = title ?? (typeof children === "string" ? children : "More information");
  return (
    <span className={cn("inline-flex items-center gap-1", className)}>
      {children}
      <InfoCard title={cardTitle} docsLinks={docsLinks}>{info}</InfoCard>
    </span>
  );
}
