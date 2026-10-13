# Connect your own agent

These examples put an agent you build with an agent framework behind
withHuman. Each one is a small support agent that looks up an order and
refunds a duplicate charge. Every tool call goes to withHuman before it
runs. Calls made by sub-agents do too.

They use the [Agent Approval Protocol SDKs](https://agentapprovalprotocol.io/docs/sdks/overview).

| Framework | Example |
| --- | --- |
| LangGraph | [`python/langgraph_refund.py`](python/langgraph_refund.py) |
| Deep Agents | [`python/deepagents_refund.py`](python/deepagents_refund.py) |
| Pydantic AI | [`python/pydanticai_refund.py`](python/pydanticai_refund.py) |
| Google ADK | [`python/adk_refund.py`](python/adk_refund.py) |
| OpenAI Agents SDK | [`python/openai_agents_refund.py`](python/openai_agents_refund.py) |
| Claude Agent SDK | [`python/claude_agent_sdk_refund.py`](python/claude_agent_sdk_refund.py) |
| CrewAI | [`python/crewai_refund.py`](python/crewai_refund.py) |
| Vercel AI SDK | [`typescript/ai_sdk_refund.ts`](typescript/ai_sdk_refund.ts) |
| Mastra | [`typescript/mastra_refund.ts`](typescript/mastra_refund.ts) |

## Set up withHuman

1. Start withHuman. See the [Quickstart](../README.md#quickstart).
2. Choose **Connect an agent** and create a **Service agent**.
3. On the agent's page, choose **New instance**, then **Generate an API
   key**. Copy the key.
4. In **Approval pipelines**, open the **entry** pipeline and remove the
   **Approve every call** block. Calls then come to you for a decision.

## Set up the examples

Copy the settings file and fill it in:

```sh
cp .env.example .env
```

- `AAP_INSTANCE_TOKEN`: the API key from step 3.
- `OPENAI_API_KEY` and `OPENAI_MODEL`: the model the agents use. To use
  another OpenAI-compatible endpoint, such as Azure OpenAI or a local
  model server, also set `OPENAI_BASE_URL`.

The Claude Agent SDK example uses Claude. Set `ANTHROPIC_API_KEY`, or sign
in to Claude Code on the same machine.

## Run an example

The Python examples need [uv](https://docs.astral.sh/uv/), which installs
each example's packages on the first run:

```sh
cd python
uv run --env-file ../.env langgraph_refund.py
```

The TypeScript examples need Node.js 22.18 or later:

```sh
cd typescript
npm install
npm run ai-sdk
```

Use `npm run mastra` for the Mastra example.

## What to expect

- Each tool call appears in the review queue. The agent waits for your
  decision.
- An approved call runs once. The request shows when it was used and
  whether it succeeded.
- A denied call does not run. The agent receives your note.
- A call made by a sub-agent is its own request.

To change the duplicate charge, add `--amount 2500` to a Python command,
or `-- --amount 2500` to an `npm run` command. Use it to try an approval
rule, such as approving refunds under 100 automatically.
