package clicontext

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Store manages the list of saved contexts in contexts.json inside the CLI config directory.
type Store struct {
	path     string
	prefsDir string // directory containing config.json (used by SetActive)
}

// NewStoreAt creates a Store backed by a contexts.json file at the given directory path.
// The directory is created with 0700 permissions if it does not exist.
func NewStoreAt(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create context store directory: %w", err)
	}
	return &Store{
		path:     filepath.Join(dir, "contexts.json"),
		prefsDir: dir,
	}, nil
}

// List returns all saved contexts.  Returns an empty slice if the file does not exist.
func (s *Store) List() ([]Context, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Context{}, nil
		}
		return nil, fmt.Errorf("read contexts file: %w", err)
	}

	var ctxs []Context
	if err := json.Unmarshal(data, &ctxs); err != nil {
		return nil, fmt.Errorf("parse contexts file: %w", err)
	}
	return ctxs, nil
}

// Get returns the named context or an error if it does not exist.
func (s *Store) Get(name string) (*Context, error) {
	ctxs, err := s.List()
	if err != nil {
		return nil, err
	}
	for i := range ctxs {
		if ctxs[i].Name == name {
			return &ctxs[i], nil
		}
	}
	return nil, fmt.Errorf("context %q not found", name)
}

// Save upserts a context by name.  The RawKey field is never written to disk.
func (s *Store) Save(ctx Context) error {
	ctxs, err := s.List()
	if err != nil {
		return err
	}

	// Strip raw key before persisting.
	ctx.RawKey = ""

	updated := false
	for i := range ctxs {
		if ctxs[i].Name == ctx.Name {
			ctxs[i] = ctx
			updated = true
			break
		}
	}
	if !updated {
		ctxs = append(ctxs, ctx)
	}

	return s.write(ctxs)
}

// Delete removes the named context from the store.
func (s *Store) Delete(name string) error {
	ctxs, err := s.List()
	if err != nil {
		return err
	}

	filtered := ctxs[:0]
	found := false
	for _, c := range ctxs {
		if c.Name == name {
			found = true
			continue
		}
		filtered = append(filtered, c)
	}
	if !found {
		return fmt.Errorf("context %q not found", name)
	}

	return s.write(filtered)
}

// SetActive updates the active_context field in config.json (prefs) located in
// the same directory as contexts.json.
func (s *Store) SetActive(name string) error {
	// Verify the context exists first.
	if _, err := s.Get(name); err != nil {
		return err
	}

	prefsPath := filepath.Join(s.prefsDir, "config.json")

	// Read existing prefs (or create minimal default).
	var prefsRaw map[string]interface{}
	data, err := os.ReadFile(prefsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			prefsRaw = map[string]interface{}{}
		} else {
			return fmt.Errorf("read prefs file: %w", err)
		}
	} else {
		if err := json.Unmarshal(data, &prefsRaw); err != nil {
			return fmt.Errorf("parse prefs file: %w", err)
		}
	}

	prefsRaw["active_context"] = name

	out, err := json.MarshalIndent(prefsRaw, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal prefs: %w", err)
	}

	tmp := prefsPath + ".tmp"
	if err := os.WriteFile(tmp, out, 0600); err != nil {
		return fmt.Errorf("write temp prefs file: %w", err)
	}
	if err := os.Rename(tmp, prefsPath); err != nil {
		return fmt.Errorf("rename prefs file: %w", err)
	}
	return nil
}

// write serialises ctxs to disk atomically.
func (s *Store) write(ctxs []Context) error {
	data, err := json.MarshalIndent(ctxs, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal contexts: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write temp contexts file: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("rename contexts file: %w", err)
	}
	return nil
}
