# /// script
# requires-python = ">=3.10,<3.14"
# dependencies = [
#   "agentapprovalprotocol[crewai]",
#   "crewai[azure-ai-inference]",
# ]
# ///
"""A CrewAI support agent and its billing coworker, whose tool calls wait for withHuman.

CrewAI cannot pause a run, so every call waits for its decision.

    uv run --env-file ../.env crewai_refund.py
    uv run --env-file ../.env crewai_refund.py --amount 2500
"""

import argparse
import os

os.environ.setdefault("CREWAI_DISABLE_TELEMETRY", "true")
os.environ.setdefault("OTEL_SDK_DISABLED", "true")

from crewai import LLM, Agent, Crew, Task
from crewai.tools import tool

from agentapprovalprotocol.crewai import ApprovalHooks

parser = argparse.ArgumentParser()
parser.add_argument("--amount", type=float, default=40.0, help="the duplicate charge the order lookup finds")
args = parser.parse_args()


@tool("lookup_order")
def lookup_order(order_id: str) -> str:
    """Look up an order's charges."""
    return f"Order {order_id}: two charges of ${args.amount:,.2f} on the same card, 2 seconds apart."


@tool("issue_refund")
def issue_refund(order_id: str, amount: float, reason: str) -> str:
    """Refund part of a customer's order."""
    print(f"\n>>> Refunding {amount} on order {order_id}")
    return f"Refunded {amount} on order {order_id}."


def chat_model() -> LLM:
    if os.environ.get("AZURE_OPENAI_ENDPOINT"):
        return LLM(
            model=f"azure/{os.environ['AZURE_OPENAI_DEPLOYMENT']}",
            endpoint=os.environ["AZURE_OPENAI_ENDPOINT"],
            api_key=os.environ["AZURE_OPENAI_API_KEY"],
            api_version=os.environ["OPENAI_API_VERSION"],
        )
    return LLM(model=f"openai/{os.environ['OPENAI_MODEL']}")


llm = chat_model()
billing = Agent(
    role="Billing specialist",
    goal="Issue refunds exactly as requested",
    backstory="You issue refunds. Call issue_refund once with the order ID, amount and reason you were given.",
    tools=[issue_refund],
    llm=llm,
)
support = Agent(
    role="Support agent",
    goal="Resolve customer billing complaints",
    backstory="You look orders up and delegate refunds to the Billing specialist with the order ID, amount and reason.",
    tools=[lookup_order],
    llm=llm,
    allow_delegation=True,
)
task = Task(
    description="Order 1234 was charged twice. Please sort it out.",
    expected_output="What you found and what was done for the customer.",
    agent=support,
)
crew = Crew(agents=[support, billing], tasks=[task])

# The hooks cover every crew, agent and delegation in this process.
ApprovalHooks(os.environ["AAP_URL"], os.environ["AAP_INSTANCE_TOKEN"]).install()

print("Waiting for decisions in withHuman ...")
result = crew.kickoff()
print(f"\nAgent: {result.raw}")
