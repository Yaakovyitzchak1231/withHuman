import { Badge } from "@/ds/components/badge";
import { Tab, TabPanel, Tabs, TabsList } from "@/ds/components/tabs";
import { Section, Demo, KeyboardHint } from "../section";

export function TabsSection() {
  return (
    <Section
      id="tabs"
      title="Tabs"
      lede="Quiet labels on a hairline baseline; one ink indicator slides between them for pointer and keyboard alike."
    >
      <Demo label="Request detail views">
        <Tabs defaultValue="payload">
          <TabsList>
            <Tab value="payload">Payload</Tab>
            <Tab value="history">
              History
              <Badge tone="neutral" className="h-[18px] px-1.5">
                4
              </Badge>
            </Tab>
            <Tab value="policy">Policy</Tab>
            <Tab value="raw" disabled>
              Raw
            </Tab>
          </TabsList>
          <TabPanel value="payload">
            <pre className="overflow-x-auto rounded-md bg-canvas p-3 font-mono text-[12px] leading-[19px] text-ink-secondary ring-hairline">
{`{
  "charge": "ch_3Nq2XT2eZvKYlo2C",
  "amount": 42000
}`}
            </pre>
          </TabPanel>
          <TabPanel value="history">
            <p className="text-body text-ink-muted">
              4 earlier requests from this agent, all approved.
            </p>
          </TabPanel>
          <TabPanel value="policy">
            <p className="text-body text-ink-muted">
              Refunds above $250 require human approval.
            </p>
          </TabPanel>
        </Tabs>
        <KeyboardHint>
          Left and Right arrows move between tabs; Home and End jump to the
          edges.
        </KeyboardHint>
      </Demo>
    </Section>
  );
}
