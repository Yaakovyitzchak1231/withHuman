# /// script
# requires-python = ">=3.10,<3.14"
# dependencies = [
#   "agentapprovalprotocol[adk]",
#   "litellm",
# ]
# ///
"""A Google ADK support agent and its billing agent, whose tool calls wait for withHuman.

    uv run --env-file ../.env adk_refund.py
    uv run --env-file ../.env adk_refund.py --amount 2500
"""

import argparse
import asyncio
import os
import warnings

warnings.filterwarnings("ignore", message=r"\[EXPERIMENTAL\]")

from google.adk.agents import LlmAgent
from google.adk.apps import App
from google.adk.models.lite_llm import LiteLlm
from google.adk.runners import InMemoryRunner
from google.adk.tools.agent_tool import AgentTool
from google.genai import types

from agentapprovalprotocol.adk import ApprovalPlugin, check_coverage

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


model = LiteLlm(
    model=f"openai/{os.environ['OPENAI_MODEL']}",
    api_base=os.environ.get("OPENAI_BASE_URL") or None,
    api_key=os.environ["OPENAI_API_KEY"],
)
billing = LlmAgent(
    name="billing_specialist",
    model=model,
    description="Issues refunds. Give it the order ID, the amount and the reason.",
    instruction="You issue refunds. Call issue_refund once with the details you were given, then report what happened.",
    tools=[issue_refund],
)
support = LlmAgent(
    name="support",
    model=model,
    instruction="You are a support agent. Look the order up, then ask billing_specialist to refund any duplicate charge.",
    tools=[lookup_order, AgentTool(billing)],
)

# One plugin covers every agent in the app, including agents used as tools.
approvals = ApprovalPlugin(os.environ["AAP_URL"], os.environ["AAP_INSTANCE_TOKEN"])
app = App(name="support", root_agent=support, plugins=[approvals])
check_coverage(app, approvals)


async def main() -> None:
    runner = InMemoryRunner(app=app)
    session = await runner.session_service.create_session(app_name=app.name, user_id="customer")
    message = types.Content(role="user", parts=[types.Part(text="Order 1234 was charged twice. Please sort it out.")])

    print("Waiting for decisions in withHuman ...")
    answer = ""
    async for event in runner.run_async(user_id="customer", session_id=session.id, new_message=message):
        if event.is_final_response() and event.content:
            answer = "".join(part.text or "" for part in event.content.parts or [])
    await runner.close()
    print(f"\nAgent: {answer}")


asyncio.run(main())
