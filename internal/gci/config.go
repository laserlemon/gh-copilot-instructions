package gci

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Env var names (the full configuration surface).
const (
	EnvSources = "GH_COPILOT_INSTRUCTIONS"       // multiline source list (overrides the file)
	EnvToken   = "GH_COPILOT_INSTRUCTIONS_TOKEN" // fallback token for tokenless lines
	EnvRef     = "GH_COPILOT_INSTRUCTIONS_REF"   // default ref for lines that omit @ref
)

// ConfigOrigin describes where the active source list came from.
type ConfigOrigin int

const (
	OriginNone ConfigOrigin = iota
	OriginEnv
	OriginFile
)

func (o ConfigOrigin) String() string {
	switch o {
	case OriginEnv:
		return EnvSources + " (env)"
	case OriginFile:
		return "config file"
	default:
		return "none"
	}
}

// LoadSources returns the active sources and where they came from. The env var
// wins when set; otherwise the local file is read. Malformed lines are skipped
// with a collected error.
func (p Paths) LoadSources() ([]Source, ConfigOrigin, error) {
	if raw := os.Getenv(EnvSources); strings.TrimSpace(raw) != "" {
		srcs, err := parseSources(raw)
		return srcs, OriginEnv, err
	}
	data, err := os.ReadFile(p.SourcesFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, OriginNone, nil
		}
		return nil, OriginNone, err
	}
	srcs, err := parseSources(string(data))
	return srcs, OriginFile, err
}

func parseSources(raw string) ([]Source, error) {
	var srcs []Source
	var errs []string
	sc := bufio.NewScanner(strings.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		s, ok, err := ParseLine(sc.Text())
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		if ok {
			srcs = append(srcs, s)
		}
	}
	if len(errs) > 0 {
		return srcs, fmt.Errorf("config: %s", strings.Join(errs, "; "))
	}
	return srcs, nil
}

// readFileSources reads sources from the local file only (ignoring env). Used by
// add/remove, which always mutate the file.
func (p Paths) readFileSources() ([]Source, error) {
	data, err := os.ReadFile(p.SourcesFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return parseSources(string(data))
}

// writeFileSources writes the local sources file with 0600 perms.
func (p Paths) writeFileSources(srcs []Source) error {
	if err := os.MkdirAll(p.ConfigDir, 0o755); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# gh-copilot-instructions sources — one per line:\n")
	b.WriteString("#   owner/repo[@ref][:path]  [token]\n")
	for _, s := range srcs {
		b.WriteString(s.Line())
		b.WriteByte('\n')
	}
	tmp := p.SourcesFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.SourcesFile)
}

// AddSource upserts a source into the local file (replacing any existing entry
// with the same id) and returns the stored source.
func (p Paths) AddSource(s Source) error {
	existing, err := p.readFileSources()
	if err != nil {
		return err
	}
	id := s.ID()
	out := existing[:0:0]
	for _, e := range existing {
		if e.ID() != id {
			out = append(out, e)
		}
	}
	out = append(out, s)
	return p.writeFileSources(out)
}

// RemoveSource removes sources from the local file by id or "owner/repo".
// Returns the removed sources.
func (p Paths) RemoveSource(idOrRepo string) ([]Source, error) {
	existing, err := p.readFileSources()
	if err != nil {
		return nil, err
	}
	var kept, removed []Source
	for _, e := range existing {
		if e.ID() == idOrRepo || e.Repo == idOrRepo {
			removed = append(removed, e)
		} else {
			kept = append(kept, e)
		}
	}
	if len(removed) == 0 {
		return nil, nil
	}
	if err := p.writeFileSources(kept); err != nil {
		return nil, err
	}
	return removed, nil
}

// ClearSources truncates the local sources file.
func (p Paths) ClearSources() error {
	if _, err := os.Stat(p.SourcesFile); os.IsNotExist(err) {
		return nil
	}
	return p.writeFileSources(nil)
}

// EnvSet reports whether the env var config is in effect (so add/remove can warn
// that file edits won't take effect until the env var is unset).
func EnvSet() bool {
	return strings.TrimSpace(os.Getenv(EnvSources)) != ""
}

// defaultRef returns the env-provided default ref (may be empty).
func defaultRef() string { return strings.TrimSpace(os.Getenv(EnvRef)) }
