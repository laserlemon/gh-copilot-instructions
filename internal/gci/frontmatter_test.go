package gci

import "testing"

func TestHasApplyTo(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"bare quoted glob", "---\napplyTo: '**'\n---\nbody", true},
		{"double quoted", "---\napplyTo: \"**/*.go\"\n---\n", true},
		{"unquoted value", "---\napplyTo: **\n---\n", true},
		{"with other keys", "---\ndescription: x\napplyTo: '**'\n---\n", true},
		{"closing dots fence", "---\napplyTo: '**'\n...\n", true},
		{"crlf line endings", "---\r\napplyTo: '**'\r\n---\r\n", true},
		{"leading BOM", "\uFEFF---\napplyTo: '**'\n---\n", true},
		{"value then comment", "---\napplyTo: '**' # everything\n---\n", true},

		{"no frontmatter", "just a body\napplyTo: '**'\n", false},
		{"empty single quotes", "---\napplyTo: ''\n---\n", false},
		{"empty double quotes", "---\napplyTo: \"\"\n---\n", false},
		{"key present no value", "---\napplyTo:\n---\n", false},
		{"only a comment", "---\napplyTo: # nothing\n---\n", false},
		{"indented (nested) key", "---\nnested:\n  applyTo: '**'\n---\n", false},
		{"no closing fence", "---\napplyTo: '**'\nstill in block", false},
		{"empty file", "", false},
		{"applyTo only in body", "---\ndescription: x\n---\napplyTo: '**'\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hasApplyTo([]byte(c.in)); got != c.want {
				t.Fatalf("hasApplyTo(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
