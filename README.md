# GoolemCode

An AI coding assistant CLI for the terminal. It lets you develop by choosing the LLM backend (Claude, Ollama, DeepSeek), call external **MCP** servers, and act on the project through real tools (read/write/edit files, run bash, SSH, Docker, databases, etc.).

## Features

- **Multi-provider**: Claude, Ollama, DeepSeek — switch between them at runtime with `/model`
- **Smart Router**: two-model routing — automatically delegates complex tasks to a more capable model and simple tasks to a lighter one, with transparent cost-savings tracking
- **Cost & pricing engine**: per-model token pricing (Anthropic, DeepSeek, Ollama), estimated session cost in the status bar, and savings report when Smart Router is active
- **Smart Router fallback**: if the primary model fails a few times in a row, the router asks whether to switch to the secondary one (`consecutive_fallback`)
- **Robust interrupt**: during a turn, ESC or Ctrl-C cancels the in-flight LLM call immediately (raw-mode watchdog), coexisting cleanly with `ask_user` and mid-turn confirmations (shared stdin reader)
- **40+ built-in tools**: file management, code search, git, web browsing, SSH, Docker, DB queries, semantic search, security scanning, system monitoring, and more
- **Script library**: reusable Python scripts in `~/.goolem/scripts/` — the agent uses them instead of generating repetitive code, saving tokens
- **MCP support**: connect external Model Context Protocol servers
- **Knowledge base**: per-project and global `.md` notes that the agent reads/writes
- **Checkpoints & undo**: `/rewind` undoes the last mutating action
- **Permission gate**: each file write or command execution asks for confirmation (1/2/3) unless `--auto-approve` is set
- **Session persistence**: conversations are auto-saved and resumed
- **Sub-agents**: `spawn_agent` delegates isolated subtasks to child agents
- **Multi-modal**: supports images as input (for vision models)
- **Recursive self-improvement**: el agente puede leer, editar y mejorar su propio código fuente; cada mejora incrementa sus capacidades para la siguiente iteración

## Installation

### Requirements

- **Go 1.25+** (to build from source)
- **Ollama** (optional, for local models) — `curl -fsSL https://ollama.com/install.sh | sh`
- **API keys** (optional, for Claude or DeepSeek) — environment variables `ANTHROPIC_API_KEY` / `DEEPSEEK_API_KEY`

### From source

```bash
git clone https://github.com/GOOLEMLABS/goolemcode.git
cd goolemcode/goolemcode
make build        # compile
make install      # compile + copy to ~/bin/goolemcode
```

Or manually:
```bash
cd goolemcode/goolemcode
go build -o goolemcode .
./goolemcode
```

### Download binary

Download the pre-built binary from [Releases](https://github.com/GOOLEMLABS/goolemcode/releases):

```bash
# Linux amd64
wget https://github.com/GOOLEMLABS/goolemcode/releases/latest/download/goolemcode-linux-amd64
chmod +x goolemcode-linux-amd64
sudo mv goolemcode-linux-amd64 /usr/local/bin/goolemcode

# macOS arm64
curl -LO https://github.com/GOOLEMLABS/goolemcode/releases/latest/download/goolemcode-darwin-arm64
chmod +x goolemcode-darwin-arm64
sudo mv goolemcode-darwin-arm64 /usr/local/bin/goolemcode
```

### Add to PATH

If you installed with `make install`, the binary is at `~/bin/goolemcode`. Make sure `~/bin` is in your PATH:

```bash
echo 'export PATH="$HOME/bin:$PATH"' >> ~/.bashrc
source ~/.bashrc
```

## First run

On first launch without a config file, GoolemCode runs an interactive setup wizard:

```bash
goolemcode
```

It will ask:
- **Provider** (ollama / claude / deepseek)
- **Ollama server URL** (default http://localhost:11434)
- **Default model**
- **API keys** (if choosing Claude or DeepSeek)
- **Smart routing** (two-model mode based on task complexity)

Answers are saved to `goolemcode.json` in the current directory.

### Manual config

Create `goolemcode.json` in your project or `~/.config/goolemcode/goolemcode.json`:

```json
{
  "provider": "ollama",
  "model": "qwen3:30b-a3b",
  "ollama_url": "http://localhost:11434",
  "smart_routing": {
    "enabled": true,
    "secondary_model": "deepseek-v4-flash",
    "primary_more_capable": false
  }
}
```

## Usage

```bash
goolemcode                                # interactive (default: ollama)
goolemcode -provider claude               # use Claude
goolemcode -model deepseek-chat           # override model
goolemcode -workdir /path/to/project      # working directory
goolemcode --auto-approve                 # skip confirmations
```

### Chat commands

| Command | Description |
|---|---|
| `/help` | Show help |
| `/cd <dir>` | Change working directory |
| `/pwd` | Show current directory |
| `/clear` | Reset context and checkpoints |
| `/rewind` | Undo last agent action |
| `/tools` | List available tools |
| `/tokens` | Token usage and cost per model |
| `/todo` | Show pending tasks |
| `/think` | Toggle model reasoning display |
| `/plan` | Toggle read-only plan mode |
| `/model <name>` | Switch active model at runtime |
| `/init` | Analyze project and create GOOLEM.md |
| `/exit` | Quit |

## Tools (41)

| Tool | Description |
|---|---|
| `read_file` / `write_file` / `edit_file` | File operations |
| `edit_lines` | Replace specific line range in a file |
| `batch_edit` | Find & replace across multiple files |
| `view_directory_tree` / `list_dir` | Directory listing |
| `grep` | Regex search in code |
| `execute_command` | Shell commands |
| `git_status` / `git_diff` / `git_log` / `git_commit` | Git integration |
| `git_push` / `git_branch` / `git_stash` / `git_merge` | Advanced Git |
| `gh` | GitHub CLI (issues, PRs, releases) |
| `web_fetch` / `web_search` | Internet access |
| `ssh_exec` | Remote SSH execution |
| `api_request` | REST API calls |
| `docker_exec` / `docker_logs` | Docker management |
| `db_query` | SQL (SQLite/PostgreSQL) |
| `semantic_search` | Semantic search (embeddings) |
| `system_info` | Disk, RAM, processes, network |
| `script_list` / `script_run` / `script_create` | Script library |
| `knowledge_list` / `read` / `write` / `search` | Persistent memory |
| `rag_search` | Personal documents |
| `spawn_agent` | Sub-agents |
| `ask_user` / `ask_model` | Queries |
| `task_write` / `task_list` | Task tracking |

## Smart Routing

Configure a secondary model for complex tasks (or simple if primary is more capable):

```json
{
  "smart_routing": {
    "enabled": true,
    "secondary_model": "deepseek-v4-flash",
    "primary_more_capable": false
  }
}
```

**How it works:** the router evaluates message complexity (length, code blocks, keywords, image count).  
Simple queries go to the cheaper secondary model; complex ones to the primary.

### Cost savings

GoolemCode tracks token usage per model and estimates the real cost using a built-in pricing table
(Anthropic, DeepSeek, Ollama — prices as of July 2026). When you exit a Smart Router session, it prints
a savings breakdown:

```
Ahorro estimado por Smart Router:
  Claude (claude-opus-4-8)        50000↑  25000↓ → $1.8750
  DeepSeek (deepseek-v4-flash)   200000↑ 100000↓ → $0.0560
  TOTAL                          250000↑ 125000↓
  Coste real:        $1.9310
  Coste hipotético (todo con claude-opus-4-8): $4.3750
  Ahorro:            $2.4440 (55.9%)
```

The cost estimate also appears in the terminal status bar (`$X.XXXX` per session).

**Validation:** the savings algorithm is cross-validated by
`internal/pricing/pricing_test.go` against an independent reference implementation
(historical Python replica, kept outside the repo), yielding identical results
(verified with `ask_model` using DeepSeek as a second opinion).

## Script library

Scripts in `~/.goolem/scripts/` are auto-discovered. The agent uses them over generating code:

```bash
script_list                          # list available
script_run name=list_dir args="."    # execute
script_create name=my_script code="..."  # create new
```

## Development

```bash
make build   # compile
make test    # run tests (57 passing)
make vet     # static analysis
make install # compile + install
make self    # self-improvement
```

### Recursive self-improvement

GoolemCode es capaz de mejorarse a sí mismo. El agente puede leer, editar, refactorizar y
extender su propio código fuente usando las mismas herramientas que usa en tu proyecto.
Esto significa que GoolemCode **se automejora recursivamente**: cada mejora que hace al
código incrementa sus capacidades, y esas nuevas capacidades le permiten hacer mejoras más
profundas en la siguiente iteración.

Ejemplos de auto-mejora:

- **Optimización de herramientas**: el agente analiza el uso de sus propios tools y los refina.
- **Corrección de bugs**: detecta errores en su código, los corrige, compila y testea.
- **Nuevas funcionalidades**: añade features planificadas analizando el código existente.
- **Script library**: crea y actualiza scripts en `~/.goolem/scripts/` para ahorrar tokens
  en futuras sesiones.

El ciclo es: **agente analiza → propone mejora → edita código → compila → testea → itera**.

Esto convierte a GoolemCode en un sistema que no solo asiste en tu código, sino que
**mejora su propio código base** mientras te asiste.

Zero external dependencies (`golang.org/x/sys` + `golang.org/x/term`).

## License

MIT

---

Created by [Pedro Luis García Alonso](https://www.linkedin.com/in/pedroluisgarcia/)

📖 Read the article: [GoolemCode: Next-Gen AI Coding CLI](https://www.linkedin.com/pulse/goolemcode-next-gen-ai-coding-cli-pedro-luis-garc%C3%ADa-alonso-st7df/)
