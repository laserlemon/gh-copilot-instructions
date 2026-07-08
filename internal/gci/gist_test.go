package gci

import "testing"

func TestIsGistURL(t *testing.T) {
	yes := []string{
		"https://gist.github.com/octocat/aa5a315d61ae9438b18d",
		"http://gist.github.com/aa5a315d61ae9438b18d",
		"gist.github.com/aa5a315d61ae9438b18d",
		"  gist.github.com/aa5a315d61ae9438b18d  ",
	}
	for _, s := range yes {
		if !IsGistURL(s) {
			t.Errorf("IsGistURL(%q) = false, want true", s)
		}
	}
	no := []string{
		"gist/abc123", // bare form is owner/repo-shaped, not a URL
		"gist:abc123", // no longer a supported form
		"o/r",
		"https://github.com/o/r/blob/main/x.md",
		"github.com/o/r",
		"abc123",
		"",
	}
	for _, s := range no {
		if IsGistURL(s) {
			t.Errorf("IsGistURL(%q) = true, want false", s)
		}
	}
}

func TestParseGist(t *testing.T) {
	const id = "aa5a315d61ae9438b18d"
	const rev = "e28eb6df72fb90a84015cb6fda9104bff345ae48" // 40-hex version
	cases := []struct {
		in      string
		repo    string
		ref     string
		wantErr bool
	}{
		{in: "gist.github.com/" + id, repo: "gist/" + id},
		{in: "https://gist.github.com/" + id, repo: "gist/" + id},
		{in: "https://gist.github.com/octocat/" + id, repo: "gist/" + id},
		{in: "https://gist.github.com/octocat/" + id + "/" + rev, repo: "gist/" + id, ref: rev},
		{in: "https://gist.github.com/octocat/" + id + ".git", repo: "gist/" + id},
		{in: "https://gist.github.com/octocat/" + id + "#file-x-md", repo: "gist/" + id},
		// A non-40-hex trailing segment is not treated as a version.
		{in: "https://gist.github.com/octocat/" + id + "/raw", repo: "gist/" + id},
		// Errors: not a gist URL (the bare gist/<id> form is parsed elsewhere).
		{in: "gist:" + id, wantErr: true},
		{in: "gist/" + id, wantErr: true},
		{in: "", wantErr: true},
		{in: "o/r", wantErr: true},
	}
	for _, c := range cases {
		s, err := ParseGist(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseGist(%q): expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseGist(%q): %v", c.in, err)
			continue
		}
		if s.Repo != c.repo || s.Ref != c.ref {
			t.Errorf("ParseGist(%q) = {repo:%q ref:%q}, want {%q %q}", c.in, s.Repo, s.Ref, c.repo, c.ref)
		}
		if !s.IsGist() {
			t.Errorf("ParseGist(%q) should be a gist source", c.in)
		}
	}
}

// TestBareGistFormIsGist verifies the bare gist/<id> form parses through the
// ordinary owner/repo path (ParseRepo/ParseSpec) and is recognized as a gist -
// no gist-specific input parser needed, since github.com/gist is reserved.
func TestBareGistFormIsGist(t *testing.T) {
	const id = "aa5a315d61ae9438b18d"
	repo, err := ParseRepo("gist/" + id)
	if err != nil {
		t.Fatalf("ParseRepo(gist/<id>): %v", err)
	}
	if !repo.IsGist() || repo.GistID() != id {
		t.Fatalf("ParseRepo(gist/<id>): IsGist=%v GistID=%q", repo.IsGist(), repo.GistID())
	}
	spec, err := ParseSpec("gist/" + id + "@v1:*.md")
	if err != nil {
		t.Fatalf("ParseSpec(gist spec): %v", err)
	}
	if !spec.IsGist() || spec.Ref != "v1" || spec.Path != "*.md" {
		t.Fatalf("ParseSpec(gist spec) = %+v", spec)
	}
}

func TestGistIsGistAndID(t *testing.T) {
	s, _ := ParseRepo("gist/deadbeef")
	if !s.IsGist() || s.GistID() != "deadbeef" {
		t.Fatalf("IsGist=%v GistID=%q", s.IsGist(), s.GistID())
	}
	repo, _ := ParseSpec("o/r")
	if repo.IsGist() {
		t.Errorf("owner/repo source should not be a gist")
	}
}

// TestGistSpecRoundTrips verifies the canonical config line for a gist round-trips
// through ParseSpec/ParseLine (it is owner/repo-shaped, "gist/<id>"), so it keeps
// a stable id and reloads as the same gist source.
func TestGistSpecRoundTrips(t *testing.T) {
	s, _ := ParseGist("gist.github.com/aa5a315d61ae9438b18d")
	again, err := ParseSpec(s.Spec())
	if err != nil {
		t.Fatalf("ParseSpec(%q): %v", s.Spec(), err)
	}
	if again.ID() != s.ID() || !again.IsGist() || again.GistID() != s.GistID() {
		t.Fatalf("round-trip mismatch: %+v vs %+v", again, s)
	}
}

func TestGistPathAndVersion(t *testing.T) {
	latest := Source{Repo: "gist/abc"}
	if got := gistPath(latest); got != "gists/abc" {
		t.Errorf("gistPath(latest) = %q, want gists/abc", got)
	}
	pinned := Source{Repo: "gist/abc", Ref: "v1"}
	if got := gistPath(pinned); got != "gists/abc/v1" {
		t.Errorf("gistPath(pinned) = %q, want gists/abc/v1", got)
	}
	g := gistResponse{History: []struct {
		Version string `json:"version"`
	}{{Version: "sha1"}}}
	if got := gistVersion(g, ""); got != "sha1" {
		t.Errorf("gistVersion(history) = %q, want sha1", got)
	}
	if got := gistVersion(gistResponse{}, "ref1"); got != "ref1" {
		t.Errorf("gistVersion(no history) = %q, want ref1 (pinned ref)", got)
	}
}

// TestGistMatchesFlatFilenames confirms a gist's flat file names are selected by
// the same path rules as repo files: the default **/*.instructions.md matches a
// top-level instructions file, and a filename glob narrows within the gist.
func TestGistMatchesFlatFilenames(t *testing.T) {
	def := Source{Repo: "gist/abc"} // default path
	if !def.matches("ruby.instructions.md") {
		t.Error("default should match a flat *.instructions.md gist file")
	}
	if def.matches("notes.md") {
		t.Error("default should not match a plain .md gist file")
	}
	glob := Source{Repo: "gist/abc", Path: "*.md"}
	if !glob.matches("notes.md") || glob.matches("sub/deep.md") {
		t.Error("*.md filename glob behaved unexpectedly for a gist")
	}
}
