package gci

import (
	"os"
	"path/filepath"
)

// Paths resolves the filesystem locations the tool uses. All are derived from
// HOME / XDG_* so tests can fully sandbox by overriding the environment.
//
// Config and state are split the way gh itself splits them: the user-edited
// sources file lives under XDG_CONFIG_HOME, while machine-generated, regenerable
// state lives under XDG_STATE_HOME. Both use our own "gh-copilot-instructions"
// namespace.
//
// Note: we deliberately do NOT nest state under gh's own per-extension dir
// (~/.local/state/gh/extensions/<name>/). gh's extension manager os.RemoveAll's
// that directory on every install/remove (cleanExtensionUpdateDir), so anything
// we stored there would be wiped whenever a teammate or Codespace runs
// `gh extension install`.
type Paths struct {
	ConfigDir   string // $XDG_CONFIG_HOME/gh-copilot-instructions (~/.config/...)
	SourcesFile string // <ConfigDir>/sources   (chmod 600)
	StateDir    string // $XDG_STATE_HOME/gh-copilot-instructions (~/.local/state/...)
	StateFile   string // <StateDir>/state.json
	InstallDir  string // ~/.copilot/instructions
}

// DefaultPaths computes paths from the current environment.
func DefaultPaths() Paths {
	home, _ := os.UserHomeDir()
	cfgHome := os.Getenv("XDG_CONFIG_HOME")
	if cfgHome == "" {
		cfgHome = filepath.Join(home, ".config")
	}
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		stateHome = filepath.Join(home, ".local", "state")
	}
	cfgDir := filepath.Join(cfgHome, "gh-copilot-instructions")
	stateDir := filepath.Join(stateHome, "gh-copilot-instructions")
	return Paths{
		ConfigDir:   cfgDir,
		SourcesFile: filepath.Join(cfgDir, "sources"),
		StateDir:    stateDir,
		StateFile:   filepath.Join(stateDir, "state.json"),
		InstallDir:  filepath.Join(home, ".copilot", "instructions"),
	}
}
