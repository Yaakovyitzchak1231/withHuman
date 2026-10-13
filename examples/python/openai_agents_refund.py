# /// script
# requires-python = ">=3.10,<3.14"
# dependencies = [
#   "agentapprovalprotocol[openai-agents]",
# ]
# ///
"""An OpenAI Agents SDK support agent and its billing agent, whose tool calls wait for withHuman.

    uv run --env-file ../.env openai_agents_refund.py
    uv run --env-file ../.env openai_agents_refund.py --amount 2500
"""

import argparse
import asyncio
import os

from agents import Agent, OpenAIChatCompletionsModel, Runner, function_tool, set_tracing_disabled
from openai import AsyncAzureOpenAI

from agentapprovalprotocol.openai_agents import Approvals

parser = argparse.ArgumentParser()
parser.add_argument("--amount", type=float, default=40.0, help="the duplicate charge the order lookup finds")
args = parser.parse_args()


@function_tool
def lookup_order(order_id: str) -> str:
    """Look up an order's charges."""
    return f"Order {order_id}: two charges of ${args.amount:,.2f} on the same card, 2 seconds apart."


@function_tool
def issue_refund(order_id: str, amount: float, reason: str) -> str:
    """Refund part of a customer's order."""
    print(f"\n>>> Refunding {amount} on order {order_id}")
    return f"Refunded {amount} on order {order_id}."


def chat_model():
    if os.environ.get("AZURE_OPENAI_ENDPOINT"):
        set_tracing_disabled(True)  # traces go to OpenAI, which an Azure setup has no key for
        return OpenAIChatCompletionsModel(
            model=os.environ["AZURE_OPENAI_DEPLOYMENT"],
            openai_client=AsyncAzureOpenAI(
                azure_endpoint=os.environ["AZURE_OPENAI_ENDPOINT"],
                api_version=os.environ["OPENAI_API_VERSION"],
                api_key=os.environ["AZURE_OPENAI_API_KEY"],
            ),
        )
    return os.environ["OPENAI_MODEL"]


model = chat_model()
billing = Agent(
    name="billing_specialist",
    model=model,
    instructions="You issue refunds. Call issue_refund once with the order ID, amount and reason, then report what happened.",
    tools=[issue_refund],
)
support = Agent(
    name="support",
    model=model,
    instructions="You are a support agent. Look the order up, then ask billing_specialist to refund any duplicate charge.",
    tools=[
        lookup_order,
        billing.as_tool(
            tool_name="billing_specialist",
            tool_description="Issues refunds. Give it the order ID, the amount and the reason.",
        ),
    ],
)

# Covers the agent's tools, agents it uses as tools and its handoff targets.
# Add no tools to these agents after this.
approvals = Approvals(os.environ["AAP_URL"], os.environ["AAP_INSTANCE_TOKEN"])
support = approvals.protect(support)


async def main() -> None:
    print("Waiting for decisions in withHuman ...")
    result = await Runner.run(support, "Order 1234 was charged twice. Please sort it out.")
    print(f"\nAgent: {result.final_output}")


asyncio.run(main())
