import { Badge } from "@/ds/components/badge";
import { Button } from "@/ds/components/button";
import {
  DescriptionItem,
  DescriptionList,
} from "@/ds/components/description-list";
import { ProgressIcon } from "@/ds/icons";
import {
  Card,
  CardBody,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/ds/components/card";
import { Section, Demo } from "../section";

export function CardsSection() {
  return (
    <Section
      id="cards"
      title="Card"
      lede="The standard raised panel. Header, body, and footer are optional; hairlines separate whichever are present."
    >
      <Demo label="Anatomy" bare>
        <Card className="max-w-[440px]">
          <CardHeader>
            <div>
              <CardTitle>Refund request</CardTitle>
              <CardDescription>
                billing-agent-01 wants to refund $420 to Acme Corp.
              </CardDescription>
            </div>
            <Badge tone="warn" icon={<ProgressIcon width={14} height={14} />}>
              Pending
            </Badge>
          </CardHeader>
          <CardBody>
            <DescriptionList>
              <DescriptionItem term="Tool" mono>
                stripe.refunds.create
              </DescriptionItem>
              <DescriptionItem term="Amount">$420.00</DescriptionItem>
              <DescriptionItem term="Requested">4 minutes ago</DescriptionItem>
            </DescriptionList>
          </CardBody>
          <CardFooter>
            <Button variant="ghost">Dismiss</Button>
            <Button variant="danger">Deny</Button>
            <Button variant="approve">Approve</Button>
          </CardFooter>
        </Card>
      </Demo>
    </Section>
  );
}
