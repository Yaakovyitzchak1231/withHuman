// A Mastra support agent and its billing sub-agent, whose tool calls wait for withHuman.
//
//   npm run mastra
//   npm run mastra -- --amount 2500

import { parseArgs } from "node:util";
// Mastra takes AI SDK 5 and 6 models, so Azure uses @ai-sdk/azure 3 under an alias.
import { createAzure } from "@ai-sdk/azure-v3";
import { Client, Gate } from "@agentapprovalprotocol/sdk";
import { Approvals } from "@agentapprovalprotocol/mastra";
import { Agent } from "@mastra/core/agent";
import { Mastra } from "@mastra/core/mastra";
import { createTool } from "@mastra/core/tools";
import { z } from "zod";

const { values: args } = parseArgs({ options: { amount: { type: "string", default: "40" } } });
const env = (name: string) => process.env[name] ?? "";

function chatModel() {
  if (env("AZURE_OPENAI_ENDPOINT")) {
    const azure = createAzure({
      baseURL: env("AZURE_OPENAI_ENDPOINT").replace(/\/?$/, "/openai"),
      apiKey: env("AZURE_OPENAI_API_KEY"),
      apiVersion: env("OPENAI_API_VERSION"),
      useDeploymentBasedUrls: true,
    });
    return azure.chat(env("AZURE_OPENAI_DEPLOYMENT"));
  }
  return `openai/${env("OPENAI_MODEL")}`;
}

const lookupOrder = createTool({
  id: "lookup_order",
  description: "Look up an order's charges.",
  inputSchema: z.object({ order_id: z.string() }),
  execute: async ({ order_id }) =>
    `Order ${order_id}: two charges of $${Number(args.amount).toFixed(2)} on the same card, 2 seconds apart.`,
});

const issueRefund = createTool({
  id: "issue_refund",
  description: "Refund part of a customer's order.",
  inputSchema: z.object({ order_id: z.string(), amount: z.number(), reason: z.string() }),
  execute: async ({ order_id, amount }) => {
    console.log(`\n>>> Refunding ${amount} on order ${order_id}`);
    return `Refunded ${amount} on order ${order_id}.`;
  },
});

const model = chatModel();
const approvals = new Approvals(new Gate(new Client(env("AAP_URL"), env("AAP_INSTANCE_TOKEN"))));

// Build every agent with approvals.config, sub-agents included.
const billing = new Agent(
  approvals.config({
    id: "billing",
    name: "Billing specialist",
    description: "Issues refunds. Give it the order ID, the amount and the reason.",
    instructions: "You issue refunds. Call issue_refund once with the details you were given, then report what happened.",
    model,
    tools: { issue_refund: issueRefund },
  }),
);
const support = new Agent(
  approvals.config({
    id: "support",
    name: "Support",
    instructions: "You are a support agent. Look the order up, then ask the billing agent to refund any duplicate charge.",
    model,
    tools: { lookup_order: lookupOrder },
    agents: { billing },
  }),
);

const agent = new Mastra({ agents: { support, billing }, logger: false }).getAgent("support");
// Throws if an agent or sub-agent is missing the approval hooks.
await approvals.check(agent);

console.log("Waiting for decisions in withHuman ...");
const result = await agent.generate("Order 1234 was charged twice. Please sort it out.", { maxSteps: 8 });
console.log(`\nAgent: ${result.text}`);
