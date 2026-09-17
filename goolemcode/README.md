# GoolemCode (Go)

**GoolemCode** es un agente de codificación en terminal (REPL) que ejecuta un
modelo de lenguaje (LLM) y le da acceso a **herramientas locales** (ficheros,
shell, git, búsqueda web, RAG, servidores MCP) con un **gate de permisos**
interactivo. Está escrito en **Go puro** (cero dependencias externas) y se
distribuye como **un binario estático** listo para descargar y ejecutar.

## Estado

✅ **Funcional y verificado end-to-end** contra Ollama, Claude y DeepSeek. Cubre:

- **REPL** con historial (↑/↓), edición de línea (←/→, Home/End, Supr),
  autocompletado con Tab, menú de confirmación 1/2/3 sin pulsar Enter.
- **Slash commands**: `/help`, `/tools`, `/clear`, `/rewind`, `/cd`, `/pwd`,
  `/plan`, `/model`, `/think`, `/tokens`, `/commands`, `/init`, `/exit`.
- **Interrupción robusta**: durante un turno, ESC o Ctrl-C cancelan la llamada al
  proveedor al instante (watchdog en modo raw, sin esperar a que atrape la
  goroutine); coexiste limpiamente con `ask_user` y las confirmaciones a mitad de
  turno (comparten el mismo lector de stdin).
- **Smart Router con fallback**: si el modelo primario falla N veces seguidas, el
  router te pregunta si pasar al secundario (`consecutive_fallback`).
- **3 providers**: Claude (Anthropic API, streaming SSE), Ollama (streaming JSON),
  DeepSeek (OpenAI-compat, streaming SSE). Todos con retry automático (backoff).
- **Herramientas locales**: `read_file` (rangos), `write_file` atómico,
  `edit_file` (replace_all), `edit_lines`, `batch_edit`,
  `view_directory_tree`, `list_dir`, `execute_command` (timeout),
  `grep` (regex), `semantic_search` (embeddings).
- **Herramientas web/red**: `web_fetch` (descarga HTTP, HTML a texto),
  `web_search` (DuckDuckGo, sin API key), `api_request` (REST).
- **Infraestructura**: `ssh_exec` (remoto), `docker_exec`/`docker_logs`,
  `db_query` (SQLite/PostgreSQL), `system_info`.
- **Git avanzado**: `git_status`/`diff`/`log`/`commit` y
  `git_push`/`branch`/`stash`/`merge` + `gh` (GitHub CLI).
- **Seguridad**: `security_scan` (credenciales/secrets),
  `security_port_scan`, `security_checks`.
- **Script library**: scripts reutilizables en `~/.goolem/scripts/`
  (`script_list`/`script_run`/`script_create`) para ahorrar tokens.
- **Consensus**: `internal/consensus` contrasta respuestas entre modelos para
  decisiones de alto riesgo.
- **Memoria persistente**: notas `.md` por proyecto y globales con índice en el
  system prompt y `scope:"global"` para la memoria compartida.
- **Tareas persistentes**: `task_list`/`task_write` con estados
  pending/in_progress/done; se inyectan en el prompt cada turno.
- **RAG**: `rag_search` consulta documentos indexados en **goolem-rag**
  (ChromaDB + nomic-embed-text).
- **MCP**: cliente para 3 transportes (stdio, SSE, HTTP) — unificación de
  herramientas locales y remotas en un solo registro.
- **Checkpoints + `/rewind`**: snapshots en memoria antes de cada acción mutadora.
- **Gate de permisos**: Y/N + "Siempre" por herramienta, con preview de diff
  coloreado. Modo `/plan` (solo lectura). Flag `--auto-approve`.
- **Sub-agentes**: `spawn_agent` para subtareas independientes (1 nivel).
- **`ask_model` / `ask_user`**: consultar a otro modelo o pedir datos al usuario.
- **Cambio de modelo en caliente**: comando `/model`.
- **Sesiones persistentes**: flag `-resume` para reanudar la conversación.
- **Directorio de trabajo móvil**: `/cd` sin perder contexto.
- **Compactación de contexto**: descarta rondas antiguas.
- **Razonamiento del modelo**: thinking visible (toggle `/think`).
- **Hooks Pre/PostToolUse**: scripts personalizados en `.goolem/hooks.json`.
- **Multimodal**: imágenes para modelos con visión (Ollama, Claude).
- **Comandos personalizados**: scripts .md en `.goolem/commands/`.

**Sin dependencias externas** (solo stdlib).

## Compilar

```bash
go build -o goolemcode .                 # binario para tu plataforma
# cross-compile (estático, cero deps):
CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -o dist/goolemcode-linux-amd64 .
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o dist/goolemcode-darwin-arm64 .
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o dist/goolemcode-windows-amd64.exe .
```

## Instalar

```bash
make install   # compila + copia a ~/bin + siembra config global si no existe
```

## Uso

```bash
./goolemcode                                          # Ollama por defecto
./goolemcode -provider ollama -model qwen3:30b-a3b
OLLAMA_HOST=http://localhost:11434 ./goolemcode
./goolemcode -provider claude                         # requiere ANTHROPIC_API_KEY
./goolemcode -provider deepseek                       # requiere DEEPSEEK_API_KEY
./goolemcode -workdir ./miproyecto --auto-approve
./goolemcode -resume                                  # reanudar sesión anterior
```

### Comandos del REPL

| Comando | Descripción |
|---|---|
| `/help` | Muestra la ayuda |
| `/tools` | Lista las herramientas disponibles |
| `/clear` | Reinicia contexto, checkpoints y sesión |
| `/rewind` | Deshace la última acción mutadora |
| `/cd` | Cambia el directorio de trabajo móvil |
| `/pwd` | Muestra el directorio actual |
| `/plan` | Alterna modo plan (solo lectura) |
| `/model` | Cambia el modelo activo en caliente |
| `/think` | Muestra/oculta el razonamiento del modelo |
| `/tokens` | Muestra el consumo de tokens |
| `/commands` | Lista comandos personalizados |
| `/init` | Analiza el proyecto y genera `GOOLEM.md` |
| `/exit` | Sale del programa |

Usa `@` para mencionar archivos: `@main.go`, `@./ruta/doc.md`. Para imágenes
(modelos con visión): `@foto.png`.

## Configuración (opcional)

`goolemcode.json` en el directorio de trabajo:

```json
{
  "provider": "ollama",
  "model": "qwen3:30b-a3b",
  "ollama_url": "http://localhost:11434",
  "max_tokens": 16000,
  "effort": "high",
  "show_thinking": true,
  "show_statusline": true,
  "rag": {
    "enabled": false,
    "base_url": "http://localhost:8003",
    "user": "tu_usuario",
    "n": 5
  },
  "mcp_servers": [
    { "name": "filesystem", "command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "."] },
    { "name": "remoto_sse",  "url": "http://localhost:8765/sse", "transport": "sse" },
    { "name": "remoto_http", "url": "https://api.ejemplo.com/mcp", "transport": "http",
      "headers": { "Authorization": "Bearer ..." } }
  ]
}
```

Servidores MCP por tres transportes: **stdio** (`command`+`args`),
**sse** (GET + POST) y **http** (streamable HTTP). La config se lee del
directorio actual; si no hay, de `~/.config/goolemcode/goolemcode.json`.

### Variables de entorno

| Variable | Descripción |
|---|---|
| `ANTHROPIC_API_KEY` | Clave API de Anthropic (provider claude) |
| `DEEPSEEK_API_KEY` | Clave API de DeepSeek |
| `OLLAMA_HOST` | URL del servidor Ollama (defecto `http://localhost:11434`) |

### Hooks personalizados

```json
{ "hooks": [
  { "event": "PostToolUse", "matcher": "write_file|edit_file",
    "command": "gofmt -w \"$GOOLEM_TOOL_PATH\"" }
] }
```

Variables: `GOOLEM_EVENT`, `GOOLEM_TOOL_NAME`, `GOOLEM_TOOL_ARGS`,
`GOOLEM_TOOL_PATH`, `GOOLEM_TOOL_RESULT`, `GOOLEM_TOOL_IS_ERROR`.

## Estructura

```
main.go              REPL + slash commands + registro + permisos
internal/
  model/             Modelo de datos neutral (Message, ToolCall, Usage)
  provider/          Interfaz + Anthropic, Ollama, DeepSeek (streaming)
    retry.go         Backoff para errores transitorios
    smart.go         Smart Router (primario/secundario, fallback, llamadas)
  pricing/           Tabla de precios por modelo y cálculo de coste/ahorro
  interrupt/         Watchdog ESC/Ctrl-C (modo raw) y lectura coordinada de stdin
  tools/             Registro unificado + herramientas locales y remotas
    local.go         read/write/edit_file, edit_lines, tree, list_dir, cmd
    grep.go          Búsqueda regex
    git.go           git_status/diff/log/commit
    web.go           web_fetch + web_search (DuckDuckGo)
    api.go           api_request (REST)
    ssh.go           ssh_exec (remoto)
    docker.go        docker_exec/docker_logs
    db.go            db_query (SQLite/PostgreSQL)
    semsearch.go     semantic_search (embeddings)
    sysinfo.go       system_info
    security.go      security_scan/port_scan/checks
    workspace.go     Sandbox móvil (/cd)
    registry.go      Registro + hooks Pre/PostToolUse
  consensus/         Consenso entre modelos (decisiones de alto riesgo)
  scripts/           Script library (~/.goolem/scripts/)
  knowledge/         Memoria .md (proyecto + global, índice en prompt)
  tasks/             Tareas persistentes
  rag/               rag_search → goolem-rag
  ask/               Catálogo de modelos (ask_model + /model)
  mcp/               Cliente MCP (stdio, SSE, HTTP)
  checkpoint/        Snapshots para /rewind
  agent/             Bucle agéntico + compactHistory
  session/           Persistencia de conversaciones
  subagent/          spawn_agent
  hooks/             Hooks Pre/PostToolUse
  projmem/           Instrucciones del proyecto (GOOLEM.md / CLAUDE.md)
  diff/              Diferencia LCS coloreada
  statusline/        Línea de estado sobre el prompt
  lineedit/          Editor con historial, raw mode, Tab, ReadKey
  config/            Flags + entorno + goolemcode.json
  commands/          Comandos slash personalizados
  usage/             Contador de tokens por modelo
```

## Por qué Go (vs la versión Python)

El runtime de un agente de código es I/O-bound (espera al LLM), así que el
lenguaje del bucle no cambia la velocidad percibida. La velocidad real vive en la
**inferencia** (llama.cpp), que se usa por HTTP vía Ollama o `llama-server` — sin
`cgo`, conservando el binario estático. Go se eligió por la distribución: un
fichero que corre en cualquier sitio.
