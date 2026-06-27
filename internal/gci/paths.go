package gci

import (
	"os"
	"path/filepath"
)

// Paths resolves the filesystem locations the tool uses. All are derived from
// HOME / XDG_* so tests can fully sandbox by overriding the environment.
type Paths struct {
	ConfigDir   string // ~/.config/gh-copilot-instructions
	SourcesFile string // <ConfigDir>/sources   (chmod 600)
	StateFile   string // <ConfigDir>/state.json
	InstallDir  string // ~/.copilot/instructions
}

// DefaultPaths computes paths from the current environment.
func DefaultPaths() Paths {
	home, _ := os.UserHomeDir()
	cfgHome := os.Getenv("XDG_CONFIG_HOME")
	if cfgHome == "" {
		cfgHome = filepath.Join(home, ".config")
	}
	cfgDir := filepath.Join(cfgHome, "gh-copilot-instructions")
	return Paths{
		ConfigDir:   cfgDir,
		SourcesFile: filepath.Join(cfgDir, "sources"),
		StateFile:   filepath.Join(cfgDir, "state.json"),
		InstallDir:  filepath.Join(home, ".copilot", "instructions"),
	}
}
