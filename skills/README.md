# withHuman skills

Agent skills that teach a coding agent (Claude Code, Codex, Cursor and
others) to set up and use withHuman. Install them with:

```sh
npx skills add withHumanAI/withHuman
```

| Skill | What it teaches |
| --- | --- |
| [`withhuman`](withhuman/SKILL.md) | What withHuman is, its concepts and editions, how a gated agent should behave, and which skill to use next. |
| [`withhuman-cli`](withhuman-cli/SKILL.md) | Installing the `withhuman` CLI, connecting a runtime, checking, refreshing, upgrading and removing it. |
| [`withhuman-pipelines`](withhuman-pipelines/SKILL.md) | Writing approval pipelines and escalation paths through the withHuman MCP server. |

The skills work against hosted and self-hosted withHuman alike. They name
hosted-only features as such and read deployment-specific grammar from the
MCP server's `read_reference` tool instead of copying it.

Tests keep them honest: `internal/cli/skills_test.go` checks each skill's
frontmatter and that every `withhuman` command it shows is one the CLI
accepts, and `TestMCPListsTheManagementCatalog` in `internal/httpapi`
checks that every management MCP tool a skill names is served.
