import { useState } from "react";
import { Button } from "@/ds/components/button";
import { Logo } from "@/ds/components/logo";
import { LogoSplash, type LogoSplashDirection } from "@/ds/components/logo-splash";
import { Section, Demo, DemoRow } from "../section";

export function LogoSection() {
  const [collapsed, setCollapsed] = useState(false);
  const [splash, setSplash] = useState<LogoSplashDirection | null>(null);
  return (
    <Section
      id="logo"
      title="Logo"
      lede="The brand written as a tool call, withHuman(^), in currentColor. The lockup collapses into the W mark in two beats: the name folds away under the (^), then the (^) morphs into the W."
    >
      <Demo label="Lockup" hint="Size via font-size">
        <DemoRow className="gap-8">
          <Logo className="text-[13px] text-ink" />
          <Logo className="text-[16px] text-ink" />
          <Logo className="text-[28px] text-ink" />
        </DemoRow>
      </Demo>

      <Demo label="Collapse" hint="collapsed prop, animated both ways">
        <DemoRow className="gap-6">
          <Logo collapsed={collapsed} className="text-[28px] text-ink" />
          <Button variant="neutral" size="sm" onClick={() => setCollapsed((c) => !c)}>
            {collapsed ? "Expand" : "Collapse"}
          </Button>
        </DemoRow>
      </Demo>

      <Demo label="LogoSplash" hint="Expand plays after sign-in, collapse on sign-out">
        <DemoRow>
          <Button variant="neutral" size="sm" onClick={() => setSplash("expand")}>
            Play sign-in splash
          </Button>
          <Button variant="neutral" size="sm" onClick={() => setSplash("collapse")}>
            Play sign-out splash
          </Button>
        </DemoRow>
        {splash ? (
          <LogoSplash direction={splash} onDone={() => setSplash(null)} />
        ) : null}
      </Demo>
    </Section>
  );
}
