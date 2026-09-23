// Package extid persists the SCIM externalId of users. Outline has no field for
// it, but some SCIM clients (Pocket ID) match remote users only by externalId
// and delete every user whose externalId they do not recognise.
package extid

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// FileStore maps Outline user ids to SCIM externalIds and keeps the mapping in
// a JSON file. The mapping is 1:1: assigning an externalId to one user removes
// it from any other user.
type FileStore struct {
	path string

	mu    sync.RWMutex
	byID  map[string]string // outline id -> externalId
	byExt map[string]string // externalId -> outline id
}

// Open loads the mapping from path, creating the file if it does not exist so a
// missing volume or wrong permissions fail at startup, not on the first write.
func Open(path string) (*FileStore, error) {
	s := &FileStore{path: path, byID: map[string]string{}, byExt: map[string]string{}}

	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := s.save(); err != nil {
			return nil, err
		}
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	if len(data) > 0 {
		if err := json.Unmarshal(data, &s.byID); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	for id, ext := range s.byID {
		s.byExt[ext] = id
	}
	return s, nil
}

// Get returns the externalId stored for an Outline user id, or "".
func (s *FileStore) Get(outlineID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byID[outlineID]
}

// Lookup returns the Outline user id stored for an externalId.
func (s *FileStore) Lookup(externalID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byExt[externalID]
	return id, ok
}

// Set stores externalID for outlineID and writes the file. A no-op if the
// mapping is already present.
func (s *FileStore) Set(outlineID, externalID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byID[outlineID] == externalID {
		return nil
	}
	if old, ok := s.byID[outlineID]; ok {
		delete(s.byExt, old)
	}
	if other, ok := s.byExt[externalID]; ok {
		delete(s.byID, other)
	}
	s.byID[outlineID] = externalID
	s.byExt[externalID] = outlineID
	return s.save()
}

// Delete drops the mapping for outlineID and writes the file.
func (s *FileStore) Delete(outlineID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ext, ok := s.byID[outlineID]
	if !ok {
		return nil
	}
	delete(s.byID, outlineID)
	delete(s.byExt, ext)
	return s.save()
}

// save writes the mapping via a temp file and rename so a crash mid-write never
// leaves a truncated file behind. Callers hold the write lock (or own s).
func (s *FileStore) save() error {
	data, err := json.MarshalIndent(s.byID, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".extid-*.json")
	if err != nil {
		return fmt.Errorf("write %s: %w", s.path, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", s.path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", s.path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", s.path, err)
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return fmt.Errorf("write %s: %w", s.path, err)
	}
	return nil
}
