# GoolemCode (Go) — build & install
BIN     := goolemcode
PREFIX  := $(HOME)/bin
CONFDIR := $(HOME)/.config/goolemcode
GO      ?= go

.PHONY: all build install vet test clean self

all: build

build:
	$(GO) build -o $(BIN) .

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

# Compila, instala el binario en ~/bin y siembra la config global si no existe.
install: build
	mkdir -p $(PREFIX) $(CONFDIR)
	cp $(BIN) $(PREFIX)/$(BIN)
	chmod +x $(PREFIX)/$(BIN)
	@if command -v codesign >/dev/null 2>&1; then \
		codesign --force --sign - $(PREFIX)/$(BIN) >/dev/null 2>&1; \
		echo "Firmado ad-hoc de $(PREFIX)/$(BIN) (evita SIGKILL por taskgated)"; \
	fi
	@if [ ! -f $(CONFDIR)/goolemcode.json ]; then \
		cp goolemcode.example.json $(CONFDIR)/goolemcode.json; \
		echo "Config global creada en $(CONFDIR)/goolemcode.json (ajústala)"; \
	else \
		echo "Config global ya existe en $(CONFDIR)/goolemcode.json (no se toca)"; \
	fi
	@echo "Instalado: $(PREFIX)/$(BIN)"

clean:
	rm -f $(BIN)

# Auto-mejora: lanza GoolemCode sobre su PROPIO código con el gate de permisos
# ACTIVO (sin --auto-approve). Recompila antes para arrancar con lo último.
# Aprueba cada escritura/comando; usa /rewind si algo se rompe.
self: build
	@echo "⚠  AUTO-MEJORA: GoolemCode va a editar su PROPIO código fuente."
	@echo "   Gate de permisos ACTIVO: aprueba cada escritura/comando (1/2/3)."
	@echo "   Tras cambios: make vet && make test && make install. /rewind para deshacer."
	@echo "   Lee la nota 'como-mejorar-goolemcode' (está en tu índice de conocimiento)."
	./$(BIN) -workdir .
