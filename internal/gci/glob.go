package gci

import (
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

// destName maps a matched repo-relative path to the installed filename's
// "<name>" segment: path separators become dashes and the trailing
// ".instructions.md"/".md" suffix is dropped.
func destName(rel string) string {
	rel = strings.TrimPrefix(rel, "/")
	switch {
	case strings.HasSuffix(rel, ".instructions.md"):
		rel = strings.TrimSuffix(rel, ".instructions.md")
	case strings.HasSuffix(rel, ".md"):
		rel = strings.TrimSuffix(rel, ".md")
	}
	rel = strings.ReplaceAll(rel, "/", "-")
	var b strings.Builder
	for _, r := range rel {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// DestFile returns the installed filename for a source + matched repo path,
// e.g. "gh-copilot-instructions.a1b2c3d4.instructions-ruby.instructions.md".
func (s Source) DestFile(rel string) string {
	return FilePrefix + "." + s.ID() + "." + destName(rel) + ".instructions.md"
}
