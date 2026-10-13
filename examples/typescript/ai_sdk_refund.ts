// A Vercel AI SDK support agent and its billing agent, whose tool calls wait for withHuman.
//
//   npm run ai-sdk
//   npm run ai-sdk -- --amount 2500

import { parseArgs } from "node:util";
import { createAzure } from "@ai-sdk/azure";
import { openai } from "@ai-sdk/openai";
import { Client, Gate } from "@agentapprovalprotocol/sdk";
import { approvals, gatedTools } from "@agentapprovalprotocol/ai-sdk";
import { ToolLoopAgent, stepCountIs, tool } from "ai";
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
  return openai(env("OPENAI_MODEL"));
}

const lookupOrder = tool({
  description: "Look up an order's charges.",
  inputSchema: z.object({ order_id: z.string() }),
  execute: async ({ order_id }) =>
    `Order ${order_id}: two charges of $${Number(args.amount).toFixed(2)} on the same card, 2 seconds apart.`,
});

const issueRefund = tool({
  description: "Refund part of a customer's order.",
  inputSchema: z.object({ order_id: z.string(), amount: z.number(), reason: z.string() }),
  execute: async ({ order_id, amount }) => {
    console.log(`\n>>> Refunding ${amount} on order ${order_id}`);
    return `Refunded ${amount} on order ${order_id}.`;
  },
});

const model = chatModel();
const gate = new Gate(new Client(env("AAP_URL"), env("AAP_INSTANCE_TOKEN")));

// approvals() covers only the agent it is spread into, so the billing agent's
// tools are wrapped with gatedTools.
const billing = new ToolLoopAgent({
  model,
  instructions: "You issue refunds. Call issue_refund once with the details you were given, then report what happened.",
  tools: gatedTools({ issue_refund: issueRefund }, { gate }),
  stopWhen: stepCountIs(5),
});

const billingSpecialist = tool({
  description: "Ask the billing specialist to issue a refund. Give it the order ID, the amount and the reason.",
  inputSchema: z.object({ request: z.string() }),
  execute: async ({ request }) => (await billing.generate({ prompt: request })).text,
});

const support = new ToolLoopAgent({
  model,
  instructions: "You are a support agent. Look the order up, then ask billing_specialist to refund any duplicate charge.",
  tools: { lookup_order: lookupOrder, billing_specialist: billingSpecialist },
  stopWhen: stepCountIs(8),
  ...approvals({ gate }),
});

console.log("Waiting for decisions in withHuman ...");
const result = await support.generate({ prompt: "Order 1234 was charged twice. Please sort it out." });
console.log(`\nAgent: ${result.text}`);
