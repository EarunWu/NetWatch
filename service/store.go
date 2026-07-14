package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const maxConfigBytes = 1024 * 1024
const currentConfigVersion = 3

type persistedConfig struct {
	Version int      `json:"version"`
	Targets []Target `json:"targets"`
}

type ConfigStore struct {
	path string
}

func NewConfigStore(dataDir string) *ConfigStore {
	return &ConfigStore{path: filepath.Join(dataDir, "targets.json")}
}

// Load returns exists=false only when no configuration file has been created.
func (s *ConfigStore) Load() (targets []Target, exists bool, err error) {
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(io.LimitReader(file, maxConfigBytes+1))
	decoder.DisallowUnknownFields()
	var config persistedConfig
	if err := decoder.Decode(&config); err != nil {
		return nil, true, fmt.Errorf("decode config: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, true, fmt.Errorf("decode config: %w", err)
	}
	if config.Version != 1 && config.Version != 2 && config.Version != currentConfigVersion {
		return nil, true, fmt.Errorf("unsupported config version %d", config.Version)
	}
	if config.Targets == nil {
		config.Targets = []Target{}
	}
	return config.Targets, true, nil
}

func (s *ConfigStore) Save(targets []Target) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	payload, err := json.MarshalIndent(persistedConfig{Version: currentConfigVersion, Targets: targets}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	payload = append(payload, '\n')

	temporary, err := os.CreateTemp(filepath.Dir(s.path), "targets-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	temporaryName := temporary.Name()
	cleanup := func() {
		temporary.Close()
		_ = os.Remove(temporaryName)
	}
	if err := temporary.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("secure temporary config: %w", err)
	}
	if _, err := temporary.Write(payload); err != nil {
		cleanup()
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryName)
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(temporaryName, s.path); err != nil {
		_ = os.Remove(temporaryName)
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}
