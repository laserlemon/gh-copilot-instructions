package gci

import (
	"encoding/json"
	"os"
	"time"
)

// SourceState is the recorded result of the last successful pull of a source.
type SourceState struct {
	Repo        string    `json:"repo"`
	Ref         string    `json:"ref"`
	ResolvedRef string    `json:"resolved_ref"` // actual branch used (e.g. the default branch when Ref is empty)
	Path        string    `json:"path"`
	SHA         string    `json:"sha"`
	PulledAt    time.Time `json:"pulled_at"`
	Files       []string  `json:"files"` // installed filenames (relative to InstallDir); also the prune manifest
}

// State maps source id -> SourceState.
type State struct {
	Sources map[string]SourceState `json:"sources"`
}

// LoadState reads state.json (returns an empty State if absent).
func (p Paths) LoadState() (*State, error) {
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

// Save writes state.json atomically.
func (p Paths) Save(st *State) error {
	if err := os.MkdirAll(p.ConfigDir, 0o755); err != nil {
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
