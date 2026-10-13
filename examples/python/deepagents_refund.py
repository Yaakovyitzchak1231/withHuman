# /// script
# requires-python = ">=3.11,<3.14"
# dependencies = [
#   "agentapprovalprotocol[deep-agents]",
#   "langchain-openai",
# ]
# ///
"""A Deep Agents support agent and its billing subagent, whose tool calls wait for withHuman.

Deep Agents' own tools, such as write_todos and task, are sent for approval too.

    uv run --env-file ../.env deepagents_refund.py
    uv run --env-file ../.env deepagents_refund.py --amount 2500
"""

import argparse
import os

from deepagents import create_deep_agent
from langchain_openai import AzureChatOpenAI, ChatOpenAI

from agentapprovalprotocol.langgraph import ApprovalMiddleware, deep_agent_subagents

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


def chat_model():
    if os.environ.get("AZURE_OPENAI_ENDPOINT"):
        return AzureChatOpenAI(azure_deployment=os.environ["AZURE_OPENAI_DEPLOYMENT"])
    return ChatOpenAI(model=os.environ["OPENAI_MODEL"])


billing_specialist = {
    "name": "billing-specialist",
    "description": "Issues refunds. Give it the order ID, the amount and the reason.",
    "system_prompt": "You issue refunds. Call issue_refund once with the details you were given, then report what happened.",
    "tools": [issue_refund],
}

approvals = ApprovalMiddleware(os.environ["AAP_URL"], os.environ["AAP_INSTANCE_TOKEN"])

agent = create_deep_agent(
    model=chat_model(),
    tools=[lookup_order],
    system_prompt="You are a support agent. Look the order up, then hand the refund to the billing-specialist subagent.",
    middleware=[approvals],
    # Subagents don't inherit middleware: this adds approvals to each one.
    subagents=deep_agent_subagents(approvals, [billing_specialist]),
)

print("Waiting for decisions in withHuman ...")
result = agent.invoke({"messages": [{"role": "user", "content": "Order 1234 was charged twice. Please sort it out."}]})
print(f"\nAgent: {result['messages'][-1].content}")
