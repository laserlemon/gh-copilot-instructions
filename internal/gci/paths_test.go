package gci

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPathsSplitConfigAndState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	p := DefaultPaths()

	// Config (user-edited) lives under ~/.config; state (machine-generated) lives
	// under ~/.local/state — never under gh's own ~/.local/state/gh/extensions,
	// which gh wipes on install/remove.
	wantSources := filepath.Join(home, ".config", "gh-copilot-instructions", "sources")
	if p.SourcesFile != wantSources {
		t.Errorf("SourcesFile = %q, want %q", p.SourcesFile, wantSources)
	}
	wantState := filepath.Join(home, ".local", "state", "gh-copilot-instructions", "state.json")
	if p.StateFile != wantState {
		t.Errorf("StateFile = %q, want %q", p.StateFile, wantState)
	}
}

func TestXDGStateHomeHonored(t *testing.T) {
	home := t.TempDir()
	stateHome := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", stateHome)

	p := DefaultPaths()
	want := filepath.Join(stateHome, "gh-copilot-instructions", "state.json")
	if p.StateFile != want {
		t.Errorf("StateFile = %q, want %q", p.StateFile, want)
	}
}

func TestMigrateLegacyState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	p := DefaultPaths()

	// Seed a pre-state-dir state.json in the OLD location (next to sources).
	legacy := filepath.Join(p.ConfigDir, "state.json")
	if err := os.MkdirAll(p.ConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const body = `{"sources":{"abc12345":{"repo":"o/r","ref":"","path":"","sha":"deadbeef","pulled_at":"2026-01-01T00:00:00Z","files":["x"]}}}`
	if err := os.WriteFile(legacy, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	// Loading state should transparently migrate it to the new state dir.
	st, err := p.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Sources["abc12345"]; !ok {
		t.Fatalf("migrated state missing expected source: %+v", st.Sources)
	}
	if _, err := os.Stat(p.StateFile); err != nil {
		t.Errorf("state.json not present at new location: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy state.json should be removed after migration, stat err = %v", err)
	}
}
