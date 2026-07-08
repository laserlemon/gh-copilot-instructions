package gci

import "testing"

func TestLiteralTreePrefix(t *testing.T) {
	cases := []struct {
		patterns []string
		want     string
	}{
		{[]string{"docs/*.md"}, "docs"},
		{[]string{"docs/**/*.instructions.md"}, "docs"},
		{[]string{"a/b/**/*.md"}, "a/b"},
		{[]string{"**/*.instructions.md"}, ""},
		{[]string{"*.md"}, ""},
		// A plain path expands to [exact-file, dir-glob]; their common literal
		// root is the parent directory (empty for a top-level name).
		{[]string{"docs", "docs/**/*.md"}, ""},
		{[]string{"a/b/c.md", "a/b/c.md/**/*.md"}, "a/b"},
		// A brace/charclass segment is globby and stops the prefix.
		{[]string{"docs/{a,b}/*.md"}, "docs"},
	}
	for _, c := range cases {
		if got := literalTreePrefix(c.patterns); got != c.want {
			t.Errorf("literalTreePrefix(%v) = %q, want %q", c.patterns, got, c.want)
		}
	}
}

// TestLiteralTreePrefixMatchesEffectivePath ties the prefix back to real sources:
// a tree-URL-style source scopes to its directory, while a bare glob does not.
func TestLiteralTreePrefixMatchesEffectivePath(t *testing.T) {
	if got := literalTreePrefix(Source{Path: "docs/*.md"}.matchPatterns()); got != "docs" {
		t.Errorf("docs/*.md prefix = %q, want docs", got)
	}
	if got := literalTreePrefix(Source{}.matchPatterns()); got != "" {
		t.Errorf("default path prefix = %q, want empty", got)
	}
}
