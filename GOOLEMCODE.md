# GOOLEMCODE.md

This file provides guidance to AI coding assistants when working with this repository.

## What GoolemCode Is

An AI coding-assistant CLI written in **Go**. It lets you develop by choosing the LLM backend
(Claude, Ollama, or DeepSeek), call external **MCP** servers, and act on the project through
real tools (read/write/edit files, run bash, SSH, Docker, DB queries, etc.).

The **original design spec** (`OPENCODE_CLAUDE_CODE_PROMPT.md`, under the old name "OpenCode")
is historical and is **not** distributed with this repo. The implementation deliberately diverges
from it (see below). Don't follow that spec's code verbatim.

## Commands

```bash
go build -o goolemcode .              # build
go test ./...                         # run all tests
goolemcode                            # interactive chat (default: Claude via ANTHROPIC_API_KEY)
goolemcode -provider ollama           # use local Ollama
goolemcode -auto-approve              # skip Y/N confirmation prompts
goolemcode -resume                    # resume saved conversation
goolemcode -workdir /path/to/project  # set working directory
goolemcode -model claude-sonnet-4-5   # choose model
```

`ANTHROPIC_API_KEY` and `DEEPSEEK_API_KEY` are read from the environment. In-chat slash
commands: `/help`, `/tools`, `/clear`, `/rewind`, `/exit` (bare words also work). `/rewind`
undoes the agent's last checkpoint.

## Key architectural decisions

1. **Native tool calling, not text parsing.** The spec parsed `tool_name({...})` out of model
   text with a regex. That's fragile and doesn't map to MCP. We use each provider's native
   tool-use instead.
2. **A provider-neutral data model is the spine** (`internal/model/types.go`). `Message`,
   `ToolDefinition`, `ToolCall`, `ToolResult`. The agent loop and tool registry speak ONLY
   these types.
3. **One manual agentic loop for all providers** (`internal/agent/agent.go`). We deliberately
   do NOT use provider-specific runners — that would split control flow.
4. **MCP tools stay neutral** (`internal/mcp/`). `list_tools()` → `ToolDefinition`; `call_tool()`
   → `ToolResult`. Provider-agnostic, so MCP works under Claude, Ollama, and DeepSeek.
5. **Smart Router** (`internal/provider/smart.go`) — routes requests to primary or fallback
   model based on token budgets and pricing to save costs.

## Architecture map

- **`internal/model/types.go`** — neutral types. The contract everything else depends on.
- **`internal/agent/agent.go`** — the loop: `provider.Chat()` → for each `ToolCall`,
  `executeTool` (if mutating: checkpoint + permission gate) → append results as
  `RoleTool` message → repeat until no tool calls or `maxIterations`. `SYSTEM_PROMPT` lives here.
- **`internal/checkpoint/checkpoint.go`** — `CheckpointManager`: in-memory snapshots before
  mutating actions; `/rewind` pops the last one and restores files.
- **`internal/config/config.go`** — TOML (`.goolemcode/config.toml`, cwd then `~`) + env;
  builds the provider (`claude`/`ollama`/`deepseek`).
- **`internal/provider/`**:
  - **`provider.go`** — `Provider` interface: `Chat(messages, tools, system, onDelta)`.
  - **`anthropic.go`** — Claude via `ANTHROPIC_API_KEY`, streaming, thinking blocks.
  - **`ollama.go`** — Ollama `/api/chat`, OpenAI-shaped tools.
  - **`deepseek.go`** — DeepSeek API, streaming, tool calls.
  - **`smart.go`** — Smart Router with pricing-aware fallback.
  - **`retry.go`** — Retry logic with exponential backoff.
- **`internal/tools/`**:
  - **`registry.go`** — `ToolRegistry` unites local + MCP tools; `isMutating(name)` drives
    the checkpoint/permission gate.
  - **`local.go`** — `read_file`, `write_file`, `edit_file`, `view_directory_tree`,
    `list_dir`, `bash`, `grep`. Read-only tools are `mutating=false`. All paths sandboxed.
  - **`web.go`** — `web_fetch`, `web_search`, `api_request` (HTTP client).
  - **`git.go`** — `git_status`, `git_diff`, `git_log`, `git_commit`, `git_push`, etc.
  - **`docker.go`**, **`ssh.go`**, **`db.go`**, **`sysinfo.go`** — specialized tools.
  - **`workspace.go`** — path sandboxing for all file operations.
- **`internal/mcp/`** — MCP client: `manager.go`, `stdio.go`, `httpsse.go`, `client.go`.
  Tools are registered prefixed `server__tool` (mutating=true).
- **`internal/session/session.go`** — conversation persistence and resume.
- **`internal/lineedit/lineedit.go`** — multiline input editor with wrapping.
- **`internal/commands/commands.go`** — slash-command parser and dispatcher.
- **`internal/statusline/statusline.go`** — bottom status bar (provider, model, tokens).
- **`internal/knowledge/knowledge.go`** — project knowledge base (.md notes).
- **`internal/projmem/projmem.go`** — project memory (CLAUDE.md / GOOLEMCODE.md loading).
- **`internal/tasks/tasks.go`** — task tracking (pending/in_progress/done).
- **`internal/scripts/scripts.go`** — local script library.
- **`internal/rag/rag.go`** — RAG search (personal documents).
- **`internal/subagent/subagent.go`** — sub-agent spawning for parallel work.
- **`internal/diff/diff.go`** — structured diff/merge logic.
- **`internal/usage/usage.go`** — token usage tracking and reporting.
- **`internal/ask/ask.go`** — user prompts for confirmation.
- **`internal/hooks/hooks.go`** — pre/post tool-execution hooks.
- **`internal/pricing/pricing.go`** — model pricing data for Smart Router.
- **`main.go`** — entry point, flag parsing, main loop.

## Conventions

- **UI strings and the system prompt are in Spanish**; code/identifiers in English.
- Model defaults to `claude-opus-4-8`. For any Claude API change, check the latest
  Anthropic API docs — don't copy outdated provider code.
- Adding a provider = implement the `Provider` interface + a branch in `config.go`.
  Adding a tool = `registry.Register(definition, handler)`.

## Verification

**Tested here:** agent loop, tool registry, local tools + path sandbox, checkpoint/rewind,
permissions, provider routing, line editor, status line, commands, hooks, knowledge base,
pricing, web tools.

**NOT run here** (no network): the live LLM round trips, MCP `ClientSession` API,
Smart Router fallback. Before trusting them, verify on an env with credentials.

## Repo layout & publishing

- The repository root holds the docs and CI (`.github/workflows/`); the Go module lives in `goolemcode/`.
- `tools/public-tree-guard.sh` is the **pre-publish guard**. It fails if the tree contains secrets
  (GitHub/Anthropic/AWS tokens, private keys), private IPs (`192.168.…`, `10.…`, `172.16-31.…`),
  developer paths (`/Users/<name>`, `/home/<name>`) or tracked local state (`.goolem/`, `.env`,
  `goolemcode.json`). Run it by hand before publishing; the local `.git/hooks/pre-push` hook calls
  it so a push is blocked while it fails.
- Never commit `.goolem/` (agent session/history/knowledge), `goolemcode.json` (local config with
  personal IP/model) or `.env`. Use `goolemcode/goolemcode.example.json` and `.env.example` as templates.
