package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestConfigStoreRoundTripAndReplace(t *testing.T) {
	store := NewConfigStore(t.TempDir())
	loaded, exists, err := store.Load()
	if err != nil || exists || loaded != nil {
		t.Fatalf("unexpected missing load: targets=%#v exists=%v err=%v", loaded, exists, err)
	}
	targets := []Target{
		{ID: "one", Name: "One", Kind: ProbeKindDirectTCP, Host: "127.0.0.1", Port: 443, IntervalMS: 1000, TimeoutMS: 500, Enabled: false},
		{ID: "node", Name: "Node", Kind: ProbeKindProxyGoogle, Host: "status.example", Port: 8443, ProxyHost: "127.0.0.1", ProxyPort: 10808, IntervalMS: 5000, TimeoutMS: 8000, Enabled: false},
	}
	if err := store.Save(targets); err != nil {
		t.Fatalf("first save: %v", err)
	}
	targets[0].Name = "Updated"
	if err := store.Save(targets); err != nil {
		t.Fatalf("replacement save: %v", err)
	}
	loaded, exists, err = store.Load()
	if err != nil || !exists || !reflect.DeepEqual(loaded, targets) {
		t.Fatalf("round trip mismatch: targets=%#v exists=%v err=%v", loaded, exists, err)
	}
}

func TestConfigStoreLoadsV1AndSavesCurrentVersion(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "targets.json")
	legacy := `{"version":1,"targets":[{"id":"old","name":"Old","host":"127.0.0.1","port":443,"interval_ms":1000,"timeout_ms":500,"enabled":true}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewConfigStore(directory)
	targets, exists, err := store.Load()
	if err != nil || !exists || len(targets) != 1 {
		t.Fatalf("legacy config failed to load: %#v %v %v", targets, exists, err)
	}
	normalized, err := normalizeAndValidateTarget(targets[0])
	if err != nil || normalized.Kind != ProbeKindDirectTCP {
		t.Fatalf("legacy target did not migrate logically: %#v %v", normalized, err)
	}
	if err := store.Save([]Target{normalized}); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved persistedConfig
	if err := json.Unmarshal(payload, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Version != currentConfigVersion {
		t.Fatalf("saved config version = %d", saved.Version)
	}
}

func TestConfigStoreLoadsV2WithGoogle204DisabledByDefault(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "targets.json")
	versionTwo := `{"version":2,"targets":[{"id":"node","name":"Node","kind":"proxy_google","host":"www.google.com","port":443,"proxy_host":"127.0.0.1","proxy_port":10808,"interval_ms":5000,"timeout_ms":8000,"enabled":true}]}`
	if err := os.WriteFile(path, []byte(versionTwo), 0o600); err != nil {
		t.Fatal(err)
	}
	targets, exists, err := NewConfigStore(directory).Load()
	if err != nil || !exists || len(targets) != 1 {
		t.Fatalf("version 2 config failed to load: %#v %v %v", targets, exists, err)
	}
	if targets[0].Google204Enabled {
		t.Fatal("legacy node unexpectedly enabled Google 204")
	}
}

func TestConfigStoreLoadsV3WithBypassDisabledAndMarksMigration(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "targets.json")
	versionThree := `{"version":3,"targets":[{"id":"direct","name":"Direct","kind":"direct_tcp","host":"1.1.1.1","port":443,"google_204_enabled":false,"interval_ms":5000,"timeout_ms":2000,"enabled":true}]}`
	if err := os.WriteFile(path, []byte(versionThree), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewConfigStore(directory)
	targets, exists, err := store.Load()
	if err != nil || !exists || len(targets) != 1 {
		t.Fatalf("version 3 config failed to load: %#v %v %v", targets, exists, err)
	}
	if targets[0].BypassTUN || targets[0].BypassInterfaceID != "" {
		t.Fatalf("legacy direct target unexpectedly enabled bypass: %#v", targets[0])
	}
	if !store.NeedsMigration() {
		t.Fatal("version 3 config was not marked for migration")
	}
	if err := store.Save(targets); err != nil {
		t.Fatal(err)
	}
	if store.NeedsMigration() {
		t.Fatal("saved version 4 config still needs migration")
	}
}

func TestDefaultTargetsEnableTUNBypass(t *testing.T) {
	for _, target := range defaultTargets() {
		if target.Kind == ProbeKindDirectTCP && !target.BypassTUN {
			t.Fatalf("new default target did not enable bypass: %#v", target)
		}
	}
}

func TestApplyTargetDefaultsMigratesLegacyKind(t *testing.T) {
	targets := []Target{{Name: "Legacy"}, {Name: "Node", Kind: ProbeKindProxyGoogle}}
	if !applyTargetDefaults(targets) || targets[0].Kind != ProbeKindDirectTCP || targets[1].Kind != ProbeKindProxyGoogle {
		t.Fatalf("legacy target defaults were not applied: %#v", targets)
	}
	if applyTargetDefaults(targets) {
		t.Fatal("already migrated targets were reported as changed")
	}
}

func TestConfigStoreRejectsUnknownFields(t *testing.T) {
	directory := t.TempDir()
	store := NewConfigStore(directory)
	if err := os.WriteFile(filepath.Join(directory, "targets.json"), []byte(`{"version":1,"targets":[],"unexpected":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Load(); err == nil {
		t.Fatal("unknown config field was accepted")
	}
}
