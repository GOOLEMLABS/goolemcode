// Command goolemcode es un agente de codificación en terminal (REPL) que habla
// con Claude u Ollama, usa herramientas locales y servidores MCP, con
// checkpoints (/rewind) y confirmación de permisos.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/GOOLEMLABS/goolemcode/internal/agent"
	"github.com/GOOLEMLABS/goolemcode/internal/ask"
	"github.com/GOOLEMLABS/goolemcode/internal/checkpoint"
	"github.com/GOOLEMLABS/goolemcode/internal/commands"
	"github.com/GOOLEMLABS/goolemcode/internal/config"
	"github.com/GOOLEMLABS/goolemcode/internal/consensus"
	"github.com/GOOLEMLABS/goolemcode/internal/diff"
	"github.com/GOOLEMLABS/goolemcode/internal/hooks"
	"github.com/GOOLEMLABS/goolemcode/internal/interrupt"
	"github.com/GOOLEMLABS/goolemcode/internal/knowledge"
	"github.com/GOOLEMLABS/goolemcode/internal/lineedit"
	"github.com/GOOLEMLABS/goolemcode/internal/mcp"
	"github.com/GOOLEMLABS/goolemcode/internal/model"
	"github.com/GOOLEMLABS/goolemcode/internal/projmem"
	"github.com/GOOLEMLABS/goolemcode/internal/provider"
	"github.com/GOOLEMLABS/goolemcode/internal/rag"
	"github.com/GOOLEMLABS/goolemcode/internal/scripts"
	"github.com/GOOLEMLABS/goolemcode/internal/securefs"
	"github.com/GOOLEMLABS/goolemcode/internal/session"
	"github.com/GOOLEMLABS/goolemcode/internal/statusline"
	"github.com/GOOLEMLABS/goolemcode/internal/subagent"
	"github.com/GOOLEMLABS/goolemcode/internal/tasks"
	"github.com/GOOLEMLABS/goolemcode/internal/tools"
	"github.com/GOOLEMLABS/goolemcode/internal/usage"
)

var globalScriptStore *scripts.Store

func main() {
	cfg := config.Load()
	ctx := context.Background()

	var prov provider.Provider
	switch cfg.Provider {
	case "claude":
		if os.Getenv("ANTHROPIC_API_KEY") == "" {
			fmt.Fprintln(os.Stderr, "Missing ANTHROPIC_API_KEY (environment).")
			os.Exit(1)
		}
		prov = provider.NewAnthropic(cfg.Model, cfg.MaxTokens, cfg.Effort)
	case "ollama":
		prov = provider.NewOllama(cfg.OllamaURL, cfg.Model)
	case "deepseek":
		key := os.Getenv("DEEPSEEK_API_KEY")
		if key == "" {
			fmt.Fprintln(os.Stderr, "Missing DEEPSEEK_API_KEY (environment).")
			os.Exit(1)
		}
		prov = provider.NewDeepSeek(key, cfg.Model)
	default:
		fmt.Fprintf(os.Stderr, "unknown provider: %s (use claude | ollama | deepseek)\n", cfg.Provider)
		os.Exit(1)
	}

	// Smart routing: envuelve primary + secondary en un router inteligente
	if cfg.SmartRouting.Enabled {
		secProv := cfg.SmartRouting.SecondaryProvider
		if secProv == "" {
			secProv = cfg.Provider
		}
		secModel := cfg.SmartRouting.SecondaryModel
		if secModel == "" {
			fmt.Fprintln(os.Stderr, "smart_routing.secondary_model required")
			os.Exit(1)
		}
		var secondary provider.Provider
		switch secProv {
		case "claude":
			if os.Getenv("ANTHROPIC_API_KEY") == "" {
				fmt.Fprintln(os.Stderr, "Missing ANTHROPIC_API_KEY for smart_routing secondary (claude).")
				os.Exit(1)
			}
			secondary = provider.NewAnthropic(secModel, cfg.MaxTokens, cfg.Effort)
		case "ollama":
			url := cfg.SmartRouting.SecondaryOllamaURL
			if url == "" {
				url = cfg.OllamaURL
			}
			secondary = provider.NewOllama(url, secModel)
		case "deepseek":
			key := os.Getenv("DEEPSEEK_API_KEY")
			if key == "" {
				fmt.Fprintln(os.Stderr, "Missing DEEPSEEK_API_KEY for smart_routing secondary (deepseek).")
				os.Exit(1)
			}
			secondary = provider.NewDeepSeek(key, secModel)
		default:
			fmt.Fprintf(os.Stderr, "smart_routing: unknown secondary provider: %s\n", secProv)
			os.Exit(1)
		}
		prov = provider.NewSmartRouter(prov, secondary, cfg.SmartRouting.PrimaryMoreCapable)
	}

	tracker := usage.New() // consumo de tokens desglosado por modelo
	reg := tools.NewRegistry()
	ws := tools.NewWorkspace(cfg.Workdir) // directorio de trabajo móvil (pilotable con /cd)

	// Endurece los permisos del estado local del agente (0700 en directorios,
	// 0600 en ficheros): .goolem/ guarda sesión, HISTORIA de conversación,
	// tareas y notas, que pueden contener datos personales del entorno. Migra
	// instalaciones antiguas creadas con 0755/0644.
	securefs.HardenTree(filepath.Join(cfg.Workdir, ".goolem"))
	securefs.HardenTree(cfg.GlobalKnowledgeDir)

	tools.RegisterLocal(reg, ws)
	tools.RegisterGrep(reg, ws)
	tools.RegisterGit(reg, ws)
	tools.RegisterWeb(reg)
	tools.RegisterSSH(reg)
	tools.RegisterAPI(reg)
	tools.RegisterDocker(reg)
	tools.RegisterDB(reg)
	tools.RegisterSemanticSearch(reg, cfg.OllamaURL)
	tools.RegisterSystemInfo(reg)
	tools.RegisterSecurity(reg, ws)

	kb := knowledge.New(cfg.KnowledgeDir)
	gkb := knowledge.New(cfg.GlobalKnowledgeDir)
	knowledge.RegisterTools(reg, kb, gkb)

	taskStore := tasks.New(ws.Root) // tareas persistentes del directorio actual (siguen al /cd)
	tasks.RegisterTools(reg, taskStore)

	if cfg.Rag.Enabled && cfg.Rag.BaseURL != "" {
		rag.RegisterTool(reg, cfg.Rag.BaseURL, cfg.Rag.User, cfg.Rag.N)
	}

	// Script local library (~/.goolem/scripts/)
	scriptsDir := filepath.Join(cfg.Workdir, ".goolem", "scripts")
	globalScriptStore = scripts.New(scriptsDir)
	scripts.RegisterTools(reg, globalScriptStore)

	// Catálogo de modelos disponibles (DeepSeek/Claude si hay clave + modelos del
	// servidor Ollama). Lo comparten ask_model (consulta puntual) y /model (cambio
	// del cerebro activo en caliente).
	models, modelNames := ask.Build(cfg)
	ask.RegisterTool(reg, models, modelNames, tracker)

	// Verify configured model is available on Ollama server
	if cfg.Provider == "ollama" && len(modelNames) > 0 {
		found := false
		for _, name := range modelNames {
			if name == cfg.Model {
				found = true
				break
			}
		}
		if !found {
			fmt.Fprintf(os.Stderr, "⚠ Model %q not found on Ollama server. Available: %s\n",
				cfg.Model, strings.Join(modelNames, ", "))
			fmt.Fprintf(os.Stderr, "  Use /model <name> to switch, or update goolemcode.json.\n")
			fmt.Fprintf(os.Stderr, "  To pull missing model: ollama pull %s\n", cfg.Model)
		}
	}

	mgr := &mcp.Manager{}
	if len(cfg.MCPServers) > 0 {
		fmt.Println("Connecting to MCP servers…")
		mgr.ConnectAll(ctx, cfg.MCPServers, reg)
	}
	defer mgr.Shutdown()

	cp := checkpoint.New(cfg.Workdir)
	in := bufio.NewReader(os.Stdin)
	wd := interrupt.New(in) // coordinador del único lector de stdin (watchdog del turno vs. ask_user)

	// ask_user: el agente puede consultarte a mitad de tarea. Comparte el mismo
	// lector de stdin (las lecturas son secuenciales, nunca concurrentes). Si el
	// watchdog del turno está leyendo en modo raw, primero Release() lo detiene y
	// restaura el terminal, y luego Acquire() retoma la vigilia.
	askCancel := func() {} // contexto a cancelar al retomar la vigilia (se asigna por turno)

	// Smart routing: si el primario falla varias veces seguidas, preguntamos al
	// usuario si quiere pasar al secundario. Comparte el coordinador de stdin.
	if r, ok := prov.(*provider.SmartRouter); ok {
		r.SetConsecutiveFallback(cfg.SmartRouting.ConsecutiveFallback)
		r.SetAskUser(func(msg string) bool {
			wd.Release()
			fmt.Printf("\n🤔 %s\n[y/N] ", msg)
			line, err := in.ReadString('\n')
			ans := strings.TrimSpace(line)
			wd.Acquire(func() {})
			if err != nil {
				return false
			}
			return ans == "y" || ans == "Y" || ans == "s" || ans == "S" ||
				strings.EqualFold(ans, "si") || strings.EqualFold(ans, "sí")
		})
	}

	reg.Register(model.ToolDefinition{
		Name:        "ask_user",
		Description: "Ask the user a question and wait for their response. Use it ONLY when you are genuinely stuck and need a decision or piece of data you cannot figure out on your own.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question": map[string]any{"type": "string", "description": "The question for the user"},
			},
			"required": []string{"question"},
		},
		Mutating: false,
	}, func(_ context.Context, args map[string]any) (string, error) {
		q, _ := args["question"].(string)
		// Mientras el agente lee (runTurn), el watchdog es el lector en modo raw.
		// Liberamos stdin para poder leer la respuesta del usuario con Enter.
		release := wd.Release()
		fmt.Printf("\n🤔 %s\n» ", q)
		// ask_user requiere texto libre: lectura de línea normal con Enter.
		line, err := in.ReadString('\n')
		if err != nil {
			if release {
				wd.Acquire(askCancel)
			}
			return "", err
		}
		ans := strings.TrimSpace(line)
		if release {
			wd.Acquire(askCancel)
		}
		if ans == "" {
			return "(the user did not respond)", nil
		}
		return ans, nil
	})

	planMode := false             // modo plan: bloquea acciones mutadoras (toggle /plan)
	approved := map[string]bool{} // herramientas con "sí, no preguntar más" en esta sesión
	for _, t := range cfg.ApprovedTools {
		approved[t] = true
	}
	permit := func(toolName string, args map[string]any) bool {
		if planMode { // en modo plan nada muta, ni con --auto-approve
			b, _ := json.Marshal(args)
			fmt.Printf("⛔ plan mode: blocked %s(%s). Exit /plan to execute.\n", toolName, short(string(b)))
			return false
		}
		if cfg.AutoApprove || approved[toolName] {
			return true
		}
		// A mitad de turno el watchdog es el único lector de stdin (modo raw).
		// Hay que Release() antes de leer la tecla y Acquire() al terminar; en otro
		// caso la tecla la consume el watchdog y el menú nunca recibe la respuesta.
		released := wd.Release()
		previewChange(ws, toolName, args) // diff en color para write_file/edit_file
		b, _ := json.Marshal(args)
		allow := choosePermission(toolName, short(string(b)), in, released, askCancel, wd)
		if released {
			wd.Acquire(askCancel) // retoma la vigilia (no-op si el watchdog ya no estaba activo)
		}
		if allow == 2 { // "Always": no volver a preguntar por esta herramienta en la sesión
			approved[toolName] = true
			cfg.ApprovedTools = append(cfg.ApprovedTools, toolName)
			if err := cfg.SaveConfig(); err != nil {
				fmt.Fprintf(os.Stderr, "(warning: could not save approval: %v)\n", err)
			}
		}
		return allow != 0
	}

	ag := agent.New(prov, reg, cp, permit)
	if cfg.MaxContextTokens > 0 {
		ag.SetContextBudget(cfg.MaxContextTokens)
	}
	sess := session.New(cfg.Workdir) // conversación persistida (anclada al arranque)
	if cfg.Resume {
		if prev := sess.Load(); len(prev) > 0 {
			ag.Restore(prev)
			fmt.Printf("Session resumed: %d messages from %s\n", len(prev), sess.Path())
		} else {
			fmt.Println("No previous session to resume in this directory.")
		}
	}
	kbIndex := knowledge.CombinedIndex(kb, gkb)
	// Comandos slash personalizados (.goolem/commands/*.md), anclados al arranque.
	// Necesitamos cmdStore antes que contextFn porque este lo usa.
	cmdStore := commands.New(filepath.Join(cfg.Workdir, ".goolem", "commands"))
	contextFn := func() string { // contexto al system prompt cada turno
		parts := []string{
			"TODAY'S DATE: " + time.Now().Format("Monday 2006-01-02 15:04 MST"),
			"CURRENT WORKING DIRECTORY: " + ws.Root(),
		}
		if planMode {
			parts = append(parts, "PLAN MODE ACTIVE: DO NOT edit files or execute commands that change anything (they are blocked). Explore in read-only mode (read_file, grep, view_directory_tree, web_search) and respond with a clear step-by-step PLAN. Do not attempt to apply changes; the user will review the plan and exit plan mode (/plan) for you to execute it.")
		}
		if doc := projmem.Load(cfg.Workdir); doc != "" { // GOOLEM.md/CLAUDE.md del proyecto
			parts = append(parts, doc)
		}
		if tks := taskStore.OpenSummary(); tks != "" {
			parts = append(parts, tks)
		}
		if idx := kbIndex(); idx != "" {
			parts = append(parts, idx)
		}
		if sidx := globalScriptStore.Index(); sidx != "" {
			parts = append(parts, sidx)
		}
		// Perfil de usuario: preferencias aprendidas durante la sesión
		if profile, err := kb.Read("user-profile"); err == nil && profile != "" {
			parts = append(parts, "USER PROFILE (preferences learned over time; update with knowledge_write when you discover new patterns):\n"+profile)
		}
		// Comandos auto-invocables: el agente puede usarlos directamente
		if auto := cmdStore.AutoInvokeCommands(); len(auto) > 0 {
			var sb strings.Builder
			sb.WriteString("AVAILABLE AUTO-INVOCABLE COMMANDS:\n")
			for _, c := range auto {
				sb.WriteString("  - /" + c.Name)
				if c.Description != "" {
					sb.WriteString(": " + c.Description)
				}
				if c.Trigger != "" {
					sb.WriteString(" (trigger: \"" + c.Trigger + "\")")
				}
				sb.WriteString("\n")
			}
			sb.WriteString("You can invoke them automatically when appropriate based on their trigger or description.\n")
			parts = append(parts, sb.String())
		}
		return strings.Join(parts, "\n\n")
	}
	ag.SetContextProvider(contextFn)
	// spawn_agent: sub-agentes con su propio contexto (comparten tools/permisos).
	subagent.Register(reg, prov, cp, permit, tracker, contextFn)
	// Hooks Pre/PostToolUse (.goolem/hooks.json), aplican también a sub-agentes.
	hookRunner := hooks.Load(filepath.Join(cfg.Workdir, ".goolem", "hooks.json"), ws.Root)
	reg.SetHooks(hookRunner.PreTool, hookRunner.PostTool)
	if n := hookRunner.Count(); n > 0 {
		fmt.Printf("Hooks loaded: %d (.goolem/hooks.json)\n", n)
	}
	showThinking := cfg.ShowThinking // razonamiento del modelo (toggle con /think)
	// Editor de línea con historial (↑/↓) anclado al directorio de arranque.
	editor := lineedit.New(in, filepath.Join(cfg.Workdir, ".goolem", "history"))
	editor.SetCompleter(func(word string) []string { return completePath(ws, word) })
	banner(prov.Label(), ws.Root(), reg.Names(), mgr.Connected, cfg.AutoApprove)

	// Onboarding: preguntas de configuración al inicio de la sesión para
	// personalizar la interacción (como el /init de Claude pero para preferencias).
	// Devuelve alias de modelos (alias → nombre real) que se funden en el catálogo.
	if stdinIsTerminal() && !cfg.AutoApprove {
		aliases := runOnboarding(in, &planMode, &showThinking, models, modelNames)
		if len(aliases) > 0 {
			for alias, realName := range aliases {
				if p, ok := models[realName]; ok {
					models[alias] = p
					modelNames = append(modelNames, alias)
				}
			}
			// Re-registrar ask_model con los alias incluidos
			ask.RegisterTool(reg, models, modelNames, tracker)
			// Persistir alias en goolemcode.json
			cfg.ModelAliases = aliases
			if err := cfg.SaveConfig(); err != nil {
				fmt.Fprintf(os.Stderr, "(warning: could not save aliases: %v)\n", err)
			}
		}
	}

	for {
		for {
			if cfg.ShowStatusline && stdinIsTerminal() {
				branch, dirty := gitStatus(ws.Root())
				label := prov.Label()
				var savingsEstimate string
				var costTotalStr string
				// Desglose por modelo (si se usó más de uno); se muestra en la barra.
				var modelsBreakdown []statusline.ModelUsage
				{
					raw := tracker.RawByModel()
					if len(raw) > 1 {
						modelsBreakdown = make([]statusline.ModelUsage, 0, len(raw))
						for _, name := range tracker.Order() {
							row := raw[name]
							cost := 0.0
							if c, found := tracker.GetRates().Cost(name, row.InputTokens, row.OutputTokens); found {
								cost = c
							}
							modelsBreakdown = append(modelsBreakdown, statusline.ModelUsage{
								Label:        shortModelLabel(name),
								InputTokens:  row.InputTokens,
								OutputTokens: row.OutputTokens,
								Calls:        row.Calls,
								Cost:         cost,
							})
						}
					}
				}
				if router, ok := prov.(*provider.SmartRouter); ok {
					// Etiqueta del modelo que responderá/respondió. Antes de ningún
					// turno, ActiveLabel es "" (el router aún no ha decidido): mostramos
					// el primario marcado [P], que es el que más probablemente coja el
					// arranque (y evita el "↦ DeepSeek > Ollama · $0 · ahorro $0" confuso).
					active := router.ActiveLabel()
					switch {
					case active == "":
						label = router.PrimaryLabel() + " [P]"
					case router.IsPrimaryLabel(active):
						label = active + " [P]"
					case router.IsSecondaryLabel(active):
						label = active + " [S]"
					default:
						label = active
					}
					if raw := tracker.RawByModel(); len(raw) > 0 {
						pc, sc := router.CallCounts()
						_, actual, savings, _, _ := tracker.GetRates().SavingsReportFull(raw, router.PrimaryLabel(), pc, sc, router.SecondaryLabel())
						savingsEstimate = "$" + formatSavings(savings) // siempre, "0" si no hay
						costTotalStr = "$" + formatCost(actual)        // siempre, "0" si no hay
					} else {
						savingsEstimate = "$0"
						costTotalStr = "$0"
					}
				} else {
					// Sin Smart Router: mostrar coste total estimado
					totalCost := 0.0
					if raw := tracker.RawByModel(); len(raw) > 0 {
						for name, row := range raw {
							c, found := tracker.GetRates().Cost(name, row.InputTokens, row.OutputTokens)
							if found {
								totalCost += c
							}
						}
					}
					costTotalStr = "$" + formatCost(totalCost) // "0" si no hay
				}
				fmt.Println("\n" + statusline.Render(statusline.Info{
					ModelLabel: label, Dir: filepath.Base(ws.Root()),
					Branch: branch, Dirty: dirty, Usage: ag.SessionUsage(), Models: modelsBreakdown, PlanMode: planMode,
					ScriptRuns: globalScriptStore.Stats().Runs, ScriptSaved: globalScriptStore.Stats().Tokens,
					Savings:   savingsEstimate,
					CostTotal: costTotalStr,
				}))
			}
			tag := ""
			if planMode {
				tag = " [plan]"
			}
			line, err := editor.ReadLine(fmt.Sprintf("%s%s » ", filepath.Base(ws.Root()), tag))
			if err == lineedit.ErrInterrupted {
				continue // Ctrl-C: descarta la línea, prompt nuevo
			}
			if err != nil {
				fmt.Println("\nGoodbye")
				return
			}
			input := strings.TrimSpace(line)
			if input == "" {
				continue
			}
			if cmd, arg, isCmd := asCommand(input); isCmd {
				ranAsCommand := true
				switch cmd {
				case "exit", "salir", "quit":
					fmt.Println("Goodbye")
					return
				case "clear", "limpiar":
					ag.Clear()
					_ = sess.Clear()
					fmt.Println("Context, checkpoints, and saved session reset")
				case "rewind", "deshacer":
					if msg, ok := cp.Rewind(); ok {
						fmt.Println(msg)
					} else {
						fmt.Println("No checkpoints to undo")
					}
				case "cd":
					if arg == "" {
						fmt.Println("Usage: /cd <directory>")
						break
					}
					newRoot, err := ws.SetRoot(arg)
					if err != nil {
						fmt.Println(err)
						break
					}
					cp.SetRoot(newRoot) // los checkpoints siguen al nuevo directorio
					fmt.Println("Working directory: " + newRoot)
				case "pwd":
					fmt.Println(ws.Root())
				case "help", "ayuda":
					help()
				case "tools", "herramientas":
					fmt.Println("Tools: " + strings.Join(reg.Names(), ", "))
				case "tokens", "uso":
					if rep := tracker.Report(); rep != "" {
						fmt.Println(rep)
						// ¿Smart routing activo? Muestra desglose por modelo + ahorro estimado.
						if router, ok := prov.(*provider.SmartRouter); ok {
							pc, sc := router.CallCounts()
							pl, sl := router.PrimaryLabel(), router.SecondaryLabel()
							fmt.Printf("\n📡 Peticiones: %d al primario %s [P] · %d al secundario %s [S] (total %d)\n",
								pc, shortModelLabel(pl), sc, shortModelLabel(sl), pc+sc)
							if pc == 0 || sc == 0 {
								fmt.Printf("   ⚠️  Todo va a un solo modelo: %s podria no estar activandose.\n",
									shortModelLabel(pl))
							}
							if savings := tracker.ReportSavings(pl); savings != "" {
								fmt.Println()
								fmt.Println(savings)
							}
						}
					} else {
						fmt.Println("No tokens consumed yet in this session.")
					}
				case "commands", "comandos":
					if names := cmdStore.Names(); len(names) > 0 {
						var sb strings.Builder
						sb.WriteString("Custom commands:\n")
						for _, c := range cmdStore.List() {
							sb.WriteString("  /" + c.Name)
							if c.Description != "" {
								sb.WriteString(" — " + c.Description)
							}
							if c.AutoInvoke {
								sb.WriteString(" [auto]")
							}
							sb.WriteString("\n")
						}
						fmt.Print(sb.String())
					} else {
						fmt.Println("No custom commands (create .goolem/commands/<name>.md).")
					}
				case "think", "pensar":
					showThinking = !showThinking
					if showThinking {
						fmt.Println("Model reasoning: visible")
					} else {
						fmt.Println("Model reasoning: hidden")
					}
				case "init":
					input = initPrompt // se ejecuta como turno: el agente analiza y escribe GOOLEM.md
					ranAsCommand = false
				case "plan":
					planMode = !planMode
					if planMode {
						fmt.Println("Plan mode ACTIVE: read-only; the agent will propose a plan. Use /plan to execute it.")
					} else {
						fmt.Println("Plan mode disabled: the agent can edit/execute again (with confirmation).")
					}
				case "review", "revisar":
					// code review sobre el diff actual, usando un sub-agente. El agente
					// principal genera el review; se ejecuta como un turno normal.
					diffText, err := getGitDiff(ws.Root())
					if err != nil {
						fmt.Printf("Error al obtener diff: %v\n", err)
						break
					}
					if diffText == "" {
						fmt.Println("No uncommitted changes to review.")
						break
					}
					input = fmt.Sprintf(reviewPrompt, diffText)
					ranAsCommand = false
				case "model", "modelo":
					if arg == "" {
						fmt.Printf("Active model: %s\nAvailable: %s\nUsage: /model <name>\n", prov.Label(), strings.Join(modelNames, ", "))
						break
					}
					newProv, ok := models[arg]
					if !ok {
						fmt.Printf("Unknown model: %q. Available: %s\n", arg, strings.Join(modelNames, ", "))
						break
					}
					if router, isRouter := prov.(*provider.SmartRouter); isRouter {
						router.SetPrimary(newProv)
						// Persistir modelo primario para SmartRouter
						cfg.Model = arg
						if err := cfg.SaveConfig(); err != nil {
							fmt.Fprintf(os.Stderr, "(warning: could not save model: %v)\n", err)
						}
						fmt.Println("Primary changed: " + newProv.Label())
					} else {
						prov = newProv
						ag.SetProvider(newProv)
						cfg.Model = arg
						if err := cfg.SaveConfig(); err != nil {
							fmt.Fprintf(os.Stderr, "(warning: could not save model: %v)\n", err)
						}
						fmt.Println("Active model: " + prov.Label())
					}
				case "consenso", "consensus":
					runConsensus(cfg, in, prov, models, modelNames, arg)
				default:
					if cmdObj, ok := cmdStore.Get(cmd); ok {
						input = commands.Expand(cmdObj.Template, arg)
						ranAsCommand = false
					} else {
						fmt.Println("Unknown command (use /help)")
					}
				}
				if ranAsCommand {
					continue
				}
			}

			expanded, mentioned, imgNames, images := expandMentions(input, ws)
			for _, f := range mentioned {
				fmt.Printf("  @ included: %s\n", f)
			}
			for _, f := range imgNames {
				fmt.Printf("  @ image: %s\n", f)
			}
			runTurnInterruptible(ctx, wd, ag, expanded, images, tracker, prov.Label(), &showThinking, &askCancel)
			if err := sess.Save(ag.Snapshot()); err != nil {
				fmt.Fprintf(os.Stderr, "(warning: could not save session: %v)\n", err)
			}
		}
	}
}

// choosePermission muestra el menú de confirmación de permisos y devuelve la
// elección: 1 = Yes (once), 2 = Always (esta herramienta en esta sesión),
// 0 = No (por defecto si no se pulsa 1 o 2).
//
// Permite elegir con una sola pulsación (sin Enter) y también escribiendo el
// número seguido de Enter. El watchdog de interrupción del turno ya debe estar
// liberado (released=true): al terminar, el llamante hace wd.Acquire. Si released
// es true y el usuario no responde (EOF), se lanza el "timeout" de 15 s y se
// asume No.
func choosePermission(toolName, argsShort string, in *bufio.Reader, released bool, cancel context.CancelFunc, wd *interrupt.Watcher) int {
	fmt.Printf("⚠  %s(%s)\n   1) Yes (once)   2) Always   3) No\n   Choose [1/2/3] (default 3): ", toolName, argsShort)

	type res struct {
		r   rune
		err error
	}
	ch := make(chan res, 1)

	// Salida por timeout a los 15 s sin respuesta: evita quedarse colgado.
	if released {
		go func() {
			t := time.NewTimer(15 * time.Second)
			defer t.Stop()
			select {
			case <-t.C:
				_ = lineedit.SttySet("min", "0", "time", "0") // desbloquea ReadRune pendiente
				ch <- res{'\n', nil}
			case <-ch:
				return
			}
		}()
	}

	go func() {
		r, err := lineedit.ReadKey(in)
		ch <- res{r, err}
	}()

	got := <-ch
	fmt.Println() // nueva línea tras la tecla

	if got.err != nil {
		fmt.Println("(sin respuesta del usuario)")
		return 0
	}

	// Si el primer carácter es un dígito o vacío, lo damos por elegido; si no,
	// esperamos una línea normal (usuario que escribe algo fuera de rango, p. ej. "no").
	key := got.r
	if key == '\n' || key == '\r' || key == 0 || (key >= '1' && key <= '9') {
		return permChoiceFromRune(key)
	}
	// Reinyecta el carácter ya leído (no podemos devolverlo al reader): con él
	// reconstruimos la respuesta escribiendo una línea completa.
	rest, _ := in.ReadString('\n')
	full := strings.TrimSpace(string(key) + rest)
	if full == "" {
		return 0
	}
	return permChoiceFromRune(rune(full[0]))
}

// permChoiceFromRune traduce una tecla del menú de permisos a la elección
// (1, 2 o 0). '\n' (Enter a secas) y cualquier otra tecla → 0 (No).
func permChoiceFromRune(r rune) int {
	switch r {
	case '1':
		return 1
	case '2':
		return 2
	default:
		return 0
	}
}

// runTurnInterruptible lanza runTurn en una goroutine y permite cancelarla con
// ESC o Ctrl-C mientras corre. El watchdog (internal/interrupt) pone el terminal
// en modo raw durante el turno y, al pulsar ESC/Ctrl-C, cancela el contexto, lo
// que aborta las llamadas HTTP/SSE en curso aunque el proveedor esté atascado.
// wd es el coordinador de stdin compartido (lo usan también ask_user y las
// confirmaciones a mitad de turno, via Release/Acquire).
func runTurnInterruptible(ctx context.Context, wd *interrupt.Watcher, ag *agent.Agent, input string, images []model.ImageData, tracker *usage.Tracker, provLabel string, showThinking *bool, askCancel *func()) {
	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	done := make(chan struct{})
	go func() {
		runTurn(turnCtx, ag, input, images, tracker, provLabel, showThinking)
		close(done)
	}()

	sigCh, sigStop := interrupt.SignalCh()
	defer sigStop()

	*askCancel = cancel // para que ask_user retome la vigilia con la cancelación del turno
	started := wd.Start(cancel)

	select {
	case <-done:
		wd.Stop()
		if started {
			fmt.Println("\n[ok]")
		}
	case sig := <-sigCh:
		cancel()
		<-done
		wd.Stop()
		fmt.Printf("\n■ Interrupted (signal %v). Tell me what you want me to do differently.\n", sig)
	}
}

// previewChange muestra un diff en color de lo que hará write_file/edit_file
// antes de pedir confirmación (para otras herramientas no muestra nada).
func previewChange(ws *tools.Workspace, toolName string, args map[string]any) {
	rel, _ := args["path"].(string)
	if rel == "" {
		return
	}
	switch toolName {
	case "write_file":
		before := ""
		if p, err := ws.Resolve(rel); err == nil {
			if data, err := os.ReadFile(p); err == nil {
				before = string(data)
			}
		}
		after, _ := args["content"].(string)
		verb := "create"
		if before != "" {
			verb = "overwrite"
		}
		fmt.Printf("✎ %s %s:\n%s\n", verb, rel, diff.Render(before, after, 60))
	case "edit_file":
		old, _ := args["old_string"].(string)
		neu, _ := args["new_string"].(string)
		fmt.Printf("✎ editar %s:\n%s\n", rel, diff.Render(old, neu, 60))
	}
}

// completePath autocompleta una palabra como ruta dentro del workspace (para Tab).
// Soporta un prefijo @ (menciones) y añade "/" a los directorios. Devuelve los
// reemplazos completos de la palabra.
func completePath(ws *tools.Workspace, word string) []string {
	at := strings.HasPrefix(word, "@")
	p := strings.TrimPrefix(word, "@")
	dir, base := filepath.Split(p) // dir conserva la barra final ("" si no hay)

	absDir, err := ws.Resolve(dir)
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, base) {
			continue
		}
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(base, ".") {
			continue // oculta dotfiles salvo que se empiece a escribir el punto
		}
		full := dir + name
		if e.IsDir() {
			full += "/"
		}
		if at {
			full = "@" + full
		}
		out = append(out, full)
	}
	return out
}

// reMention detecta menciones @ruta: la @ va al inicio o tras un espacio, y la
// ruta son caracteres típicos de fichero (no espacios). Evita emails (a@b.com)
// al exigir que el carácter previo sea inicio o espacio.
var reMention = regexp.MustCompile(`(^|\s)@([A-Za-z0-9._~/\-]+)`)

const mentionMaxBytes = 100 * 1024

// imageMedia devuelve el tipo MIME si la ruta es una imagen soportada, o "".
func imageMedia(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	}
	return ""
}

// expandMentions procesa las menciones @ruta del mensaje. Los ficheros de texto
// se insertan como contenido; las imágenes se adjuntan (entrada multimodal). Solo
// resuelve ficheros dentro del sandbox; las @ruta que no resuelvan se dejan
// literales. Devuelve el mensaje aumentado, los ficheros de texto incluidos, los
// nombres de imágenes adjuntas y las imágenes en sí.
func expandMentions(input string, ws *tools.Workspace) (string, []string, []string, []model.ImageData) {
	matches := reMention.FindAllStringSubmatch(input, -1)
	if len(matches) == 0 {
		return input, nil, nil, nil
	}
	var blocks strings.Builder
	var included, imgNames []string
	var images []model.ImageData
	seen := map[string]bool{}
	for _, m := range matches {
		rel := m[2]
		if seen[rel] {
			continue
		}
		p, err := ws.Resolve(rel)
		if err != nil {
			continue
		}
		info, err := os.Stat(p)
		if err != nil || info.IsDir() {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		seen[rel] = true
		if media := imageMedia(rel); media != "" { // imagen → adjuntar, no inyectar texto
			images = append(images, model.ImageData{Media: media, Bytes: data})
			imgNames = append(imgNames, rel)
			continue
		}
		content := string(data)
		if len(content) > mentionMaxBytes {
			content = content[:mentionMaxBytes] + "\n… (truncado)"
		}
		fmt.Fprintf(&blocks, "\n\n--- Content of @%s ---\n%s", rel, content)
		included = append(included, rel)
	}
	return input + blocks.String(), included, imgNames, images
}

// asCommand separa un comando de su argumento. El comando se normaliza a
// minúsculas; el argumento conserva las mayúsculas (importa para rutas en /cd).
func asCommand(input string) (cmd, arg string, isCmd bool) {
	s := input
	slash := strings.HasPrefix(s, "/")
	if slash {
		s = strings.TrimPrefix(s, "/")
	}
	fields := strings.SplitN(s, " ", 2)
	word := strings.ToLower(strings.TrimSpace(fields[0]))
	if len(fields) == 2 {
		arg = strings.TrimSpace(fields[1])
	}
	if slash {
		return word, arg, true
	}
	switch word { // palabras sueltas (sin barra) también valen
	case "exit", "salir", "quit", "clear", "limpiar", "rewind", "deshacer",
		"help", "ayuda", "tools", "herramientas", "cd", "pwd", "tokens", "uso",
		"commands", "comandos", "think", "pensar", "init", "plan", "model", "modelo",
		"review", "revisar", "consenso", "consensus":
		return word, arg, true
	}
	return "", "", false
}

// initPrompt es la instrucción de /init: el agente explora el proyecto y escribe
// un GOOLEM.md que orientará a futuros agentes (como el /init de Claude Code).
const initPrompt = `Analyze this project and create a concise GOOLEM.md file in the root of the working directory.

First explore with view_directory_tree and read_file (key files: build manifests, README, configs). Then write GOOLEM.md with:
- What the project is (1-2 sentences) and its language/stack.
- How to build, test and run (concrete commands).
- Relevant directory structure (brief map).
- Important conventions (style, UI language, patterns) an agent should follow.

Be brief and concrete; do not invent what you cannot verify in the code. If GOOLEM.md already exists, update it instead of duplicating.`

const (
	ansiGreen = "\x1b[32m"
	ansiRed   = "\x1b[31m"
	ansiCyan  = "\x1b[36m"
	ansiDim   = "\x1b[2m"
	ansiReset = "\x1b[0m"
)

func runTurn(ctx context.Context, ag *agent.Agent, input string, images []model.ImageData, tracker *usage.Tracker, provLabel string, showThinking *bool) {
	fmt.Println("GoolemCode:")
	inThinking := false
	endThinking := func() {
		if inThinking {
			fmt.Print(ansiReset + "\n")
			inThinking = false
		}
	}
	h := agent.Hooks{
		OnThinking: func(s string) {
			if showThinking == nil || !*showThinking {
				return
			}
			if !inThinking {
				fmt.Print("\n" + ansiDim)
				inThinking = true
			}
			fmt.Print(s)
		},
		OnDelta: func(s string) {
			endThinking()
			fmt.Print(s)
		},
		OnToolCall: func(c model.ToolCall) {
			endThinking()
			b, _ := json.Marshal(c.Arguments)
			fmt.Printf("\n%s→ %s(%s)%s\n", ansiCyan, c.Name, short(string(b)), ansiReset)
		},
		OnToolResult: func(r model.ToolResult) {
			tag := "✓"
			color := ansiGreen
			if r.IsError {
				tag = "✗"
				color = ansiRed
			}
			fmt.Printf("  %s%s %s%s\n", color, tag, short(strings.ReplaceAll(r.Content, "\n", " ")), ansiReset)
		},
	}
	_, err := ag.RunWithImages(ctx, input, images, h)
	endThinking()
	if err != nil && ctx.Err() == nil {
		fmt.Printf("\n%sError: %v%s\n", ansiRed, err, ansiReset)
	}
	if u := ag.TurnUsage(); u.InputTokens > 0 || u.OutputTokens > 0 {
		activeLabel := provLabel
		if p := ag.Provider(); p != nil {
			if router, ok := p.(*provider.SmartRouter); ok {
				activeLabel = router.ActiveLabel()
			}
		}
		tracker.Add(activeLabel, u)
		if st := globalScriptStore.Stats(); st.Runs > 0 {
			fmt.Printf("\n[%s · turno %d↑ %d↓ · 📜%d -%s]\n",
				activeLabel, u.InputTokens, u.OutputTokens, st.Runs, statusline.Human(st.Tokens))
		} else {
			fmt.Printf("\n[%s · turno %d↑ %d↓]\n",
				activeLabel, u.InputTokens, u.OutputTokens)
		}
	}
	fmt.Println()
}

// reviewPrompt es la instrucción para /review: el agente revisa un diff unificado.
const reviewPrompt = `Perform a code review of the following unified diff (git diff).

Evaluate:
1. **Correctness**: are there logical bugs or errors?
2. **Security**: data leaks, injection, unsafe commands?
3. **Performance**: unnecessary operations, inefficient loops?
4. **Readability**: confusing names, overly dense code?
5. **Style**: does it follow project conventions?

For each issue found, indicate:
- File and line (if applicable)
- Severity: 🐛 critical | ⚠️ important | 💡 suggestion
- Brief explanation
- Suggested fix (optional code)

If there are no notable issues, kindly state so.

Diff to review:
%s`

// getGitDiff devuelve el diff unificado de los cambios no commiteados (staged + unstaged).
func getGitDiff(root string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", root, "diff", "--unified=8").Output()
	if err != nil {
		// intentar con diff HEAD (incluye cambios staged)
		out2, err2 := exec.CommandContext(ctx, "git", "-C", root, "diff", "HEAD", "--unified=8").Output()
		if err2 != nil {
			return "", fmt.Errorf("git diff: %v / git diff HEAD: %v", err, err2)
		}
		return string(out2), nil
	}
	return string(out), nil
}

// gitStatus devuelve la rama actual y si hay cambios sin commitear (vacío si el
// directorio no es un repo git o git no está disponible). Una sola llamada.
func gitStatus(root string) (branch string, dirty bool) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", root, "status", "--porcelain=v1", "-b").Output()
	if err != nil {
		return "", false
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) == 0 {
		return "", false
	}
	if first := lines[0]; strings.HasPrefix(first, "## ") {
		b := strings.TrimPrefix(first, "## ")
		if i := strings.Index(b, "..."); i >= 0 { // "main...origin/main" → "main"
			b = b[:i]
		}
		branch = b
	}
	dirty = len(lines) > 1
	return branch, dirty
}

// stdinIsTerminal indica si la entrada es un terminal (no una tubería): el
// statusline solo tiene sentido en modo interactivo.
func stdinIsTerminal() bool {
	stat, err := os.Stdin.Stat()
	return err == nil && stat.Mode()&os.ModeCharDevice != 0
}

func short(s string) string {
	const limit = 140
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}

// runConsensus abre el modo consenso: selecciona varios LLM de los configurados
// (DeepSeek/Claude si hay clave, y los modelos del servidor Ollama, que pueden
// descargarse con /consenso pull <modelo>), lanza la pregunta a todos en paralelo
// y usa el modelo principal como juez para decidir si llegan a la misma conclusión.
func runConsensus(cfg config.Config, in *bufio.Reader, mainProv provider.Provider, models map[string]provider.Provider, modelNames []string, arg string) {
	// Sub-comando pull: descargar un modelo en el servidor Ollama vía su API
	if strings.HasPrefix(strings.ToLower(arg), "pull ") {
		target := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(arg, "pull "), "pull ")[:])
		target = strings.TrimSpace(target)
		if fields := strings.Fields(target); len(fields) > 0 {
			target = fields[0]
		}
		runOllamaPull(cfg.OllamaURL, target)
		return
	}

	entries := make([]consensus.Entry, 0, len(modelNames))
	for _, name := range modelNames {
		if p, ok := models[name]; ok {
			entries = append(entries, consensus.NewEntry(name, p))
		}
	}
	if len(entries) < 2 {
		fmt.Println("Consenso necesita al menos 2 LLM disponibles. Actualmente hay: " + strings.Join(modelNames, ", "))
		fmt.Println("  Usa /consenso pull <modelo> para descargar un modelo en Ollama.")
		fmt.Println("  O asegúrate de tener DEEPSEEK_API_KEY / ANTHROPIC_API_KEY en el entorno.")
		return
	}

	selected := selectConsensusEntries(in, entries)
	if len(selected) < 2 {
		fmt.Println("  ❌ Consenso cancelado o selección insuficiente.")
		return
	}

	fmt.Printf("\nPregunta para el consenso (Enter al terminar): ")
	question := readLine(in)
	if strings.TrimSpace(question) == "" {
		fmt.Println("  ❌ Pregunta vacía.")
		return
	}

	// Lanzar la pregunta a todos los modelos seleccionados en paralelo (ronda 0)
	fmt.Printf("\nConsultando %d modelos (ronda 1/2)…\n", len(selected))
	history := consensus.Debate(context.Background(), selected, question, 2)

	// Mostrar respuestas de la ronda inicial
	initial := history[0].Answers
	border := strings.Repeat("-", 50)
	for _, a := range initial {
		fmt.Printf("\n%s\n📣 %s [%s]\n%s\n", border, a.Label, a.Origin, border)
		if a.Err != nil {
			fmt.Printf("  ⚠ error: %v\n", a.Err)
		} else {
			fmt.Println(a.Content)
		}
	}

	// Rondas de debate: compartir las respuestas entre todos y revisar
	if len(history) > 1 {
		for _, round := range history[1:] {
			fmt.Printf("\n=== 🔄 Ronda %d de debate: revisión tras ver las respuestas de los demás ===\n", round.Round+1)
			for _, a := range round.Answers {
				fmt.Printf("\n%s\n🔄 %s [%s]\n%s\n", border, a.Label, a.Origin, border)
				if a.Err != nil {
					fmt.Printf("  ⚠ error: %v\n", a.Err)
				} else {
					fmt.Println(a.Content)
				}
			}
		}
	}

	// El juez (modelo principal del agente) resume el consenso de la ronda final.
	// Tras el debate los modelos ya compartieron posturas, así que el juez solo
	// sintetiza el resultado converge; no decide unilateralmente.
	final := history[len(history)-1].Answers
	fmt.Printf("\n=== ⚖️  El modelo juez (%s) resume el consenso tras el debate ===\n", mainProv.Label())
	verdict, err := consensus.Judge(context.Background(), mainProv, question, final)
	if err != nil {
		fmt.Printf("  ⚠ el juez falló: %v\n", err)
		return
	}
	fmt.Println(verdict)
}

// runOllamaPull descarga un modelo en el servidor Ollama vía POST /api/pull.
// El streaming devuelve líneas JSON por el progreso; mostramos el estado final.
func runOllamaPull(baseURL, model string) {
	if model == "" {
		fmt.Println("Uso: /consenso pull <modelo>   (p. ej. /consenso pull llama3.2)")
		return
	}
	url := strings.TrimRight(baseURL, "/") + "/api/pull"
	body, _ := json.Marshal(map[string]string{"model": model})
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		fmt.Printf("  ❌ %v\n", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("  ❌ no se pudo contactar Ollama en %s: %v\n", baseURL, err)
		return
	}
	defer resp.Body.Close()
	// Escaneamos el stream para mostrar el estado (porcentaje) mientras avanza
	fmt.Printf("  Descargando %q desde Ollama…\n", model)
	var last string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var chunk struct {
			Status    string `json:"status"`
			Completed int64  `json:"completed"`
			Total     int64  `json:"total"`
			Error     string `json:"error"`
		}
		if json.Unmarshal([]byte(line), &chunk) != nil {
			continue
		}
		if chunk.Error != "" {
			fmt.Printf("  ❌ pull error: %s\n", chunk.Error)
			return
		}
		if chunk.Total > 0 {
			pct := float64(chunk.Completed) / float64(chunk.Total) * 100
			status := chunk.Status
			if status == "" {
				status = "descargando"
			}
			last = fmt.Sprintf("\r  %s %.0f%%", status, pct)
			fmt.Print(last)
		} else if chunk.Status != "" {
			fmt.Printf("\r  %s   \n", chunk.Status)
		}
	}
	fmt.Print("\r  ✔ Descarga completada                           \n")
}

// selectConsensusEntries imprime el catálogo y deja elegir los modelos (mínimo 2).
func selectConsensusEntries(in *bufio.Reader, entries []consensus.Entry) []consensus.Entry {
	fmt.Println("\n=== 🧠 MODO CONSENSO ===")
	fmt.Println("Modelos disponibles [origen]:")
	for i, e := range entries {
		fmt.Printf("  %2d)  %s  [%s]\n", i+1, e.Name, e.Origin)
	}
	fmt.Printf("\nSelecciona los modelos (números separados por comas/espacios, p. ej. \"1 3 4 5\"), mínimo 2: ")
	sel := readLine(in)
	indices, err := parseSelection(sel, len(entries))
	if err != nil {
		fmt.Printf("  ❌ %v\n", err)
		return nil
	}
	chosen := make([]consensus.Entry, 0, len(indices))
	for _, i := range indices {
		chosen = append(chosen, entries[i])
	}
	return chosen
}
func parseSelection(s string, total int) ([]int, error) {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' })
	var out []int
	seen := map[int]bool{}
	for _, p := range parts {
		if p == "" {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(p, "%d", &n); err != nil {
			return nil, fmt.Errorf("selección inválida: %q no es un número", p)
		}
		if n < 1 || n > total {
			return nil, fmt.Errorf("selección inválida: %d fuera de rango (1-%d)", n, total)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n-1)
		}
	}
	if len(out) < 2 {
		return nil, fmt.Errorf("selecciona al menos 2 modelos distintos")
	}
	if len(out) > 8 {
		return nil, fmt.Errorf("demasiados modelos (máximo 8)")
	}
	return out, nil
}

// runOnboarding hace una serie de preguntas al usuario al inicio de la sesión
// para configurar preferencias de interacción (como el init de Claude Code).
// Se ejecuta solo en terminal interactiva y cuando no está --auto-approve.
// Devuelve un mapa de alias para modelos (alias → nombre real), que se añade al
// catálogo de modelos disponibles para /model y ask_model.
func runOnboarding(in *bufio.Reader, planMode *bool, showThinking *bool, models map[string]provider.Provider, modelNames []string) map[string]string {
	aliases := map[string]string{}

	fmt.Println("\n--- ⚙️  Quick setup ---")
	fmt.Println("(press Enter to accept the default value)")

	// Pregunta 1: ¿modo plan al empezar?
	fmt.Print("\nStart in plan mode? (y/N): ")
	if ans := readLine(in); strings.EqualFold(ans, "s") || strings.EqualFold(ans, "si") || strings.EqualFold(ans, "y") || strings.EqualFold(ans, "yes") {
		*planMode = true
		fmt.Println("  → Plan mode ACTIVE. Use /plan to disable.")
	}

	// Pregunta 2: ¿mostrar thinking?
	fmt.Print("Show model reasoning (thinking)? (Y/n): ")
	if ans := readLine(in); strings.EqualFold(ans, "n") || strings.EqualFold(ans, "no") {
		*showThinking = false
		fmt.Println("  → Thinking hidden. Use /think to show.")
	} else {
		*showThinking = true
		fmt.Println("  → Thinking visible.")
	}

	// Pregunta 3: alias de modelos
	if len(modelNames) > 0 {
		fmt.Print("\n📛 Model aliases (optional):\n")
		fmt.Printf("   Available models: %s\n", strings.Join(modelNames, ", "))
		fmt.Println("   You can define a short alias for each model, e.g. \"goolem\" for \"gemma3:27b-q4-16k\".")
		fmt.Println("   Then use the alias in /model and ask_model.")
		fmt.Println("   (Leave empty and press Enter to skip this question)")
		fmt.Print("Do you want to define aliases? (y/N): ")
		if ans := readLine(in); strings.EqualFold(ans, "s") || strings.EqualFold(ans, "si") || strings.EqualFold(ans, "y") || strings.EqualFold(ans, "yes") {
			for _, name := range modelNames {
				fmt.Printf("  Alias for %q (Enter = no alias): ", name)
				alias := readLine(in)
				if alias != "" && alias != name {
					aliases[alias] = name
					fmt.Printf("    → ✅ \"%s\" points to %s\n", alias, name)
				}
			}
			if len(aliases) > 0 {
				fmt.Printf("   Aliases defined: %v\n", aliases)
			} else {
				fmt.Println("   (no aliases defined)")
			}
		}
	}

	fmt.Println("--- Setup complete ---")
	return aliases
}

// readLine lee una línea de stdin y devuelve el texto recortado.
func readLine(in *bufio.Reader) string {
	line, err := in.ReadString('\n')
	if err != nil {
		return ""
	}
	return strings.TrimSpace(line)
}

func banner(label, root string, names, mcpNames []string, auto bool) {
	fmt.Println("GoolemCode")
	fmt.Printf("Provider: %s\n", label)
	fmt.Printf("Directory: %s\n", root)
	fmt.Printf("Tools: %d\n", len(names))
	if len(mcpNames) > 0 {
		fmt.Printf("MCP: %s\n", strings.Join(mcpNames, ", "))
	}
	if auto {
		fmt.Println("--auto-approve ACTIVE: no confirmation will be requested.")
	}
	fmt.Println("Type /help for commands, /exit to quit.")
}

func help() {
	fmt.Println(`Commands:
  /help        Shows this help
  /cd <dir>    Changes the working directory (manage another folder)
  /pwd         Shows the current working directory
  /clear       Resets context and checkpoints
  /rewind      Undoes the last agent checkpoint changes
  /tools       Lists available tools
  /tokens      Shows token consumption per model (session)
  /commands    Lists custom commands (.goolem/commands/*.md)
  /think       Shows or hides model reasoning (qwen3…)
  /init        Analyzes the project and generates a GOOLEM.md
  /plan        Plan mode: read-only explore and propose a plan (without touching anything)
  /model [n]   Hot-swaps the active model (no arg: lists available models)
  /consenso    Consensus mode: ask several LLMs the same question and a judge
               determines if they reach the same conclusion. /consenso pull <m>
               downloads a model on the Ollama server.
  /exit        Quits

Ask another model without changing the primary: ask the agent
("ask deepseek: …") and it will use the ask_model tool.
	
Mention a file with @path to include its content in the message.
Create your own commands in .goolem/commands/<name>.md (use $ARGUMENTS).

The working directory is mobile: /cd takes you to another folder to manage it,
while the conversation and .md memory remain anchored where you started.

The conversation is saved automatically; start with -resume to continue it.
Use ↑/↓ to navigate message history (←/→ and backspace to edit).

Security: before writing files or executing commands, confirmation is requested
(1/2/3), unless you start with --auto-approve. Use /rewind if something breaks.`)
}

// formatSavings muestra el ahorro en formato legible (p. ej. "$0.05" o "$1.23").
func formatSavings(savings float64) string {
	switch {
	case savings >= 1.0:
		return fmt.Sprintf("%.2f", savings)
	case savings >= 0.01:
		return fmt.Sprintf("%.3f", savings)
	case savings > 0:
		return fmt.Sprintf("%.4f", savings)
	}
	return "0"
}

// formatCost muestra el coste en formato legible (mismo formato que formatSavings).
func formatCost(cost float64) string {
	return formatSavings(cost)
}

// shortModelLabel extrae el nombre del modelo de etiquetas tipo "DeepSeek (deepseek-chat)".
func shortModelLabel(label string) string {
	if i := strings.Index(label, "("); i >= 0 {
		j := strings.Index(label[i:], ")")
		if j > 0 {
			return label[i+1 : i+j]
		}
	}
	return label
}
