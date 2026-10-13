# /// script
# requires-python = ">=3.10,<3.14"
# dependencies = [
#   "agentapprovalprotocol[claude-agent-sdk]",
# ]
# ///
"""A Claude Agent SDK support agent and its billing subagent, whose tool calls wait for withHuman.

The model is Claude: set ANTHROPIC_API_KEY, or sign in to Claude Code on this machine.

    uv run --env-file ../.env claude_agent_sdk_refund.py
    uv run --env-file ../.env claude_agent_sdk_refund.py --amount 2500
"""

import argparse
import asyncio
import os
import tempfile

from claude_agent_sdk import AgentDefinition, ClaudeAgentOptions, ResultMessage, create_sdk_mcp_server, query, tool

from agentapprovalprotocol.claude_agent_sdk import ApprovalHooks

parser = argparse.ArgumentParser()
parser.add_argument("--amount", type=float, default=40.0, help="the duplicate charge the order lookup finds")
args = parser.parse_args()


def text(value: str) -> dict:
    return {"content": [{"type": "text", "text": value}]}


@tool("lookup_order", "Look up an order's charges.", {"order_id": str})
async def lookup_order(input: dict) -> dict:
    return text(f"Order {input['order_id']}: two charges of ${args.amount:,.2f} on the same card, 2 seconds apart.")


@tool("issue_refund", "Refund part of a customer's order.", {"order_id": str, "amount": float, "reason": str})
async def issue_refund(input: dict) -> dict:
    print(f"\n>>> Refunding {input['amount']} on order {input['order_id']}")
    return text(f"Refunded {input['amount']} on order {input['order_id']}.")


approvals = ApprovalHooks(os.environ["AAP_URL"], os.environ["AAP_INSTANCE_TOKEN"])

# options() adds the approval hooks, and leaves out ~/.claude/settings.json so a
# Claude Code approval hook installed there doesn't ask a second time.
options = approvals.options(
    ClaudeAgentOptions(
        system_prompt="You are a support agent. Look the order up, then use the billing-specialist subagent to refund any duplicate charge.",
        mcp_servers={"shop": create_sdk_mcp_server("shop", tools=[lookup_order, issue_refund])},
        # Only the shop server: no MCP servers from settings or claude.ai connectors.
        strict_mcp_config=True,
        tools=["Agent"],
        allowed_tools=["mcp__shop__lookup_order", "Agent"],
        agents={
            "billing-specialist": AgentDefinition(
                description="Issues refunds. Give it the order ID, the amount and the reason.",
                prompt="You issue refunds. Call issue_refund once with the details you were given, then report what happened.",
                tools=["mcp__shop__issue_refund"],
            )
        },
        cwd=tempfile.mkdtemp(prefix="withhuman-example-"),
    )
)


async def main() -> None:
    print("Waiting for decisions in withHuman ...")
    async for message in query(prompt="Order 1234 was charged twice. Please sort it out.", options=options):
        # The subagent runs in the background, so the agent can answer more than once.
        if isinstance(message, ResultMessage) and message.result:
            print(f"\nAgent: {message.result}")


asyncio.run(main())
