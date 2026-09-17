# GoolemCode: Un Cliente CLI de Programación Asistida por IA

## Resumen

GoolemCode es un agente de codificación que corre en la terminal. Habla con modelos LLM (Claude, Ollama, DeepSeek), ejecuta herramientas reales sobre tu proyecto y se conecta a servidores MCP externos. Todo en un binario único de ~10MB, sin dependencias externas.

---

## Arquitectura

```
goolemcode/
├── main.go                    # Entry point, REPL, wiring
├── internal/
│   ├── agent/                 # Agentic loop (tool loop)
│   ├── provider/              # LLM providers
│   │   ├── anthropic.go       # Claude API
│   │   ├── ollama.go          # Ollama local
│   │   ├── deepseek.go        # DeepSeek API
│   │   └── smart.go           # Smart Router (two-model)
│   ├── tools/                 # 33 built-in tools
│   │   ├── local.go           # read/write/edit files, bash
│   │   ├── grep.go            # Regex search
│   │   ├── git.go             # Git integration
│   │   ├── web.go             # Internet access
│   │   ├── ssh.go             # Remote SSH
│   │   ├── api.go             # HTTP REST
│   │   ├── docker.go          # Docker management
│   │   ├── db.go              # SQL queries
│   │   ├── semsearch.go       # Semantic search
│   │   ├── sysinfo.go         # System monitoring
│   │   ├── registry.go        # Tool registry
│   │   └── workspace.go       # Path sandbox
│   ├── scripts/               # Reusable Python script library
│   ├── model/                 # Neutral types
│   ├── config/                # JSON config
│   ├── knowledge/             # Persistent .md memory
│   ├── tasks/                 # Task tracking system
│   ├── session/               # Conversation persistence
│   ├── checkpoint/            # Snapshots for /rewind
│   ├── statusline/            # Terminal status bar
│   ├── subagent/              # Parallel sub-agents
│   ├── ask/                   # Model query tool
│   ├── commands/              # Custom slash commands
│   ├── rag/                   # RAG document search
│   ├── mcp/                   # MCP client
│   ├── diff/                  # Diff rendering
│   ├── hooks/                 # Pre/Post tool hooks
│   ├── lineedit/              # Terminal line editor
│   └── projmem/               # Project memory (GOOLEM.md)
```

### Principio de diseño neutral

El modelo de datos (`model/types.go`) es independiente del proveedor. Cada provider traduce en su frontera entre el formato neutral y el de su API. Esto permite que Claude, Ollama y DeepSeek compartan un único bucle agéntico y el mismo conjunto de herramientas.

```go
type Message struct {
    Role        Role
    Content     string
    ToolCalls   []ToolCall
    ToolResults []ToolResult
    StopReason  string
    ProviderRaw json.RawMessage
    Usage       *Usage
    Images      []ImageData
}
```

---

## Características clave

### 1. Smart Router (dos modelos)

```go
type SmartRouter struct {
    primary       Provider
    secondary     Provider
    primaryBetter bool
    lastLabel     string
}
```

El router decide automáticamente qué modelo usar según la complejidad de la tarea:

- **Si el primario es menos capaz**: tareas complejas → secundario, simples → primario
- **Si el primario es más capaz**: tareas simples → secundario, complejas → primario

La heurística de complejidad puntúa según:

```go
func isComplexTask(messages []model.Message) bool {
    score := 0
    words := len(strings.Fields(lastUser))
    if words > 100 { score += 3 }
    if words > 50  { score += 2 }
    if words > 20  { score += 1 }
    score += codeBlocks * 2       // código = más peso
    score += complexKeywords * 2  // "refactor", "debug", etc.
    if hasImages { score += 2 }
    if questions >= 2 { score++ }
    return score >= 4
}
```

### 2. Bucle agéntico

```go
for step := 0; step < maxSteps; step++ {
    assistant, err := a.prov.Chat(ctx, messages, tools, system, onDelta, onThinking)
    if len(assistant.ToolCalls) == 0 {
        return last, nil  // respuesta final
    }
    for _, call := range assistant.ToolCalls {
        if isMutating {
            checkpoint.Capture()    // snapshot antes de mutar
            if !permit(call, args) { return denied }
        }
        result := registry.Execute(ctx, call)  // ejecuta herramienta
        results = append(results, result)
    }
    messages = append(messages, toolResults...)
}
```

Checkpoint + permiso antes de cada acción, resultados inyectados para re-evaluación.

### 3. Script library

La biblioteca de scripts en `~/.goolem/scripts/` permite al agente reutilizar código en vez de generarlo desde cero:

```go
type Store struct {
    dir   string
    stats Stats  // runs, tokens saved
}

func (s *Store) List() []Script { /* scan .py files, parse docstrings */ }
func (s *Store) Run(ctx, name, args, timeout) (string, int, error) { /* exec python3 */ }
```

Los ahorros de tokens se calculan así:
```go
s.stats.Tokens += len(scriptContent) / 4  // chars → tokens estimados
```

### 4. Sistema de tareas persistente

Las tareas se guardan en `.goolem/tasks.json` y se inyectan en el system prompt cada turno:

```go
func (s *Store) OpenSummary() string {
    for _, t := range ts {
        if t.Status != StatusDone {
            lines = append(lines, "- [" + mark + "] " + t.Title)
        }
    }
    return "TAREAS EN CURSO (retómalas):\n" + strings.Join(lines, "\n")
}
```

### 5. Herramientas (33 total)

| Categoría | Herramientas |
|---|---|
| Archivos | `read_file`, `write_file`, `edit_file`, `edit_lines`, `batch_edit` |
| Directorios | `view_directory_tree`, `list_dir` |
| Código | `grep` |
| Shell | `execute_command` |
| Git | `git_status`, `git_diff`, `git_log`, `git_commit`, `git_push`, `git_branch`, `git_stash`, `git_merge` |
| GitHub | `gh` |
| Web | `web_fetch`, `web_search` |
| SSH | `ssh_exec` |
| HTTP | `api_request` |
| Docker | `docker_exec`, `docker_logs` |
| Bases de datos | `db_query` |
| Búsqueda semántica | `semantic_search` |
| Sistema | `system_info` |
| Scripts | `script_list`, `script_run`, `script_create` |
| Memoria | `knowledge_list`, `knowledge_read`, `knowledge_write`, `knowledge_search` |
| RAG | `rag_search` |
| Agentes | `spawn_agent` |
| Consultas | `ask_user`, `ask_model` |
| Tareas | `task_write`, `task_list` |

### 6. Checkpoints + /rewind

```go
type snapshot struct {
    label string
    files map[string]*string  // nil = file didn't exist
}

func (m *Manager) Rewind() (string, bool) {
    cp := m.stack[len(m.stack)-1]
    for p, prev := range cp.files {
        if prev == nil {
            os.Remove(p)        // restore deletion
        } else {
            os.WriteFile(p, prev) // restore content
        }
    }
}
```

---

## Seguridad

### Permission gate

Cada herramienta mutadora pasa por un gate de permisos antes de ejecutarse:

```go
permit := func(toolName string, args map[string]any) bool {
    if cfg.AutoApprove || approved[toolName] { return true }
    previewChange(ws, toolName, args)  // show diff
    fmt.Printf("⚠ %s(%s)\n   1) Yes (once)   2) Always   3) No\n", toolName, args)
    ans := readLine(in)
    switch ans {
    case "1": return true
    case "2": approved[toolName] = true; return true
    }
    return false
}
```

### Sandbox de rutas

```go
func safeJoin(root, rel string) (string, error) {
    p := rel
    if !filepath.IsAbs(rel) {
        p = filepath.Join(root, rel)
    }
    p = filepath.Clean(p)
    if p != root && !strings.HasPrefix(p, root+string(os.PathSeparator)) {
        return "", fmt.Errorf("path outside working directory: %s", rel)
    }
    return p, nil
}
```

Todas las rutas, absolutas o relativas, se resuelven dentro de `root`. No hay excepción para rutas absolutas. Cualquier intento de path traversal es bloqueado.

---

## Proveedores

### Claude (Anthropic)

```go
type Anthropic struct {
    APIKey    string
    Model     string
    MaxTokens int
    Effort    string  // low | medium | high | xhigh | max
}
```

Usa streaming con `thinking: adaptive` y `output_config.effort`. Los bloques de thinking se preservan via `ProviderRaw` para que la API no los rechace.

### Ollama

```go
type Ollama struct {
    BaseURL string
    Model   string
    noTools bool  // learned from 400 errors
}
```

Endpoint `/api/chat` en modo streaming. Si un modelo no soporta tool-calling (400), lo detecta y reintenta sin tools. Compatible con qwen3, llama3, gemma4, etc.

### DeepSeek

Endpoint compatible con OpenAI. Usa streaming con tool_calls nativos.

---

## Smart Routing en detalle

### Configuración

```json
{
  "provider": "ollama",
  "model": "gemma4:31b",
  "smart_routing": {
    "enabled": true,
    "secondary_provider": "ollama",
    "secondary_model": "deepseek-v4-flash",
    "primary_more_capable": false
  }
}
```

### Heurística de complejidad

La decisión es instantánea (sin llamada extra al LLM):

1. **Longitud del prompt**: >100 palabras → +3, >50 → +2, >20 → +1
2. **Bloques de código**: cada ``` → +2
3. **Palabras clave técnicas**: refactor, debug, implement, architect, security → +2
4. **Imágenes adjuntas**: → +2
5. **Múltiples preguntas**: ≥2 signos `?` → +1

Threshold: score ≥ 4 → tarea compleja.

### Output en tiempo real

```
[→ Ollama (deepseek-v4-flash)]   ← el router muestra qué modelo usa
```

Y al final del turno:
```
[Ollama (deepseek-v4-flash) · turno 123↑ 456↓]
```

---

## Script Library

### Cómo funciona

1. El agente ejecuta `script_list` para ver scripts disponibles
2. El índice se inyecta en el system prompt (auto-descubrimiento)
3. Para tareas repetitivas, usa `script_run` en vez de generar código
4. Si no existe el script adecuado, `script_create` lo crea

### Ejemplo de script

```python
"""List directory contents with size and date.
Args: <path> [-a] [-r]
  path: directory to list (default .)
  -a: include hidden files
  -r: sort by size
"""

import os, sys
for e in sorted(os.scandir(sys.argv[1] if len(sys.argv) > 1 else "."),
                key=lambda e: e.name.lower()):
    if e.is_file():
        print(f"{e.stat().st_size:>8}  {e.name}")
```

### Ahorro estimado

Cada vez que el agente ejecuta un script en vez de generar código, se ahorran aproximadamente `len(script) / 4` tokens. El contador `📜N -Xk` en la statusline muestra el acumulado.

---

## Persistencia

### Sesión

```go
type Store struct {
    path string  // <workdir>/.goolem/session.json
}
```

Se guarda tras cada turno. Se restaura automáticamente al iniciar (sin flags).

### Tareas

```go
type Task struct {
    Title  string `json:"title"`
    Status string `json:"status"`
}
```

Persisten en `.goolem/tasks.json`. El agente las ve en el system prompt y las actualiza con `task_write`.

### Memoria (Knowledge)

Notas `.md` en `.goolem/knowledge/`. El índice se inyecta en el contexto. Lectura completa bajo demanda (divulgación progresiva).

### Perfil de usuario (adaptación)

```go
// Inyectado en el system prompt cada turno:
if profile := kb.Read("user-profile"); profile != "" {
    parts = append(parts, "USER PROFILE:\n"+profile)
}
```

El agente lee y escribe una nota `user-profile` en knowledge. Cuando descubre patrones (preferencias de estilo, comandos frecuentes, convenciones del proyecto), actualiza el perfil. Así se adapta progresivamente sin intervención manual.

### Colores en terminal

Para mejorar la legibilidad, la salida usa códigos ANSI:

| Elemento | Color | Propósito |
|---|---|---|
| `→ tool(args)` | Cyan | Identificar llamadas a herramientas |
| `✓ mensaje` | Verde | Resultados exitosos |
| `✗ mensaje` | Rojo | Errores |
| Thinking del modelo | Dim | Separar razonamiento de respuesta |
| Errores del sistema | Rojo | Alertas

---

## Estado actual y roadmap

### Implementado

- [x] Proveedores: Claude, Ollama, DeepSeek
- [x] Smart Router (dos modelos)
- [x] 40 herramientas locales
- [x] Script library con ahorro de tokens
- [x] MCP client
- [x] Checkpoints + /rewind
- [x] Permission gate
- [x] Sesión auto-resume
- [x] Tareas persistentes
- [x] Sub-agentes paralelos
- [x] Modo plan
- [x] Statusline con métricas (tokens, scripts, tareas)
- [x] Onboarding interactivo en primera ejecución
- [x] Búsqueda semántica con embeddings
- [x] Integración SSH, Docker, SQL, HTTP API
- [x] batch_edit: búsqueda y reemplazo multi-archivo
- [x] edit_lines: edición por rango de líneas
- [x] git_push, git_branch, git_stash, git_merge
- [x] gh: GitHub CLI integration
- [x] Colores ANSI en terminal (tool calls cyan, ✓ verde, ✗ rojo)
- [x] Perfil de usuario adaptativo (knowledge user-profile)
- [x] SafeJoin con sandbox estricto (path traversal fix)
- [x] Checkpoint único por turno (sin snapshots redundantes)

### Próximos

- [ ] Releases con binarios precompilados
- [ ] GitHub Actions CI/CD
- [ ] Tests de integración con LLM real
- [ ] Más scripts preinstalados
- [ ] Modo headless (pipe mode)
- [ ] Integración con OpenAI API

---

## Especificaciones técnicas

- **Lenguaje**: Go 1.25+
- **Dependencias externas**: `golang.org/x/sys`, `golang.org/x/term` (solo indirectas)
- **Tamaño binario**: ~10MB (statically linked)
- **Plataformas**: macOS arm64, Linux amd64
- **Licencia**: MIT
- **Repositorio**: https://github.com/GOOLEMLABS/goolemcode (único; el antiguo repo privado se eliminó)

---

Creado por [Pedro Luis García Alonso](https://www.linkedin.com/in/pedroluisgarcia/)

📖 Leer el artículo: [GoolemCode: Next-Gen AI Coding CLI](https://www.linkedin.com/pulse/goolemcode-next-gen-ai-coding-cli-pedro-luis-garc%C3%ADa-alonso-st7df/)
