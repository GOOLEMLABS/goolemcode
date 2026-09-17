// Package session persiste la conversación (los mensajes neutrales) en disco,
// de modo que se pueda reanudar una sesión anterior con -resume. Se guarda en
// <workdir>/.goolem/session.json, anclado al directorio de arranque (como la
// memoria .md), no al directorio móvil de /cd.
package session

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/GOOLEMLABS/goolemcode/internal/model"
)

type Store struct {
	path string
}

// New crea el store en <root>/.goolem/session.json.
func New(root string) *Store {
	return &Store{path: filepath.Join(root, ".goolem", "session.json")}
}

func (s *Store) Path() string { return s.path }

// Save escribe los mensajes (atómico). No hace nada si la lista está vacía.
func (s *Store) Save(msgs []model.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(msgs, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Clear borra la sesión guardada (ignora si no existe).
func (s *Store) Clear() error {
	err := os.Remove(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Load lee la conversación guardada (nil si no existe o está corrupta).
func (s *Store) Load() []model.Message {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil
	}
	var msgs []model.Message
	if json.Unmarshal(data, &msgs) != nil {
		return nil
	}
	return msgs
}
