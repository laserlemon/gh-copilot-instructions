package gci

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// SourceState is the recorded result of the last successful pull of a source.
type SourceState struct {
	Repo     string    `json:"repo"`
	Ref      string    `json:"ref"`
	Path     string    `json:"path"`
	SHA      string    `json:"sha"`
	PulledAt time.Time `json:"pulled_at"`
	Files    []string  `json:"files"` // installed filenames (relative to InstallDir); also the prune manifest
}

// State maps source id -> SourceState.
type State struct {
	Sources    map[string]SourceState `json:"sources"`
	AutoPull   *AutoPullState         `json:"autoPull,omitempty"`
	Codespaces *CodespacesState       `json:"codespaces,omitempty"`
}

// CodespacesState records what this machine last pushed to the user's
// GH_COPILOT_INSTRUCTIONS Codespaces secret. It is nil until `codespaces setup`
// (or `codespaces update`) first pushes the secret. SecretSignature is the
// ConfigSignature of the source set at push time; `codespaces check` compares it
// against the current signature to detect drift. Because the secret's value is
// write-only via the API, this local record - not a remote marker - is how we
// answer "did my sources change since I last pushed?" on the machine that pushed.
type CodespacesState struct {
	SecretSignature string    `json:"secretSignature"`
	PushedAt        time.Time `json:"pushedAt"`
}

// AutoPullState records the user's scheduled-pull intent (see autopull.go). It
// is nil until auto-pull is first enabled. The OS scheduler (launchd/cron) is
// the source of truth for whether a job is actually installed; this is the
// recorded intent that `status` reconciles against.
type AutoPullState struct {
	Enabled   bool      `json:"enabled"`
	Cadence   string    `json:"cadence"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// LoadState reads state.json (returns an empty State if absent).
func (p Paths) LoadState() (*State, error) {
	p.migrateLegacyState()
	st := &State{Sources: map[string]SourceState{}}
	data, err := os.ReadFile(p.StateFile)
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return st, nil
	}
	if err := json.Unmarshal(data, st); err != nil {
		return nil, err
	}
	if st.Sources == nil {
		st.Sources = map[string]SourceState{}
	}
	return st, nil
}

// migrateLegacyState relocates a pre-state-dir state.json (which lived next to
// the sources file in ConfigDir) into the XDG state dir, once. Best-effort: any
// failure leaves the legacy file untouched so a later run can retry, and the
// worst case is simply a re-pull that regenerates state.
func (p Paths) migrateLegacyState() {
	legacy := filepath.Join(p.ConfigDir, "state.json")
	if legacy == p.StateFile {
		return
	}
	if _, err := os.Stat(p.StateFile); err == nil {
		return // already migrated (or fresh install wrote here directly)
	}
	data, err := os.ReadFile(legacy)
	if err != nil {
		return // nothing to migrate
	}
	if err := os.MkdirAll(p.StateDir, 0o755); err != nil {
		return
	}
	tmp := p.StateFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, p.StateFile); err != nil {
		os.Remove(tmp)
		return
	}
	os.Remove(legacy) // best-effort cleanup of the old location
}

// Save writes state.json atomically.
func (p Paths) Save(st *State) error {
	if err := os.MkdirAll(p.StateDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := p.StateFile + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.StateFile)
}
