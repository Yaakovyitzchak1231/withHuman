# /// script
# requires-python = ">=3.11,<3.14"
# dependencies = [
#   "agentapprovalprotocol[pydantic-ai]",
#   "pydantic-ai-slim[openai]",
# ]
# ///
"""A Pydantic AI support agent and its billing agent, whose tool calls wait for withHuman.

    uv run --env-file ../.env pydanticai_refund.py
    uv run --env-file ../.env pydanticai_refund.py --amount 2500
"""

import argparse
import asyncio
import os

os.environ.setdefault("PYDANTIC_AI_NO_BANNER", "1")

from pydantic_ai import Agent, RunContext
from pydantic_ai.models.openai import OpenAIChatModel
from pydantic_ai.providers.openai import OpenAIProvider

from agentapprovalprotocol.pydantic_ai import ApprovalCapability

parser = argparse.ArgumentParser()
parser.add_argument("--amount", type=float, default=40.0, help="the duplicate charge the order lookup finds")
args = parser.parse_args()


def lookup_order(order_id: str) -> str:
    """Look up an order's charges."""
    return f"Order {order_id}: two charges of ${args.amount:,.2f} on the same card, 2 seconds apart."


def issue_refund(order_id: str, amount: float, reason: str) -> str:
    """Refund part of a customer's order."""
    print(f"\n>>> Refunding {amount} on order {order_id}")
    return f"Refunded {amount} on order {order_id}."


model = OpenAIChatModel(
    os.environ["OPENAI_MODEL"],
    provider=OpenAIProvider(base_url=os.environ.get("OPENAI_BASE_URL") or None, api_key=os.environ["OPENAI_API_KEY"]),
)
approvals = ApprovalCapability(os.environ["AAP_URL"], os.environ["AAP_INSTANCE_TOKEN"])

# Every agent that calls tools needs the capability, including agents run from a tool.
billing = Agent(
    model,
    instructions="You issue refunds. Call issue_refund once with the details you were given, then report what happened.",
    tools=[issue_refund],
    capabilities=[approvals],
)


async def ask_billing_specialist(ctx: RunContext[None], request: str) -> str:
    """Ask the billing specialist to issue a refund. Give it the order ID, the amount and the reason."""
    result = await billing.run(request, usage=ctx.usage)
    return result.output


support = Agent(
    model,
    instructions="You are a support agent. Look the order up, then ask the billing specialist to refund any duplicate charge.",
    tools=[lookup_order, ask_billing_specialist],
    capabilities=[approvals],
)


async def main() -> None:
    print("Waiting for decisions in withHuman ...")
    result = await support.run("Order 1234 was charged twice. Please sort it out.")
    print(f"\nAgent: {result.output}")


asyncio.run(main())
