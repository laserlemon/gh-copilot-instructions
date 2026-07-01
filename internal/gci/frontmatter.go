package gci

import (
	"strings"
)

// hasApplyTo reports whether a file's content begins with a YAML frontmatter
// block that declares a non-empty top-level "applyTo" key.
//
// VS Code only auto-applies a user-level *.instructions.md file when the file
// itself carries an applyTo value, so a pulled file without one is installed but
// silently never applied there. We detect that (read-only; the file is still
// copied verbatim) so the caller can warn.
//
// The rule, kept deliberately simple and dependency-free:
//   - the content must start with a frontmatter fence: a line that is exactly
//     "---" (a leading UTF-8 BOM and surrounding whitespace are tolerated);
//   - scanning the block up to its closing fence ("---" or "..."), a top-level
//     line "applyTo: <value>" must have a non-empty scalar value once quotes and
//     inline comments are stripped ("applyTo: ”" therefore counts as empty).
//
// Nested keys (indented) and any "applyTo:" appearing in the document body after
// the block are ignored.
func hasApplyTo(content []byte) bool {
	s := strings.TrimPrefix(string(content), "\uFEFF")
	lines := strings.Split(s, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return false
	}
	found := false
	for _, raw := range lines[1:] {
		line := strings.TrimRight(raw, "\r")
		if t := strings.TrimSpace(line); t == "---" || t == "..." {
			return found // end of a properly-closed block
		}
		// Only top-level keys (no leading indentation) count.
		if line != strings.TrimLeft(line, " \t") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "applyTo:"); ok && applyToValueSet(rest) {
			found = true
		}
	}
	return false // no closing fence => malformed frontmatter, treat as missing
}

// applyToValueSet reports whether the text after "applyTo:" is a non-empty
// scalar: it strips an inline "# comment", trims whitespace and surrounding
// quotes, and treats what remains as the value.
func applyToValueSet(rest string) bool {
	v := strings.TrimSpace(rest)
	// Strip an inline comment only when it's clearly not inside a quoted value.
	if !strings.HasPrefix(v, "'") && !strings.HasPrefix(v, "\"") {
		if i := strings.IndexByte(v, '#'); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
	}
	v = strings.TrimSpace(strings.Trim(v, `'"`))
	return v != ""
}
