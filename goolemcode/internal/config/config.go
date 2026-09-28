// Package config carga la configuración: flags + variables de entorno + un
// archivo opcional goolemcode.json (para servidores MCP y ajustes). Sin TOML
// para mantener cero dependencias.
package config

import (
	"bufio"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"

	"github.com/GOOLEMLABS/goolemcode/internal/mcp"
	"github.com/GOOLEMLABS/goolemcode/internal/securefs"
)

type Config struct {
	Provider           string             `json:"provider"`
	Model              string             `json:"model"`
	OllamaURL          string             `json:"ollama_url"`
	MaxTokens          int                `json:"max_tokens"`
	Effort             string             `json:"effort"`
	MCPServers         []mcp.ServerSpec   `json:"mcp_servers"`
	KnowledgeDir       string             `json:"knowledge_dir"`        // "" => <workdir>/.goolem/knowledge
	GlobalKnowledgeDir string             `json:"global_knowledge_dir"` // "" => ~/.goolem/knowledge
	Rag                RagConfig          `json:"rag"`
	DeepSeekModel      string             `json:"deepseek_model"`     // "" => deepseek-v4-pro
	MaxContextTokens   int                `json:"max_context_tokens"` // 0 => valor por defecto del agente
	ShowThinking       bool               `json:"show_thinking"`      // mostrar el razonamiento del modelo (qwen3…)
	ShowStatusline     bool               `json:"show_statusline"`    // línea de estado sobre el prompt
	ModelAliases       map[string]string  `json:"model_aliases"`      // alias -> real name
	ApprovedTools      []string           `json:"approved_tools"`     // herramientas siempre permitidas
	PlanMode           string             `json:"plan_mode"`          // "safe" | "fast" (decisión de inicio)
	OnboardingDone     bool               `json:"onboarding_done"`    // si ya pasó por el onboarding
	SmartRouting       SmartRoutingConfig `json:"smart_routing"`

	Debug bool `json:"debug"` // modo depuración: log en .goolem/debug.log + volcado de goroutines con SIGUSR1

	Workdir     string `json:"-"`
	AutoApprove bool   `json:"-"`
	Resume      bool   `json:"-"`
}

// RagConfig configura la herramienta rag_search (goolem-rag). Apagada por
// defecto: es infraestructura propia del usuario, se activa vía goolemcode.json.
type RagConfig struct {
	Enabled bool   `json:"enabled"`
	BaseURL string `json:"base_url"`
	User    string `json:"user"`
	N       int    `json:"n"`
}

// SmartRoutingConfig configura el ruteo inteligente entre dos modelos.
// El router elige automáticamente qué modelo usar según la complejidad de la
// tarea y la capacidad relativa de los modelos.
type SmartRoutingConfig struct {
	Enabled             bool   `json:"enabled"`
	SecondaryProvider   string `json:"secondary_provider"`   // "" = mismo proveedor que el primario
	SecondaryModel      string `json:"secondary_model"`      // obligatorio si enabled=true
	SecondaryOllamaURL  string `json:"secondary_ollama_url"` // "" = misma URL que el primario
	PrimaryMoreCapable  bool   `json:"primary_more_capable"` // true = primario más capaz (le tocan tareas complejas)
	ConsecutiveFallback int    `json:"consecutive_fallback"` // 0 (por defecto 3) = preguntar al usuario tras N fallos seguidos del primario
}

func Load() Config {
	// Flags primero: el workdir decide dónde buscar la config y el .env.
	providerFlag := flag.String("provider", "", "claude | ollama")
	modelFlag := flag.String("model", "", "modelo a usar")
	workdirFlag := flag.String("workdir", "", "directorio de trabajo (por defecto el actual)")
	auto := flag.Bool("auto-approve", false, "no pedir confirmación antes de escribir/ejecutar")
	resume := flag.Bool("resume", false, "reanudar la conversación guardada del directorio de trabajo")
	debugFlag := flag.Bool("debug", false, "modo depuración: registra hitos en .goolem/debug.log y vuelca goroutines con SIGUSR1")
	flag.Parse()

	workdir := *workdirFlag
	if workdir == "" {
		workdir, _ = os.Getwd()
	}
	workdir, _ = filepath.Abs(workdir)

	// .env: primero el del workdir, luego el del directorio actual (sin sobrescribir el entorno real).
	loadDotEnv(filepath.Join(workdir, ".env"))
	loadDotEnv(".env")

	cfg := Config{
		Provider:       "ollama",
		OllamaURL:      "http://localhost:11434",
		MaxTokens:      16000,
		Effort:         "high",
		ShowThinking:   true, // por defecto sí; el JSON puede ponerlo en false
		ShowStatusline: true,
		// Smart routing activo por defecto: enruta las tareas simples al modelo
		// secundario (más barato) y las complejas al primario. Si no quieres
		// usarlo, pon "smart_routing": { "enabled": false } en goolemcode.json.
		SmartRouting: SmartRoutingConfig{
			Enabled:             true,
			ConsecutiveFallback: 3,
		},
	}
	// goolemcode.json con precedencia: workdir → directorio actual → ~/.config/goolemcode.
	// Gana el primero que exista.
	if !loadConfigFile(filepath.Join(workdir, "goolemcode.json"), &cfg) {
		if !loadConfigFile("goolemcode.json", &cfg) {
			if home, err := os.UserHomeDir(); err == nil {
				loadConfigFile(filepath.Join(home, ".config", "goolemcode", "goolemcode.json"), &cfg)
			}
		}
	}

	cfg.Workdir = workdir
	cfg.AutoApprove = *auto
	cfg.Resume = *resume
	cfg.Debug = cfg.Debug || *debugFlag // el flag activa; el JSON puede dejarlo activo por defecto
	providerOverridden := *providerFlag != ""
	if providerOverridden {
		cfg.Provider = *providerFlag
	}
	if *modelFlag != "" {
		cfg.Model = *modelFlag
	}

	if v := os.Getenv("OLLAMA_HOST"); v != "" {
		cfg.OllamaURL = v
	}

	if cfg.KnowledgeDir == "" {
		cfg.KnowledgeDir = filepath.Join(cfg.Workdir, ".goolem", "knowledge")
	}
	if cfg.GlobalKnowledgeDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cfg.GlobalKnowledgeDir = filepath.Join(home, ".goolem", "knowledge")
		}
	}

	// Si el proveedor se cambió por flag pero el modelo no, usar el modelo por
	// defecto del nuevo proveedor (ignora lo que diga el config file para model).
	if cfg.Model == "" || (providerOverridden && *modelFlag == "") {
		switch cfg.Provider {
		case "claude":
			cfg.Model = "claude-opus-4-8"
		case "deepseek":
			cfg.Model = "deepseek-v4-pro"
		default:
			cfg.Model = "gemma4:31b"
		}
	}
	if cfg.DeepSeekModel == "" {
		cfg.DeepSeekModel = "deepseek-v4-pro"
	}

	// El secundario puede quedar sin especificar en configs que simplemente no lo
	// tocan; ponemos un modelo por defecto razonable según el proveedor para no
	// romper el arranque cuando el smart routing está activo por defecto.
	if cfg.SmartRouting.Enabled && cfg.SmartRouting.SecondaryModel == "" {
		switch cfg.Provider {
		case "claude":
			cfg.SmartRouting.SecondaryModel = "claude-haiku-4-5"
		case "deepseek":
			cfg.SmartRouting.SecondaryModel = "deepseek-v4-flash"
		default:
			cfg.SmartRouting.SecondaryModel = "gemma4:31b"
		}
	}
	return cfg
}

// SaveConfig serializa la configuración de vuelta al goolemcode.json del workdir.
// Conserva JSON existente (solo sobreescribe campos conocidos). Crea el fichero
// si no existe.
func (cfg *Config) SaveConfig() error {
	path := filepath.Join(cfg.Workdir, "goolemcode.json")
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// 0600: la config local puede incluir cabeceras de MCP con tokens.
	return securefs.WriteFile(path, data)
}

// loadConfigFile aplica un goolemcode.json sobre cfg. Devuelve true si lo leyó.
func loadConfigFile(path string, cfg *Config) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return json.Unmarshal(data, cfg) == nil
}

// loadDotEnv carga pares KEY=VALUE de un archivo .env al entorno. Ignora líneas
// vacías y comentarios (#), admite el prefijo "export " y comillas alrededor del
// valor, y NO sobrescribe variables ya definidas en el entorno real.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if n := len(val); n >= 2 && (val[0] == '"' && val[n-1] == '"' || val[0] == '\'' && val[n-1] == '\'') {
			val = val[1 : n-1]
		}
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue // el entorno real tiene prioridad
		}
		_ = os.Setenv(key, val)
	}
}
