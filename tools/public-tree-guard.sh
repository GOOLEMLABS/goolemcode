#!/bin/sh
# public-tree-guard.sh — Guardián del árbol público de GoolemCode.
#
# Comprueba que NADA de lo que se va a publicar contenga secretos, IPs privadas,
# rutas locales del desarrollador o estado local del agente (.goolem/).
# Se usa a mano y desde el hook .git/hooks/pre-push.
#
# Uso: tools/public-tree-guard.sh [--all]
#   (por defecto escanea los ficheros rastreados por git del árbol actual)
#
# Salida: 0 = limpio; 1 = hallazgos (bloquea el push).

set -u
FILES="$(git ls-files 2>/dev/null || find . -type f)"
# El propio guardián contiene los patrones de detección: no se autoescanea
FILES="$(printf '%s\n' "$FILES" | grep -v 'public-tree-guard.sh$' || true)"
FOUND=0

report() {
  FOUND=1
  echo "❌ GUARDIÁN: $1"
  shift
  printf '%s\n' "$@" | sed 's/^/    /'
  echo
}

# 1. Ficheros que nunca deben estar rastreados
BAD_TRACKED="$(printf '%s\n' "$FILES" | grep -E '(^|/)\.goolem/|(^|/)\.env$|goolemcode\.json$|(^|/)\.DS_Store$' || true)"
if [ -n "$BAD_TRACKED" ]; then
  report "estado local/secretos rastreados por git (deben estar en .gitignore)" "$BAD_TRACKED"
fi

# 2. Secretos
SECRETS="$(grep -nHE '(ghp_[A-Za-z0-9]{36}|github_pat_[A-Za-z0-9_]{20,}|sk-ant-[A-Za-z0-9_-]{20,}|AKIA[0-9A-Z]{16}|-----BEGIN [A-Z ]*PRIVATE KEY-----|xox[baprs]-[A-Za-z0-9-]{10,})' $(printf '%s\n' "$FILES") 2>/dev/null || true)"
if [ -n "$SECRETS" ]; then
  report "posibles secretos" "$SECRETS"
fi

# 3. IPs privadas / hosts internos
IPS="$(grep -nHE '(^|[^0-9])(192\.168\.[0-9]+\.[0-9]+|10\.[0-9]+\.[0-9]+\.[0-9]+|172\.(1[6-9]|2[0-9]|3[01])\.[0-9]+\.[0-9]+|goolemserver)' $(printf '%s\n' "$FILES") 2>/dev/null || true)"
if [ -n "$IPS" ]; then
  report "IPs privadas / hosts internos" "$IPS"
fi

# 4. Rutas locales del desarrollador
PATHS="$(grep -nHE '/(Users|home)/[a-zA-Z0-9_.-]+' $(printf '%s\n' "$FILES") 2>/dev/null || true)"
if [ -n "$PATHS" ]; then
  report "rutas locales" "$PATHS"
fi

# 5. Identidad de los commits: no publicar emails personales.
#    Permitidos: noreply de GitHub (*@users.noreply.github.com), bots y, si se
#    define, GUARD_ALLOWED_EMAIL (para colaboradores legítimos).
BAD_MAIL="$(git log --format='%h %ae %ce' --all 2>/dev/null | sort -u | awk -v allow="${GUARD_ALLOWED_EMAIL:-}" '
  {
    bad=""
    for (i = 2; i <= NF; i++) {
      e = $i
      if (e ~ /@users\.noreply\.github\.com$/) continue
      if (allow != "" && e == allow) continue
      bad = bad " " e
    }
    if (bad != "") print $1 bad
  }' || true)"
if [ -n "$BAD_MAIL" ]; then
  report "emails no-noreply en la identidad de commits (usa <id>+usuario@users.noreply.github.com)" "$BAD_MAIL"
fi

if [ "$FOUND" -eq 0 ]; then
  echo "✅ GUARDIÁN: árbol limpio ($(printf '%s\n' "$FILES" | wc -l | tr -d ' ') ficheros) — push permitido."
  exit 0
fi

echo "🚫 GUARDIÁN: push BLOQUEADO. Sanea el árbol antes de publicar."
exit 1
