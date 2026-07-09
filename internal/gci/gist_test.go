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
		"gist:abc123", // the bare shorthand is a spec, not a URL
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
		{in: "gist.github.com/" + id, repo: "gist:" + id},
		{in: "https://gist.github.com/" + id, repo: "gist:" + id},
		{in: "https://gist.github.com/octocat/" + id, repo: "gist:" + id},
		{in: "https://gist.github.com/octocat/" + id + "/" + rev, repo: "gist:" + id, ref: rev},
		{in: "https://gist.github.com/octocat/" + id + ".git", repo: "gist:" + id},
		{in: "https://gist.github.com/octocat/" + id + "#file-x-md", repo: "gist:" + id},
		// A non-40-hex trailing segment is not treated as a version.
		{in: "https://gist.github.com/octocat/" + id + "/raw", repo: "gist:" + id},
		// Errors: not a gist URL (the bare gist:<id> shorthand is parsed elsewhere).
		{in: "gist:" + id, wantErr: true},
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

// TestParseGistArg covers the bare "gist:<id>" CLI shorthand: it yields a gist
// source, and (like ParseRepo) rejects an inline @ref or :path in favor of the
// --ref/--path flags.
func TestParseGistArg(t *testing.T) {
	const id = "aa5a315d61ae9438b18d"
	s, err := ParseGistArg("gist:" + id)
	if err != nil {
		t.Fatalf("ParseGistArg(gist:<id>): %v", err)
	}
	if !s.IsGist() || s.GistID() != id || s.Repo != "gist:"+id {
		t.Fatalf("ParseGistArg(gist:<id>) = %+v", s)
	}
	for _, bad := range []string{"gist:" + id + "@v1", "gist:" + id + ":*.md", "gist:", "gist:no/slash"} {
		if _, err := ParseGistArg(bad); err == nil {
			t.Errorf("ParseGistArg(%q): expected error", bad)
		}
	}
	if !IsGistSpec("gist:abc") || IsGistSpec("o/r") || IsGistSpec("abc123") {
		t.Errorf("IsGistSpec routing is wrong")
	}
}

func TestGistIsGistAndID(t *testing.T) {
	s, _ := ParseGistArg("gist:deadbeef")
	if !s.IsGist() || s.GistID() != "deadbeef" {
		t.Fatalf("IsGist=%v GistID=%q", s.IsGist(), s.GistID())
	}
	repo, _ := ParseSpec("o/r")
	if repo.IsGist() {
		t.Errorf("owner/repo source should not be a gist")
	}
	// "gist/abc" is now an ordinary owner/repo (owner "gist"), not a gist.
	notGist, err := ParseSpec("gist/abc")
	if err != nil || notGist.IsGist() {
		t.Errorf("gist/abc should parse as an ordinary repo, not a gist: %+v (err %v)", notGist, err)
	}
}

// TestGistSpecRoundTrips verifies the canonical config line for a gist round-trips
// through ParseSpec (special-cased on the "gist:" scheme), keeping a stable id and
// carrying an inline version/glob back and forth.
func TestGistSpecRoundTrips(t *testing.T) {
	s, _ := ParseGist("gist.github.com/aa5a315d61ae9438b18d")
	s.Ref = "e28eb6df72fb90a84015cb6fda9104bff345ae48"
	s.Path = "*.md"
	again, err := ParseSpec(s.Spec())
	if err != nil {
		t.Fatalf("ParseSpec(%q): %v", s.Spec(), err)
	}
	if again.ID() != s.ID() || !again.IsGist() || again.GistID() != s.GistID() ||
		again.Ref != s.Ref || again.Path != s.Path {
		t.Fatalf("round-trip mismatch: %+v vs %+v", again, s)
	}
}

// TestGistDisplay covers the human-facing identifier: "<owner>/gist:<id>" when the
// owner is known, falling back to the stored "gist:<id>" otherwise; a repo ignores
// the owner argument.
func TestGistDisplay(t *testing.T) {
	g := Source{Repo: "gist:abc"}
	if got := g.Display("octocat"); got != "octocat/gist:abc" {
		t.Errorf("Display(owner) = %q, want octocat/gist:abc", got)
	}
	if got := g.Display(""); got != "gist:abc" {
		t.Errorf("Display(anonymous) = %q, want gist:abc", got)
	}
	r := Source{Repo: "o/r"}
	if got := r.Display("ignored"); got != "o/r" {
		t.Errorf("Display(repo) = %q, want o/r", got)
	}
}

func TestGistOwner(t *testing.T) {
	if got := gistOwner(gistResponse{}); got != "" {
		t.Errorf("gistOwner(anonymous) = %q, want empty", got)
	}
	g := gistResponse{Owner: &struct {
		Login string `json:"login"`
	}{Login: "octocat"}}
	if got := gistOwner(g); got != "octocat" {
		t.Errorf("gistOwner = %q, want octocat", got)
	}
}

func TestGistFileURL(t *testing.T) {
	const id = "aa5a315d61ae9438b18d"
	if got := gistFileURL(id, "sha1", "", "Setup.instructions.md"); got != "https://gist.github.com/"+id+"/sha1#file-setup-instructions-md" {
		t.Errorf("gistFileURL(pinned) = %q", got)
	}
	if got := gistFileURL(id, "", "", "notes.md"); got != "https://gist.github.com/"+id+"#file-notes-md" {
		t.Errorf("gistFileURL(latest) = %q", got)
	}
}

func TestGistPathAndVersion(t *testing.T) {
	latest := Source{Repo: "gist:abc"}
	if got := gistPath(latest); got != "gists/abc" {
		t.Errorf("gistPath(latest) = %q, want gists/abc", got)
	}
	pinned := Source{Repo: "gist:abc", Ref: "v1"}
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
	def := Source{Repo: "gist:abc"} // default path
	if !def.matches("ruby.instructions.md") {
		t.Error("default should match a flat *.instructions.md gist file")
	}
	if def.matches("notes.md") {
		t.Error("default should not match a plain .md gist file")
	}
	glob := Source{Repo: "gist:abc", Path: "*.md"}
	if !glob.matches("notes.md") || glob.matches("sub/deep.md") {
		t.Error("*.md filename glob behaved unexpectedly for a gist")
	}
}
