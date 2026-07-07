package gci

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestVSCodeUserDirs(t *testing.T) {
	// darwin
	got := vscodeUserDirs("darwin", "/Users/x", "")
	want := []string{
		"/Users/x/Library/Application Support/Code/User",
		"/Users/x/Library/Application Support/Code - Insiders/User",
		"/Users/x/Library/Application Support/VSCodium/User",
	}
	assertEqualPaths(t, "darwin", got, want)

	// linux
	got = vscodeUserDirs("linux", "/home/x", "")
	want = []string{
		"/home/x/.config/Code/User",
		"/home/x/.config/Code - Insiders/User",
		"/home/x/.config/VSCodium/User",
	}
	assertEqualPaths(t, "linux", got, want)

	// windows uses APPDATA; build want via filepath.Join so the comparison is
	// host-separator-agnostic (we only assert the component selection here).
	appData := filepath.Join(`C:\Users\x\AppData\Roaming`)
	got = vscodeUserDirs("windows", `C:\Users\x`, appData)
	want = []string{
		filepath.Join(appData, "Code", "User"),
		filepath.Join(appData, "Code - Insiders", "User"),
		filepath.Join(appData, "VSCodium", "User"),
	}
	assertEqualPaths(t, "windows", got, want)

	if dirs := vscodeUserDirs("windows", `C:\Users\x`, ""); len(dirs) != 0 {
		t.Fatalf("windows with empty APPDATA should yield no dirs, got %v", dirs)
	}
}

func assertEqualPaths(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %d dirs %v, want %d %v", label, len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s[%d] = %q, want %q", label, i, got[i], want[i])
		}
	}
}

// vsUserDir returns (and creates) the stable-Code User dir for the current OS
// under the sandboxed HOME, so detection fires. Skips on an OS we can't map.
func vsUserDir(t *testing.T) string {
	t.Helper()
	dirs := vscodeUserDirs(runtime.GOOS, homeDir(), os.Getenv("APPDATA"))
	if len(dirs) == 0 {
		t.Skipf("no VS Code user dir mapping for %s", runtime.GOOS)
	}
	if err := os.MkdirAll(dirs[0], 0o755); err != nil {
		t.Fatal(err)
	}
	return dirs[0]
}

func TestVSCodePromptDirsDetection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(EnvNoVSCode, "")

	// No VS Code installed yet -> no roots.
	if dirs := vscodePromptDirs(); len(dirs) != 0 {
		t.Fatalf("expected no prompt dirs before install, got %v", dirs)
	}

	// Create the User dir -> its prompts dir is now a target.
	vsUser := vsUserDir(t)
	dirs := vscodePromptDirs()
	if len(dirs) != 1 || dirs[0] != filepath.Join(vsUser, "prompts") {
		t.Fatalf("expected [%s], got %v", filepath.Join(vsUser, "prompts"), dirs)
	}

	// Opt-out disables detection entirely.
	t.Setenv(EnvNoVSCode, "1")
	if dirs := vscodePromptDirs(); len(dirs) != 0 {
		t.Fatalf("opt-out should disable, got %v", dirs)
	}
	// "0"/"false" are treated as not-opted-out.
	t.Setenv(EnvNoVSCode, "false")
	if dirs := vscodePromptDirs(); len(dirs) != 1 {
		t.Fatalf(`EnvNoVSCode="false" should not opt out, got %v`, dirs)
	}
}

func TestMirrorCopyUpdatePrune(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(EnvNoVSCode, "")
	vsUser := vsUserDir(t)
	a := &App{Paths: DefaultPaths()}

	canonical := filepath.Join(a.Paths.InstallDir, FileDir)
	mirror := filepath.Join(vsUser, "prompts", FileDir)
	write := func(root, rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Install two files, mirror, and verify both copied verbatim.
	write(canonical, "id1/a.instructions.md", "AAA")
	write(canonical, "id2/b.instructions.md", "BBB")
	if w := a.mirrorToVSCode(); len(w) != 0 {
		t.Fatalf("unexpected warnings: %v", w)
	}
	if b, _ := os.ReadFile(filepath.Join(mirror, "id1/a.instructions.md")); string(b) != "AAA" {
		t.Fatalf("a not mirrored, got %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(mirror, "id2/b.instructions.md")); string(b) != "BBB" {
		t.Fatalf("b not mirrored, got %q", b)
	}

	// Change a's content and remove b from canonical; mirror should update a and
	// prune b (and tidy id2's empty dir).
	write(canonical, "id1/a.instructions.md", "AAA2")
	os.RemoveAll(filepath.Join(canonical, "id2"))
	if w := a.mirrorToVSCode(); len(w) != 0 {
		t.Fatalf("unexpected warnings: %v", w)
	}
	if b, _ := os.ReadFile(filepath.Join(mirror, "id1/a.instructions.md")); string(b) != "AAA2" {
		t.Fatalf("a not updated, got %q", b)
	}
	if _, err := os.Stat(filepath.Join(mirror, "id2/b.instructions.md")); !os.IsNotExist(err) {
		t.Fatalf("b should be pruned, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(mirror, "id2")); !os.IsNotExist(err) {
		t.Fatalf("empty id2 dir should be tidied, stat err=%v", err)
	}

	// Remove everything canonical; mirror should drop the whole subtree.
	os.RemoveAll(canonical)
	if w := a.mirrorToVSCode(); len(w) != 0 {
		t.Fatalf("unexpected warnings: %v", w)
	}
	if _, err := os.Stat(mirror); !os.IsNotExist(err) {
		t.Fatalf("mirror subtree should be gone, stat err=%v", err)
	}
}

// TestAddMirrorsThroughDefer exercises the real Add path (with the deferred
// syncVSCode hook) to confirm a pulled source lands in both roots, and that a
// later Remove prunes the VS Code copy.
func TestAddMirrorsThroughDefer(t *testing.T) {
	src, _ := ParseSpec("o/r")
	id := src.ID()
	f := &fakeFetcher{
		sha: map[string]string{id: "sha1"},
		files: map[string][]FetchedFile{id: {
			{Rel: "a.instructions.md", Content: []byte("---\napplyTo: \"**\"\n---\nhi")},
		}},
	}
	a := newTestApp(t, f)
	t.Setenv(EnvNoVSCode, "")
	vsUser := vsUserDir(t)
	mirrorFile := filepath.Join(vsUser, "prompts", FileDir, id, "a.instructions.md")

	if err := a.Add(src, true); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Canonical copy present.
	if _, err := os.Stat(filepath.Join(a.Paths.InstallDir, FileDir, id, "a.instructions.md")); err != nil {
		t.Fatalf("canonical file missing: %v", err)
	}
	// VS Code mirror present (written by the deferred hook).
	if b, err := os.ReadFile(mirrorFile); err != nil || len(b) == 0 {
		t.Fatalf("VS Code mirror missing: err=%v", err)
	}

	// Removing the source prunes the VS Code copy too.
	if err := a.Remove("o/r", true); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(mirrorFile); !os.IsNotExist(err) {
		t.Fatalf("VS Code mirror should be pruned after remove, stat err=%v", err)
	}
}

func TestAddOptOutSkipsVSCode(t *testing.T) {
	src, _ := ParseSpec("o/r")
	id := src.ID()
	f := &fakeFetcher{
		sha:   map[string]string{id: "sha1"},
		files: map[string][]FetchedFile{id: {{Rel: "a.instructions.md", Content: []byte("hi")}}},
	}
	a := newTestApp(t, f)
	t.Setenv(EnvNoVSCode, "1")
	vsUser := vsUserDir(t)

	if err := a.Add(src, true); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if entries, err := os.ReadDir(filepath.Join(vsUser, "prompts")); err == nil && len(entries) > 0 {
		t.Fatalf("opt-out should not write to VS Code prompts, found %v", entries)
	}
}
