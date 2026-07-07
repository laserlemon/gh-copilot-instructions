package gci

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// EnvNoVSCode opts out of mirroring instructions into VS Code's prompts dir.
// Any non-empty value other than "0"/"false" disables it. VS Code reads
// user-scope instructions from its own User/prompts directory (not
// ~/.copilot/instructions), so by default we mirror our managed subtree there
// too; set this to keep the tool from touching VS Code's directories at all.
const EnvNoVSCode = "GH_COPILOT_INSTRUCTIONS_NO_VSCODE"

// vsCodeVariants are the VS Code application-support directory names we look
// for. Stable and Insiders share the same on-disk layout; VSCodium too.
var vsCodeVariants = []string{"Code", "Code - Insiders", "VSCodium"}

// vscodeOptOut reports whether the user disabled VS Code mirroring.
func vscodeOptOut() bool {
	v := strings.TrimSpace(os.Getenv(EnvNoVSCode))
	return v != "" && v != "0" && !strings.EqualFold(v, "false")
}

// vscodeUserDirs returns candidate "<app>/User" directories for the given OS.
// Pure (no I/O) so path construction is unit-testable across platforms. On
// Windows the profile lives under %APPDATA%; elsewhere it derives from home.
func vscodeUserDirs(goos, home, appData string) []string {
	var dirs []string
	for _, v := range vsCodeVariants {
		switch goos {
		case "darwin":
			dirs = append(dirs, filepath.Join(home, "Library", "Application Support", v, "User"))
		case "windows":
			if appData != "" {
				dirs = append(dirs, filepath.Join(appData, v, "User"))
			}
		default: // linux and other unix-likes
			dirs = append(dirs, filepath.Join(home, ".config", v, "User"))
		}
	}
	return dirs
}

// vscodePromptDirs returns the VS Code user "prompts" directories present on
// this machine - one per installed variant whose User directory already exists.
// These are secondary install roots: we mirror our managed subtree into
// <promptDir>/gh-copilot-instructions/ so VS Code (which reads User/prompts, not
// ~/.copilot/instructions) sees the same instructions.
//
// Only variants whose User directory already exists are returned, so we never
// create a VS Code profile from nothing and simply no-op on machines (and CI)
// without VS Code installed. Honors the EnvNoVSCode opt-out.
func vscodePromptDirs() []string {
	if vscodeOptOut() {
		return nil
	}
	var out []string
	for _, userDir := range vscodeUserDirs(runtime.GOOS, homeDir(), os.Getenv("APPDATA")) {
		if fi, err := os.Stat(userDir); err != nil || !fi.IsDir() {
			continue // that variant isn't installed / hasn't run
		}
		out = append(out, filepath.Join(userDir, "prompts"))
	}
	return out
}

// mirrorToVSCode makes each detected VS Code prompts directory's managed subtree
// identical to the canonical one under ~/.copilot/instructions. It is
// best-effort: the canonical copy is the source of truth, so a failure to write
// a given VS Code root is returned as a warning rather than failing the pull.
// Returns one warning per root that could not be fully synced.
//
// It runs after any command that mutates the canonical subtree (add, pull,
// remove, remove --all), so VS Code roots always reflect the final state -
// including populating a newly-installed VS Code, healing a hand-deleted file,
// and pruning removed sources - all from the local copy, with no network.
func (a *App) mirrorToVSCode() []string {
	src := filepath.Join(a.Paths.InstallDir, FileDir)
	var warnings []string
	for _, promptDir := range vscodePromptDirs() {
		dst := filepath.Join(promptDir, FileDir)
		if err := mirrorDir(src, dst); err != nil {
			warnings = append(warnings, fmt.Sprintf("VS Code: couldn't sync %s (%v)", promptDir, err))
		}
	}
	return warnings
}

// syncVSCode mirrors the canonical subtree into detected VS Code prompt dirs and
// surfaces any per-root failures as notes (suppressed for --json). Best-effort:
// it never affects the command's exit status.
func (a *App) syncVSCode(asJSON bool) {
	warnings := a.mirrorToVSCode()
	if asJSON {
		return
	}
	for _, w := range warnings {
		a.note("%s", w)
	}
}

// mirrorDir makes dst an exact copy of src (files only): every file under src is
// written into dst, and any file under dst not present in src is removed, then
// emptied directories are tidied. When src doesn't exist (nothing installed),
// dst is removed entirely. Only regular files are considered.
func mirrorDir(src, dst string) error {
	si, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			if rmErr := os.RemoveAll(dst); rmErr != nil && !os.IsNotExist(rmErr) {
				return rmErr
			}
			return nil
		}
		return err
	}
	if !si.IsDir() {
		return fmt.Errorf("%s is not a directory", src)
	}
	if err := copyTree(src, dst); err != nil {
		return err
	}
	return pruneTree(src, dst)
}

// copyTree copies every regular file under src into dst at the same relative
// path, creating parent directories as needed. A file is (re)written only when
// missing or its contents differ, so repeated syncs of unchanged instructions
// don't churn the filesystem.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if existing, err := os.ReadFile(target); err == nil && bytes.Equal(existing, content) {
			return nil // already in sync
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, content, 0o644)
	})
}

// pruneTree removes every regular file under dst that has no counterpart under
// src, then removes directories left empty. This deletes instructions for
// sources that were removed, and cleans up renamed files.
func pruneTree(src, dst string) error {
	if _, err := os.Stat(dst); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	// First pass: remove files with no source counterpart.
	err := filepath.Walk(dst, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dst, p)
		if err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(src, rel)); os.IsNotExist(err) {
			return os.Remove(p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Second pass: remove now-empty directories, deepest first, but keep dst.
	return removeEmptyDirs(dst)
}

// removeEmptyDirs removes empty directories under root (and root itself if it
// ends up empty), deepest first. Non-empty directories are left intact.
func removeEmptyDirs(root string) error {
	var dirs []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Deepest paths last in a walk; remove in reverse so children go first.
	for i := len(dirs) - 1; i >= 0; i-- {
		if entries, err := os.ReadDir(dirs[i]); err == nil && len(entries) == 0 {
			os.Remove(dirs[i]) // best-effort; a non-empty dir simply stays
		}
	}
	return nil
}
