// Package hooks ejecuta comandos de usuario en eventos del ciclo de una
// herramienta (al estilo de los hooks de Claude Code). Se configuran en
// <workdir>/.goolem/hooks.json:
//
//	{"hooks":[
//	  {"event":"PostToolUse","matcher":"write_file|edit_file","command":"gofmt -w \"$GOOLEM_TOOL_PATH\""},
//	  {"event":"PreToolUse","matcher":"execute_command","command":"…"}
//	]}
//
// PreToolUse corre ANTES de la herramienta; si su comando sale con código != 0,
// BLOQUEA la ejecución (su salida es el motivo). PostToolUse corre DESPUÉS (para
// efectos secundarios: formatear, notificar…); su código se ignora. El comando
// recibe el contexto por variables de entorno (GOOLEM_TOOL_NAME, GOOLEM_TOOL_ARGS,
// GOOLEM_TOOL_PATH, GOOLEM_EVENT, y en Post GOOLEM_TOOL_RESULT/GOOLEM_TOOL_IS_ERROR)
// y se ejecuta en el directorio de trabajo vigente.
package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const (
	EventPre    = "PreToolUse"
	EventPost   = "PostToolUse"
	hookTimeout = 30 * time.Second
	maxHookOut  = 4000
)

type spec struct {
	Event   string `json:"event"`
	Matcher string `json:"matcher"`
	Command string `json:"command"`
}

type compiled struct {
	spec
	re *regexp.Regexp // nil => aplica a todas las herramientas
}

// Runner ejecuta los hooks configurados. root devuelve el directorio de trabajo
// vigente (donde se ejecutan los comandos).
type Runner struct {
	pre  []compiled
	post []compiled
	root func() string
}

// Load lee los hooks de path (JSON). Devuelve un Runner vacío si no existe o es
// inválido (con un aviso por stderr en ese último caso).
func Load(path string, root func() string) *Runner {
	r := &Runner{root: root}
	data, err := os.ReadFile(path)
	if err != nil {
		return r
	}
	var doc struct {
		Hooks []spec `json:"hooks"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		fmt.Fprintf(os.Stderr, "(aviso: %s inválido: %v)\n", path, err)
		return r
	}
	for _, s := range doc.Hooks {
		c := compiled{spec: s}
		if strings.TrimSpace(s.Matcher) != "" {
			re, err := regexp.Compile(s.Matcher)
			if err != nil {
				fmt.Fprintf(os.Stderr, "(aviso: matcher de hook inválido %q: %v)\n", s.Matcher, err)
				continue
			}
			c.re = re
		}
		switch s.Event {
		case EventPre:
			r.pre = append(r.pre, c)
		case EventPost:
			r.post = append(r.post, c)
		default:
			fmt.Fprintf(os.Stderr, "(aviso: evento de hook desconocido %q)\n", s.Event)
		}
	}
	return r
}

// Count devuelve cuántos hooks hay cargados (para el banner).
func (r *Runner) Count() int { return len(r.pre) + len(r.post) }

// PreTool ejecuta los hooks PreToolUse que casen. Si alguno sale != 0, bloquea
// (devuelve block=true y el motivo).
func (r *Runner) PreTool(name string, args map[string]any) (bool, string) {
	for _, h := range r.pre {
		if h.re != nil && !h.re.MatchString(name) {
			continue
		}
		if code, out := r.exec(h, EventPre, name, args, "", false); code != 0 {
			reason := strings.TrimSpace(out)
			if reason == "" {
				reason = fmt.Sprintf("hook '%s' salió con código %d", h.Command, code)
			}
			return true, reason
		}
	}
	return false, ""
}

// PostTool ejecuta los hooks PostToolUse que casen (efectos secundarios). Imprime
// un aviso conciso si un hook falla o produce salida.
func (r *Runner) PostTool(name string, args map[string]any, result string, isErr bool) {
	for _, h := range r.post {
		if h.re != nil && !h.re.MatchString(name) {
			continue
		}
		code, out := r.exec(h, EventPost, name, args, result, isErr)
		if out = strings.TrimSpace(out); out != "" {
			fmt.Printf("  ⚙ hook: %s\n", firstLine(out))
		} else if code != 0 {
			fmt.Printf("  ⚙ hook '%s' salió con código %d\n", h.Command, code)
		}
	}
}

func (r *Runner) exec(h compiled, event, name string, args map[string]any, result string, isErr bool) (int, string) {
	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", h.Command)
	if r.root != nil {
		cmd.Dir = r.root()
	}
	argsJSON, _ := json.Marshal(args)
	path, _ := args["path"].(string)
	env := append(os.Environ(),
		"GOOLEM_EVENT="+event,
		"GOOLEM_TOOL_NAME="+name,
		"GOOLEM_TOOL_ARGS="+string(argsJSON),
		"GOOLEM_TOOL_PATH="+path,
	)
	if event == EventPost {
		if len(result) > maxHookOut {
			result = result[:maxHookOut]
		}
		env = append(env, "GOOLEM_TOOL_RESULT="+result, "GOOLEM_TOOL_IS_ERROR="+boolStr(isErr))
	}
	cmd.Env = env

	out, err := cmd.CombinedOutput()
	if len(out) > maxHookOut {
		out = out[:maxHookOut]
	}
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		return 1, "no se pudo ejecutar el hook: " + err.Error()
	}
	return code, string(out)
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}
