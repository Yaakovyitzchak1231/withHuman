# /// script
# requires-python = ">=3.10,<3.14"
# dependencies = [
#   "agentapprovalprotocol[langgraph]",
#   "langchain-openai",
# ]
# ///
"""A LangGraph support agent whose tool calls wait for withHuman.

    uv run --env-file ../.env langgraph_refund.py
    uv run --env-file ../.env langgraph_refund.py --amount 2500
"""

import argparse
import os

from langchain.agents import create_agent
from langchain_openai import AzureChatOpenAI, ChatOpenAI

from agentapprovalprotocol.langgraph import ApprovalMiddleware

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


# Sends every tool call to withHuman and waits for the decision.
approvals = ApprovalMiddleware(os.environ["AAP_URL"], os.environ["AAP_INSTANCE_TOKEN"])

agent = create_agent(
    chat_model(),
    tools=[lookup_order, issue_refund],
    middleware=[approvals],
    system_prompt="You are a support agent. Look the order up, then refund any duplicate charge.",
)

print("Waiting for decisions in withHuman ...")
result = agent.invoke({"messages": [{"role": "user", "content": "Order 1234 was charged twice. Please sort it out."}]})
print(f"\nAgent: {result['messages'][-1].content}")
