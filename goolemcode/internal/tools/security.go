// Package tools: seguridad. Batería de herramientas read-only para revisar
// problemas de seguridad en el proyecto y en hosts. No ejecuta nada mutador:
// se limita a detectar y reportar (algunos comandos externos como netstat/lsof
// son de solo lectura).
package tools

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
)

// ---------------------------------------------------------------------------
// Patrones de "secrets" muy comunes. Cada uno tiene nombre legible.
// ---------------------------------------------------------------------------
type secPattern struct {
	name string
	re   *regexp.Regexp
	cat  string
}

func newSecPattern(cat, name, pattern string) secPattern {
	return secPattern{cat: cat, name: name, re: regexp.MustCompile(pattern)}
}

var securityPatterns = []secPattern{
	newSecPattern("aws", "AWS Access Key", `\b((?i)AKIA[0-9A-Z]{16})\b`),
	newSecPattern("aws", "AWS Secret Access Key", `(?i)\b(sa?ccess[_-]?key)?\s*[:=]\s*['"]?([A-Za-z0-9/+=]{40})\b`),
	newSecPattern("google", "Google API Key", `\b(AIza[0-9A-Za-z\-_]{35})\b`),
	newSecPattern("github", "GitHub Personal Token", `\b(ghp_[0-9A-Za-z]{36})\b`),
	newSecPattern("github", "GitHub OAuth Token", `\b(gho_[0-9A-Za-z]{36})\b`),
	newSecPattern("gitlab", "GitLab PAT", `\b(glpat-[0-9A-Za-z\-_]{20,})\b`),
	newSecPattern("slack", "Slack Token", `\b(xox[baprs]-[0-9A-Za-z\-]{10,60})\b`),
	newSecPattern("stripe", "Stripe Secret Key", `\b(sk_live_[0-9a-zA-Z]{24,})\b`),
	newSecPattern("stripe", "Stripe Publishable Key", `\b(pk_live_[0-9a-zA-Z]{24,})\b`),
	newSecPattern("twilio", "Twilio API Key", `\b(SK[0-9a-fA-F]{32})\b`),
	newSecPattern("slack", "Slack Webhook URL", `https://hooks\.slack\.com/services/[A-Z0-9]{6,}`),
	newSecPattern("private_key", "Private Key (PEM)", `-----BEGIN (RSA |EC |DSA |OPENSSH |PGP )?PRIVATE KEY-----`),
	newSecPattern("jwt", "JWT / Bearer Token", `\b(eyJ[a-zA-Z0-9_-]{10,}\.[a-zA-Z0-9_-]{10,}\.[a-zA-Z0-9_-]{10,})\b`),
	newSecPattern("password", "Password/Secret assignment", `(?i)(password|passwd|pwd|secret|token|api[_-]?key)\s*(=|:)\s*['"][^'"]{6,}['"]`),
	newSecPattern("url", "Credentials in URL", `\b(https?://)[^\s/@]{1,64}:[^\s/@]{1,64}@`),
}

const (
	secMaxFileSize  = 2 * 1024 * 1024 // 2 MB por fichero
	secScanMaxHits  = 300             // límite de hallazgos por scan
	secScanMaxFiles = 4000            // límite de ficheros revisados
)

var secDefaultSkipDirs = map[string]bool{
	".git": true, "node_modules": true, "dist": true, "build": true, "vendor": true,
	".venv": true, "venv": true, "__pycache__": true, ".idea": true, ".gradle": true,
	"coverage": true, ".next": true, "out": true, ".pytest_cache": true,
}

// RegisterSecurity registra las herramientas de seguridad.
func RegisterSecurity(reg *Registry, ws *Workspace) {
	reg.Register(model.ToolDefinition{
		Name: "security_scan",
		Description: "Scan project files for security issues by default: leaked credentials/secrets " +
			"(AWS keys, GitHub tokens, private keys, passwords, API keys, JWTs, URLs with credentials). " +
			"Walk a directory (default '.' = workspace root), skip binaries and common vendor dirs. " +
			"Returns file:line with a highlighted snippet. Read-only.",
		InputSchema: obj(map[string]any{
			"path":     prop("string", "File or directory to scan (default '.')"),
			"glob":     prop("string", "Optional file name filter, e.g. '*.env' or '*.go'"),
			"patterns": prop("string", "Comma-separated categories to limit the scan: aws, google, github, gitlab, slack, stripe, twilio, private_key, jwt, password, url. Default all."),
		}),
		Mutating: false,
	}, func(_ context.Context, args map[string]any) (string, error) {
		return runSecurityScan(ws.Root(), str(args["path"]), str(args["glob"]), str(args["patterns"]))
	})

	reg.Register(model.ToolDefinition{
		Name: "security_port_scan",
		Description: "Scan TCP ports on a host (default localhost). Reports open ports with a guessed " +
			"service name. Uses raw TCP dial (no external deps) with concurrency + per-port timeout. Read-only.",
		InputSchema: obj(map[string]any{
			"host":       prop("string", "Host to scan (default localhost)"),
			"ports":      prop("string", "Ports: single (80), comma list (80,443), range (8000-8010), or 'common' for the top ~60 (default)."),
			"timeout_ms": prop("integer", "Per-port dial timeout in ms (default 500)"),
			"concurrent": prop("integer", "Concurrency (default 50)"),
		}),
		Mutating: false,
	}, func(ctx context.Context, args map[string]any) (string, error) {
		return runPortScan(ctx, ws, str(args["host"]), str(args["ports"]), intArg(args, "timeout_ms", 500), intArg(args, "concurrent", 50))
	})

	reg.Register(model.ToolDefinition{
		Name: "security_checks",
		Description: "Basic security hygiene checks for the project (read-only): permissions of sensitive files " +
			"(.env, keys), listening sockets (netstat/lsof), and risky dependency config (package.json install hooks, " +
			"Dockerfile root, docker-compose 0.0.0.0 binds, terraform open SGs). Returns a bullet-point report.",
		InputSchema: obj(map[string]any{
			"path": prop("string", "Directory to check (default '.')"),
		}),
		Mutating: false,
	}, func(_ context.Context, args map[string]any) (string, error) {
		return runSecurityChecks(ws.Root(), str(args["path"]))
	})
}

func intArg(args map[string]any, k string, def int) int {
	if v, ok := toInt(args[k]); ok && v > 0 {
		return v
	}
	return def
}

// enabledCategories devuelve el subconjunto de patterns seleccionado por el
// argumento "patterns". Con cadena vacía usa todos.
func enabledCategories(csv string) []secPattern {
	sel := strings.TrimSpace(csv)
	if sel == "" {
		return securityPatterns
	}
	wanted := map[string]bool{}
	for _, c := range strings.Split(sel, ",") {
		if t := strings.ToLower(strings.TrimSpace(c)); t != "" {
			wanted[t] = true
		}
	}
	var out []secPattern
	for _, p := range securityPatterns {
		if wanted[p.cat] {
			out = append(out, p)
		}
	}
	return out
}

type secFinding struct {
	file          string
	line          int
	snippet, name string
}

func runSecurityScan(root, relPath, glob, patterns string) (string, error) {
	patterns2 := enabledCategories(patterns)
	if len(patterns2) == 0 {
		return "No matching pattern categories. Available: aws, google, github, gitlab, slack, stripe, twilio, private_key, jwt, password, url.", nil
	}

	base := "."
	if relPath != "" {
		base = relPath
	}
	basePath, err := safeJoin(root, base)
	if err != nil {
		return "", err
	}

	var findings []secFinding
	truncated := false

	scanFile := func(path string) {
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		if len(data) > secMaxFileSize {
			return
		}
		if n := len(data); n > 0 {
			t := n
			if t > 512 {
				t = 512
			}
			if bytes.IndexByte(data[:t], 0) >= 0 {
				return // binario
			}
		}
		text := string(data)
		rel, _ := filepath.Rel(root, path)
		lines := strings.Split(text, "\n")
		for _, p := range patterns2 {
			for _, m := range p.re.FindAllStringIndex(text, -1) {
				if len(findings) >= secScanMaxHits {
					truncated = true
					return
				}
				lineNo := 1 + strings.Count(text[:m[0]], "\n")
				if lineNo-1 < len(lines) {
					snippet := strings.TrimSpace(lines[lineNo-1])
					if len(snippet) > 160 {
						snippet = snippet[:160] + "…"
					}
					findings = append(findings, secFinding{file: rel, line: lineNo, snippet: snippet, name: p.name})
				}
			}
		}
	}

	if fi, err := os.Stat(basePath); err == nil && !fi.IsDir() {
		scanFile(basePath)
	} else if err == nil { // directorio
		files := 0
		_ = filepath.WalkDir(basePath, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if secDefaultSkipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if files >= secScanMaxFiles {
				truncated = true
				return filepath.SkipAll
			}
			if glob != "" {
				if ok, _ := filepath.Match(glob, d.Name()); !ok {
					return nil
				}
			}
			files++
			scanFile(p)
			if len(findings) >= secScanMaxHits {
				truncated = true
				return filepath.SkipAll
			}
			return nil
		})
	} else {
		return fmt.Sprintf("Path not found: %s", base), nil
	}

	if len(findings) == 0 {
		return "security_scan: no secrets/leaks found in " + base, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "security_scan: %d potential finding(s)\n", len(findings))
	for _, f := range findings {
		fmt.Fprintf(&b, "%s:%d [%s] %s\n", f.file, f.line, f.name, f.snippet)
	}
	if truncated {
		b.WriteString(fmt.Sprintf("\n… (truncated to %d findings; narrow with path/glob/patterns)", secScanMaxHits))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// ---------------------------------------------------------------------------
// security_port_scan
// ---------------------------------------------------------------------------

var commonPorts = []int{
	20, 21, 22, 23, 25, 53, 69, 80, 81, 110, 111, 123, 135, 137, 139, 143,
	161, 179, 389, 443, 445, 465, 514, 587, 636, 873, 902, 993, 995, 1080,
	1433, 1521, 2049, 2375, 2376, 3128, 3306, 3389, 4369, 5432, 5900, 5984,
	6379, 7001, 8000, 8080, 8081, 8088, 8443, 8888, 9090, 9200, 9300, 11211,
	27017, 28017,
}

func parsePortSpec(s string) []int {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "common") {
		return commonPorts
	}
	var ports []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			segs := strings.SplitN(part, "-", 2)
			lo, err1 := strconv.Atoi(strings.TrimSpace(segs[0]))
			hi, err2 := strconv.Atoi(strings.TrimSpace(segs[1]))
			if err1 == nil && err2 == nil && lo >= 1 && hi <= 65535 && hi >= lo {
				for p := lo; p <= hi; p++ {
					ports = append(ports, p)
				}
			}
			continue
		}
		if p, err := strconv.Atoi(part); err == nil && p >= 1 && p <= 65535 {
			ports = append(ports, p)
		}
	}
	if len(ports) > 1024 {
		ports = ports[:1024] // salvaguarda
	}
	return ports
}

func runPortScan(ctx context.Context, ws *Workspace, host, portSpec string, timeoutMs, concurrency int) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		host = "127.0.0.1"
	}
	_ = ws
	ports := parsePortSpec(portSpec)
	if len(ports) == 0 {
		return "Invalid port spec. Use e.g. '80', '80,443', '8000-8010', or 'common'.", nil
	}
	if concurrency <= 0 {
		concurrency = 50
	}

	open := make([]int, 0)
	var mu sync.Mutex

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, p := range ports {
		select {
		case <-ctx.Done():
			goto done
		default:
		}
		p := p
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			addr := net.JoinHostPort(host, strconv.Itoa(p))
			conn, err := net.DialTimeout("tcp", addr, time.Duration(timeoutMs)*time.Millisecond)
			if err == nil {
				conn.Close()
				mu.Lock()
				open = append(open, p)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
done:

	sort.Ints(open)
	var b strings.Builder
	fmt.Fprintf(&b, "security_port_scan: %s (scanned %d ports, %d open)\n", host, len(ports), len(open))
	if len(open) == 0 {
		b.WriteString("No TCP ports open.")
		return b.String(), nil
	}
	for _, p := range open {
		fmt.Fprintf(&b, "  %-5d %s\n", p, commonService(p))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func commonService(p int) string {
	if s, ok := map[int]string{
		21: "ftp", 22: "ssh", 23: "telnet", 25: "smtp", 53: "dns", 80: "http",
		110: "pop3", 111: "rpcbind", 143: "imap", 389: "ldap", 443: "https",
		445: "smb", 465: "smtps", 587: "smtp-submission", 873: "rsync", 993: "imaps",
		995: "pop3s", 1080: "socks", 1433: "mssql", 1521: "oracle", 2049: "nfs",
		2375: "docker(plain)", 2376: "docker(tls)", 3306: "mysql", 3389: "rdp",
		5432: "postgres", 5900: "vnc", 5984: "couchdb", 6379: "redis", 8080: "http-alt",
		8443: "https-alt", 8888: "http-alt", 9090: "prometheus", 9200: "elasticsearch",
		9300: "elasticsearch", 11211: "memcached", 27017: "mongodb", 28017: "mongodb",
	}[p]; ok {
		return s
	}
	return ""
}

// ---------------------------------------------------------------------------
// security_checks
// ---------------------------------------------------------------------------

func runSecurityChecks(root, relPath string) (string, error) {
	base := "."
	if relPath != "" {
		base = relPath
	}
	basePath, err := safeJoin(root, base)
	if err != nil {
		return "", err
	}
	var report []string

	if fi, err := os.Stat(basePath); err == nil && fi.IsDir() {
		sensitive := []string{".env", ".env.local", "id_rsa", "id_ed25519", "id_ecdsa", "*.pem", "*.key", ".pgpass"}
		_ = filepath.WalkDir(basePath, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				if d != nil && d.IsDir() && secDefaultSkipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			want := false
			for _, pat := range sensitive {
				if ok, _ := filepath.Match(pat, d.Name()); ok {
					want = true
					break
				}
			}
			if !want {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			mode := info.Mode()
			world := mode.Perm() & 0o004
			group := mode.Perm() & 0o020
			if world != 0 || group != 0 {
				rel, _ := filepath.Rel(root, p)
				report = append(report, fmt.Sprintf("⚠ %s has permissive permissions (%04o); chmod 600/700 recommended", rel, mode.Perm()))
			}
			return nil
		})
	} else if fi == nil {
		return fmt.Sprintf("Path not found: %s", base), nil
	}

	if out, err := netstatListening(); err == nil && strings.TrimSpace(out) != "" {
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(lines) > 20 {
			lines = lines[:20]
		}
		report = append(report, "🖥 Exposed/listening sockets (top 20):\n  "+strings.Join(lines, "\n  "))
	} else {
		report = append(report, "🖥 netstat/lsof no disponible o sin sockets listando")
	}

	report = append(report, checkDependencyFiles(root)...)

	if len(report) == 0 {
		return "security_checks: no issues found in " + base, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "security_checks report (%s):\n", base)
	for _, r := range report {
		b.WriteString("• " + r + "\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func netstatListening() (string, error) {
	if out, err := exec.Command("netstat", "-tulnp").Output(); err == nil {
		return string(out), nil
	}
	if out, err := exec.Command("netstat", "-tlnp").Output(); err == nil {
		return string(out), nil
	}
	if out, err := exec.Command("lsof", "-iTCP", "-sTCP:LISTEN").Output(); err == nil {
		return string(out), nil
	}
	return "", fmt.Errorf("netstat/lsof not available")
}

func checkDependencyFiles(root string) []string {
	var out []string

	if data, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		low := strings.ToLower(string(data))
		if strings.Contains(low, `"preinstall"`) || strings.Contains(low, `"postinstall"`) {
			out = append(out, "⚠ package.json has install hooks (pre/postinstall); verify they are not malicious")
		}
		if !fileExists(filepath.Join(root, ".gitignore")) {
			out = append(out, "ℹ no .gitignore found; ensure keys/secrets are not committed")
		}
	}

	if data, err := os.ReadFile(filepath.Join(root, "Dockerfile")); err == nil {
		low := strings.ToLower(string(data))
		if !strings.Contains(low, "user ") {
			out = append(out, "⚠ Dockerfile runs as root by default; consider a non-root USER directive")
		}
		if strings.Contains(low, "curl ") && strings.Contains(low, "| sh") {
			out = append(out, "⚠ Dockerfile pipes curl into a shell; verify the source before executing")
		}
	}

	if data, err := os.ReadFile(filepath.Join(root, "docker-compose.yml")); err == nil {
		low := strings.ToLower(string(data))
		if strings.Contains(low, "0.0.0.0:") {
			out = append(out, "⚠ docker-compose binds 0.0.0.0; ensure those services are intended to be public")
		}
	}

	if data, err := os.ReadFile(filepath.Join(root, "main.tf")); err == nil {
		low := strings.ReplaceAll(string(data), "\"", "")
		if strings.Contains(low, "0.0.0.0/0") {
			out = append(out, "⚠ Terraform security group allows 0.0.0.0/0; likely too permissive")
		}
	}
	return out
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
