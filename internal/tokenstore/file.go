// Package tokenstore provides a file-backed auth.Store.
package tokenstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/emergingrobotics/gopentair/internal/auth"
)

// DefaultPath returns ~/.config/pentair/tokens.json (honouring XDG_CONFIG_HOME).
func DefaultPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "pentair", "tokens.json")
}

// File stores sessions in a single JSON file keyed by account, written
// atomically with mode 0600. It refuses to read a group/world-readable file.
type File struct {
	Path string
	mu   sync.Mutex
}

// NewFile returns a File store at path ("" for DefaultPath).
func NewFile(path string) *File {
	if path == "" {
		path = DefaultPath()
	}
	return &File{Path: path}
}

func (f *File) read() (map[string]auth.Session, error) {
	st, err := os.Stat(f.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]auth.Session{}, nil
	}
	if err != nil {
		return nil, err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("token file %s has permissions %v; must not be group/world accessible (chmod 600)", f.Path, st.Mode().Perm())
	}
	b, err := os.ReadFile(f.Path)
	if err != nil {
		return nil, err
	}
	m := map[string]auth.Session{}
	if len(b) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("token file %s: %w", f.Path, err)
	}
	return m, nil
}

func (f *File) write(m map[string]auth.Session) error {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.Path), ".tokens-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.Path)
}

// Load implements auth.Store.
func (f *File) Load(_ context.Context, key string) (*auth.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.read()
	if err != nil {
		return nil, err
	}
	s, ok := m[key]
	if !ok {
		return nil, auth.ErrNotFound
	}
	return &s, nil
}

// Save implements auth.Store.
func (f *File) Save(_ context.Context, key string, s *auth.Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.read()
	if err != nil {
		return err
	}
	m[key] = *s
	return f.write(m)
}

// Delete implements auth.Store.
func (f *File) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.read()
	if err != nil {
		return err
	}
	if _, ok := m[key]; !ok {
		return auth.ErrNotFound
	}
	delete(m, key)
	return f.write(m)
}
