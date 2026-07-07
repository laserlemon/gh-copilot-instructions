package gci

import (
	"path"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// matchPatterns returns the doublestar patterns a source's path expands to.
//
//   - empty path      -> the default "**/*.instructions.md"
//   - a glob          -> used as-is
//   - a plain path     -> matched as an exact file OR as a directory ("<p>/**/*.md")
func (s Source) matchPatterns() []string {
	p := s.effectivePath()
	if isGlob(p) {
		return []string{p}
	}
	p = strings.Trim(p, "/")
	if p == "" {
		return []string{DefaultPath}
	}
	// Plain path: either the exact file, or every *.md under it as a directory.
	return []string{p, p + "/**/*.md"}
}

func isGlob(p string) bool {
	return strings.ContainsAny(p, "*?[{")
}

// matches reports whether a repo-relative file path is selected by the source.
func (s Source) matches(rel string) bool {
	rel = strings.TrimPrefix(rel, "/")
	for _, pat := range s.matchPatterns() {
		if ok, err := doublestar.Match(pat, rel); err == nil && ok {
			return true
		}
	}
	return false
}

// namePriority ranks a matched file for collision resolution when several files
// normalize to the same install name. Lower is preferred: a file that already
// ends in ".instructions.md" wins over a ".md" file, which wins over anything
// else (a bare or other-suffixed name). Ties are broken lexicographically by the
// repo-relative path, so the outcome is always deterministic.
func namePriority(rel string) int {
	switch {
	case strings.HasSuffix(rel, ".instructions.md"):
		return 0
	case strings.HasSuffix(rel, ".md"):
		return 1
	default:
		return 2
	}
}

// instructionsName normalizes a file's base name to a clean ".instructions.md"
// form (the suffix Copilot requires to auto-load it): drop a trailing ".md",
// then a trailing ".instructions", then append ".instructions.md". This is
// idempotent and yields the tidiest possible names:
//
//	ruby.instructions.md -> ruby.instructions.md
//	ruby.md              -> ruby.instructions.md
//	ruby                 -> ruby.instructions.md
//
// Two distinct source names can now normalize to the same result (e.g. "ruby.md"
// and "ruby.instructions.md"); callers detect that collision and keep just one.
func instructionsName(name string) string {
	base := strings.TrimSuffix(name, ".md")
	base = strings.TrimSuffix(base, ".instructions")
	return base + ".instructions.md"
}

// DestPath returns the install path (relative to ~/.copilot/instructions, with
// forward slashes) for a source + matched repo-relative path. It preserves the
// repo's directory structure under a per-source namespace and ensures the file
// ends in ".instructions.md":
//
//	gh-copilot-instructions/<slug>/<repo-relative-dir>/<name>.instructions.md
//
// Returns "" for an unsafe path (one containing a ".." component).
func (s Source) DestPath(rel string) string {
	rel = strings.TrimPrefix(rel, "/")
	dir, file := path.Split(rel)
	clean := path.Clean(dir)
	if clean == "." {
		clean = ""
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == ".." {
			return ""
		}
	}
	return path.Join(FileDir, s.ID(), clean, instructionsName(file))
}
