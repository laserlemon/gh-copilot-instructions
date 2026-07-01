package gci

import "testing"

func TestParseSpec(t *testing.T) {
	cases := []struct {
		in              string
		repo, ref, path string
		wantErr         bool
	}{
		{in: "laserlemon/my-instructions", repo: "laserlemon/my-instructions"},
		{in: "acme/standards@main", repo: "acme/standards", ref: "main"},
		{in: "acme/standards@release/2026", repo: "acme/standards", ref: "release/2026"},
		{in: "o/r:instructions/*.md", repo: "o/r", path: "instructions/*.md"},
		{in: "o/r@main:**/*.instructions.md", repo: "o/r", ref: "main", path: "**/*.instructions.md"},
		{in: "o/r:/leading/slash.md", repo: "o/r", path: "leading/slash.md"},
		{in: "o/r@feat/x:dir/**", repo: "o/r", ref: "feat/x", path: "dir/**"},
		{in: "noslash", wantErr: true},
		{in: "", wantErr: true},
		{in: "a/b/c", wantErr: true},
	}
	for _, c := range cases {
		s, err := ParseSpec(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseSpec(%q): expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseSpec(%q): %v", c.in, err)
			continue
		}
		if s.Repo != c.repo || s.Ref != c.ref || s.Path != c.path {
			t.Errorf("ParseSpec(%q) = {%q,%q,%q}, want {%q,%q,%q}", c.in, s.Repo, s.Ref, s.Path, c.repo, c.ref, c.path)
		}
	}
}

func TestParseLine(t *testing.T) {
	s, ok, err := ParseLine("  o/r@main:**/*.md   ghp_TOKEN123  ")
	if err != nil || !ok {
		t.Fatalf("ParseLine err=%v ok=%v", err, ok)
	}
	if s.Repo != "o/r" || s.Ref != "main" || s.Path != "**/*.md" || s.Token != "ghp_TOKEN123" {
		t.Fatalf("got %+v", s)
	}
	for _, blank := range []string{"", "   ", "# comment", "  # c"} {
		if _, ok, _ := ParseLine(blank); ok {
			t.Errorf("ParseLine(%q): expected skip", blank)
		}
	}
}

func TestIDDeterministicAndUnique(t *testing.T) {
	a, _ := ParseSpec("o/r")
	b, _ := ParseSpec("o/r")
	if a.ID() != b.ID() {
		t.Errorf("same spec -> different id: %s vs %s", a.ID(), b.ID())
	}
	if len(a.ID()) != 8 {
		t.Errorf("id length = %d, want 8", len(a.ID()))
	}
	// IDs are base36 (lowercase 0-9a-z), not hex, so they don't read like
	// commit SHAs and stay stable on case-insensitive file systems.
	for _, r := range a.ID() {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'z')) {
			t.Errorf("id %q has non-base36 char %q", a.ID(), r)
		}
	}
	seen := map[string]string{}
	for _, spec := range []string{"o/r", "o/r@main", "o/r:p", "o/r2", "o2/r", "o/r@main:p"} {
		s, _ := ParseSpec(spec)
		if prev, ok := seen[s.ID()]; ok {
			t.Errorf("id collision: %q and %q -> %s", prev, spec, s.ID())
		}
		seen[s.ID()] = spec
	}
}

func TestRoundTripLine(t *testing.T) {
	s, _ := ParseSpec("o/r@main:**/*.instructions.md")
	s.Token = "tok"
	again, ok, err := ParseLine(s.Line())
	if err != nil || !ok {
		t.Fatalf("re-parse: err=%v ok=%v", err, ok)
	}
	if again.ID() != s.ID() || again.Token != "tok" {
		t.Fatalf("round-trip mismatch: %+v vs %+v", again, s)
	}
}

func TestMatches(t *testing.T) {
	def, _ := ParseSpec("o/r") // default **/*.instructions.md
	yes := []string{"general.instructions.md", "a/b/ruby.instructions.md"}
	no := []string{"README.md", ".github/copilot-instructions.md", "notes.txt"}
	for _, p := range yes {
		if !def.matches(p) {
			t.Errorf("default should match %q", p)
		}
	}
	for _, p := range no {
		if def.matches(p) {
			t.Errorf("default should NOT match %q", p)
		}
	}

	glob, _ := ParseSpec("o/r:instructions/*.md")
	if !glob.matches("instructions/foo.md") || glob.matches("instructions/sub/bar.md") {
		t.Errorf("single-star glob behaved unexpectedly")
	}

	dir, _ := ParseSpec("o/r:rules")
	if !dir.matches("rules/a.md") || !dir.matches("rules/x/y.md") {
		t.Errorf("dir path should match nested *.md")
	}

	file, _ := ParseSpec("o/r:docs/one.instructions.md")
	if !file.matches("docs/one.instructions.md") {
		t.Errorf("exact file path should match itself")
	}
}

func TestDestPath(t *testing.T) {
	s, _ := ParseSpec("o/r")
	id := s.ID()
	cases := []struct {
		rel  string
		want string
	}{
		// Already-correct names are preserved verbatim (common case).
		{"instructions/ruby.instructions.md", FileDir + "/" + id + "/instructions/ruby.instructions.md"},
		// A plain .md file is normalized to a clean ".instructions.md" name.
		{"commit-messages.md", FileDir + "/" + id + "/commit-messages.instructions.md"},
		// Extensionless files get the full suffix.
		{"AGENTS", FileDir + "/" + id + "/AGENTS.instructions.md"},
		// Deep directory structure is preserved.
		{"a/b/c/x.instructions.md", FileDir + "/" + id + "/a/b/c/x.instructions.md"},
		// A leading slash is normalized away.
		{"/top.instructions.md", FileDir + "/" + id + "/top.instructions.md"},
	}
	for _, c := range cases {
		if got := s.DestPath(c.rel); got != c.want {
			t.Errorf("DestPath(%q) = %q, want %q", c.rel, got, c.want)
		}
	}
	// Paths that escape the namespace are rejected.
	if got := s.DestPath("../evil.instructions.md"); got != "" {
		t.Errorf("DestPath with .. = %q, want \"\"", got)
	}
}

func TestIsOurs(t *testing.T) {
	s, _ := ParseSpec("o/r")
	// New nested layout and a legacy flat file are both recognized as ours.
	ours := []string{
		s.DestPath("x.instructions.md"),
		FileDir + ".abc12345.x.instructions.md",
	}
	for _, n := range ours {
		if !isOurs(n) {
			t.Errorf("%q should be recognized as ours", n)
		}
	}
	for _, n := range []string{"my-own.instructions.md", "random.md", "gh-copilot-instructions.md"} {
		if isOurs(n) {
			t.Errorf("%q should NOT be considered ours", n)
		}
	}
}

func TestRefPinsTo(t *testing.T) {
	const sha = "e28eb6df72fb90a84015cb6fda9104bff345ae48"
	cases := []struct {
		ref  string
		want bool
	}{
		{"e28eb6d", true}, // ≥7 hex, prefix
		{"e28eb6df72fb90a84015cb6fda9104bff345ae48", true}, // full SHA
		{"E28EB6D", true},  // case-insensitive
		{"e28eb6", false},  // only 6 hex digits
		{"345ae48", false}, // hex, but a suffix
		{"main", false},    // not hex
		{"", false},        // empty (default branch)
		{"deadbee", false}, // ≥7 hex but not a prefix
	}
	for _, c := range cases {
		if got := refPinsTo(c.ref, sha); got != c.want {
			t.Errorf("refPinsTo(%q, sha) = %v, want %v", c.ref, got, c.want)
		}
	}
	// No recorded SHA yet => never pins.
	if refPinsTo("e28eb6d", "") {
		t.Error("refPinsTo with empty sha should be false")
	}
}

// TestParseSpecGitHubURL covers accepting a GitHub blob URL as a source.
func TestParseSpecGitHubURL(t *testing.T) {
	cases := []struct{ spec, repo, ref, path string }{
		{"https://github.com/o/r/blob/main/path/to/file.md", "o/r", "main", "path/to/file.md"},
		{"https://github.com/o/r/blob/-/instructions/x.md", "o/r", "", "instructions/x.md"},
		{"github.com/o/r", "o/r", "", ""},
		{"https://github.com/o/r/blob/main/file.md#L5", "o/r", "main", "file.md"},
		{"https://github.com/o/r/", "o/r", "", ""},
	}
	for _, c := range cases {
		s, err := ParseSpec(c.spec)
		if err != nil {
			t.Fatalf("ParseSpec(%q): %v", c.spec, err)
		}
		if s.Repo != c.repo || s.Ref != c.ref || s.Path != c.path {
			t.Errorf("ParseSpec(%q) = {repo:%q ref:%q path:%q}, want {%q %q %q}", c.spec, s.Repo, s.Ref, s.Path, c.repo, c.ref, c.path)
		}
	}
}

func TestParseSpecGitHubURLErrors(t *testing.T) {
	for _, spec := range []string{
		"https://github.com/o/r/tree/main/instructions",
		"https://github.com/o/r/raw/main/x.md",
		"https://github.com/onlyowner",
	} {
		if _, err := ParseSpec(spec); err == nil {
			t.Errorf("ParseSpec(%q) should error", spec)
		}
	}
}

// TestParseSpecRefAndPath covers the "owner/repo[@ref][:path]" spec forms.
func TestParseSpecRefAndPath(t *testing.T) {
	cases := []struct{ spec, repo, ref, path string }{
		{"o/r", "o/r", "", ""},
		{"o/r:instructions", "o/r", "", "instructions"},
		{"o/r:instructions/topics", "o/r", "", "instructions/topics"},
		{"o/r@v1.2.0:docs/x.instructions.md", "o/r", "v1.2.0", "docs/x.instructions.md"},
		{"o/r:/leading/slash", "o/r", "", "leading/slash"},
	}
	for _, c := range cases {
		s, err := ParseSpec(c.spec)
		if err != nil {
			t.Fatalf("ParseSpec(%q): %v", c.spec, err)
		}
		if s.Repo != c.repo || s.Ref != c.ref || s.Path != c.path {
			t.Errorf("ParseSpec(%q) = {repo:%q ref:%q path:%q}, want {%q %q %q}", c.spec, s.Repo, s.Ref, s.Path, c.repo, c.ref, c.path)
		}
	}
}

// TestSourceMatchesScopesToPath verifies a :path narrows the pull: a directory
// takes every *.md under it, a single file takes only itself, the default takes
// **/*.instructions.md.
func TestSourceMatchesScopesToPath(t *testing.T) {
	dir := Source{Repo: "o/r", Path: "instructions/topics"}
	for _, rel := range []string{"instructions/topics/a.instructions.md", "instructions/topics/sub/b.md"} {
		if !dir.matches(rel) {
			t.Errorf("dir path should match %q", rel)
		}
	}
	for _, rel := range []string{"instructions/general.instructions.md", "other/c.instructions.md", "README.md"} {
		if dir.matches(rel) {
			t.Errorf("dir path should NOT match %q", rel)
		}
	}
	file := Source{Repo: "o/r", Path: "instructions/general.instructions.md"}
	if !file.matches("instructions/general.instructions.md") {
		t.Error("single-file path should match the exact file")
	}
	if file.matches("instructions/other.instructions.md") {
		t.Error("single-file path should match only the exact file")
	}
	def := Source{Repo: "o/r"}
	if !def.matches("deep/x.instructions.md") {
		t.Error("default should match **/*.instructions.md")
	}
	if def.matches("deep/x.md") {
		t.Error("default should not match a plain .md")
	}
}
