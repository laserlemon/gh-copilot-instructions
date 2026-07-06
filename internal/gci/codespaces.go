package gci

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/text"
)

// CodespacesSecretName is the multiline Codespaces secret that carries the
// source list into a Codespace (the env-var form of the config; see config.go).
const CodespacesSecretName = EnvSources // "GH_COPILOT_INSTRUCTIONS"

// errCodespaceScope signals that the caller's gh auth lacks the `codespace`
// OAuth scope required to read or manage user Codespaces secrets. It is the
// central branch of the codespaces flow: the fix is `gh auth refresh -s
// codespace` (or a PAT fallback), surfaced by the detectors below.
var errCodespaceScope = errors.New("gh auth is missing the codespace scope")

// codespacesAPI abstracts the GitHub calls the codespaces commands make so the
// detectors are unit-testable without a network. The real implementation talks
// to the REST API via gh's resolved auth; tests inject a fake.
type codespacesAPI interface {
	// ListUserSecrets returns the caller's user Codespaces secrets keyed by
	// name, with each secret's last-updated time. It returns errCodespaceScope
	// when the token can't read them for lack of the `codespace` scope.
	ListUserSecrets() (map[string]time.Time, error)
}

// csAPI returns the App's codespaces API, defaulting to the real one (mirrors
// the lazy sched() accessor). Tests set a.CSAPI to a fake before calling.
func (a *App) csAPI() codespacesAPI {
	if a.CSAPI == nil {
		a.CSAPI = ghCodespacesAPI{}
	}
	return a.CSAPI
}

// ghCodespacesAPI is the real codespacesAPI, backed by gh's default REST auth.
type ghCodespacesAPI struct{}

func (ghCodespacesAPI) ListUserSecrets() (map[string]time.Time, error) {
	client, err := api.DefaultRESTClient()
	if err != nil {
		return nil, err
	}
	var resp struct {
		Secrets []struct {
			Name      string    `json:"name"`
			UpdatedAt time.Time `json:"updated_at"`
		} `json:"secrets"`
	}
	if err := client.Get("user/codespaces/secrets?per_page=100", &resp); err != nil {
		var httpErr *api.HTTPError
		if errors.As(err, &httpErr) && (httpErr.StatusCode == 401 || httpErr.StatusCode == 403) {
			// The endpoint is gated on the `codespace` scope; a token without it
			// is rejected before the secret list is ever consulted.
			return nil, errCodespaceScope
		}
		return nil, err
	}
	out := make(map[string]time.Time, len(resp.Secrets))
	for _, s := range resp.Secrets {
		out[s.Name] = s.UpdatedAt
	}
	return out, nil
}

// Check statuses. These drive both the icon (TTY) and the JSON `status` field.
const (
	checkPass = "pass" // satisfied
	checkFail = "fail" // an actionable gap; makes the overall result not-ready
	checkWarn = "warn" // a soft problem or a thing we can't fully verify
	checkSkip = "skip" // not evaluated because a prior step blocks it
)

// checkStep is one row of the readiness report.
type checkStep struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"` // a copy-paste command that resolves a fail
}

// checkResult is the whole report. Location is "local" (run on your machine,
// checking what can be verified from here) or "codespace" (run inside a
// Codespace, checking whether it actually worked here).
type checkResult struct {
	Location string      `json:"location"`
	Ready    bool        `json:"ready"`
	Steps    []checkStep `json:"steps"`
}

// inCodespace reports whether we're running inside a Codespace. GitHub sets
// CODESPACES=true in the container environment.
func inCodespace() bool { return os.Getenv("CODESPACES") == "true" }

// CodespacesCheck runs the read-only readiness doctor and renders it. It never
// writes anything. Exit status is always success in this phase - readiness is
// reported in the output (and the JSON `ready` field), not the exit code.
func (a *App) CodespacesCheck(asJSON bool) error {
	var res checkResult
	if inCodespace() {
		res = a.checkInCodespace()
	} else {
		res = a.checkLocal()
	}
	if asJSON {
		return a.writeJSON(res)
	}
	a.printCheck(res)
	return nil
}

// checkLocal builds the laptop-side report: sources present, the codespace
// scope, the secret's existence, and config drift (via the local signature).
func (a *App) checkLocal() checkResult {
	var steps []checkStep

	// 1. Local sources - is there anything to publish?
	srcs, origin, _ := a.Paths.LoadSources()
	if origin == OriginNone || len(srcs) == 0 {
		steps = append(steps, checkStep{
			ID: "sources", Label: "Sources", Status: checkFail,
			Detail: "no sources configured",
			Fix:    "gh copilot-instructions add <owner/repo>",
		})
	} else {
		steps = append(steps, checkStep{
			ID: "sources", Label: "Sources", Status: checkPass,
			Detail: fmt.Sprintf("%s configured", text.Pluralize(len(srcs), "source")),
		})
	}

	// 2. The `codespace` scope - can we manage the user secret at all?
	secrets, err := a.csAPI().ListUserSecrets()
	scoped := err == nil
	switch {
	case errors.Is(err, errCodespaceScope):
		steps = append(steps, checkStep{
			ID: "scope", Label: "Secret access", Status: checkFail,
			Detail: "gh auth is missing the codespace scope",
			Fix:    "gh auth refresh -s codespace",
		})
	case err != nil:
		steps = append(steps, checkStep{
			ID: "scope", Label: "Secret access", Status: checkFail,
			Detail: err.Error(),
		})
	default:
		steps = append(steps, checkStep{
			ID: "scope", Label: "Secret access", Status: checkPass,
			Detail: "codespace scope present",
		})
	}

	// 3. The secret itself - is GH_COPILOT_INSTRUCTIONS set? (Only checkable
	// once we have the scope; skip honestly otherwise.)
	secretPresent := false
	switch {
	case !scoped:
		steps = append(steps, checkStep{
			ID: "secret", Label: "Secret", Status: checkSkip,
			Detail: "needs the codespace scope",
		})
	default:
		if updated, ok := secrets[CodespacesSecretName]; ok {
			secretPresent = true
			steps = append(steps, checkStep{
				ID: "secret", Label: "Secret", Status: checkPass,
				Detail: fmt.Sprintf("%s set %s", CodespacesSecretName, relTime(updated)),
			})
		} else {
			steps = append(steps, checkStep{
				ID: "secret", Label: "Secret", Status: checkFail,
				Detail: fmt.Sprintf("%s is not set", CodespacesSecretName),
				Fix:    "gh copilot-instructions codespaces setup",
			})
		}
	}

	// 4. Config drift - does the pushed secret still match local sources?
	steps = append(steps, a.driftStep(srcs, secretPresent))

	return checkResult{Location: "local", Ready: allClear(steps), Steps: steps}
}

// driftStep compares the current ConfigSignature against the one this machine
// recorded at its last push. It can only run when a secret exists; and it can
// only be exact when this machine is the one that pushed (otherwise we have no
// local record and say so).
func (a *App) driftStep(srcs []Source, secretPresent bool) checkStep {
	if !secretPresent {
		return checkStep{ID: "drift", Label: "Config sync", Status: checkSkip,
			Detail: "no secret to compare against"}
	}
	st, err := a.Paths.LoadState()
	if err != nil || st.Codespaces == nil || st.Codespaces.SecretSignature == "" {
		return checkStep{ID: "drift", Label: "Config sync", Status: checkWarn,
			Detail: "the secret wasn't pushed from this machine, so its contents can't be verified here"}
	}
	if st.Codespaces.SecretSignature == ConfigSignature(srcs) {
		return checkStep{ID: "drift", Label: "Config sync", Status: checkPass,
			Detail: "the secret matches your current sources"}
	}
	return checkStep{ID: "drift", Label: "Config sync", Status: checkWarn,
		Detail: "your sources changed since you last pushed",
		Fix:    "gh copilot-instructions codespaces update"}
}

// checkInCodespace builds the in-container report: did the bootstrap actually
// land here? It checks the secret's presence in the environment and whether any
// instructions were installed.
func (a *App) checkInCodespace() checkResult {
	var steps []checkStep

	if os.Getenv(CodespacesSecretName) != "" {
		steps = append(steps, checkStep{
			ID: "secret", Label: "Secret", Status: checkPass,
			Detail: fmt.Sprintf("%s is present in this Codespace", CodespacesSecretName),
		})
	} else {
		steps = append(steps, checkStep{
			ID: "secret", Label: "Secret", Status: checkFail,
			Detail: fmt.Sprintf("%s is not set in this Codespace", CodespacesSecretName),
			Fix:    "set it from your machine: gh copilot-instructions codespaces setup",
		})
	}

	if n := a.installedFileCount(); n > 0 {
		steps = append(steps, checkStep{
			ID: "instructions", Label: "Instructions", Status: checkPass,
			Detail: fmt.Sprintf("%s installed under ~/.copilot/instructions", text.Pluralize(n, "file")),
		})
	} else {
		steps = append(steps, checkStep{
			ID: "instructions", Label: "Instructions", Status: checkWarn,
			Detail: "no instruction files installed yet",
			Fix:    "gh copilot-instructions pull",
		})
	}

	return checkResult{Location: "codespace", Ready: allClear(steps), Steps: steps}
}

// installedFileCount counts the instruction files this tool installed (the
// managed FileDir subtree under InstallDir). Best-effort: any error reads as 0.
func (a *App) installedFileCount() int {
	root := filepath.Join(a.Paths.InstallDir, FileDir)
	count := 0
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		if e.IsDir() {
			sub, err := os.ReadDir(filepath.Join(root, e.Name()))
			if err != nil {
				continue
			}
			count += len(sub)
		} else {
			count++
		}
	}
	return count
}

// allClear reports whether no step failed (warnings don't block readiness).
func allClear(steps []checkStep) bool {
	for _, s := range steps {
		if s.Status == checkFail {
			return false
		}
	}
	return true
}

// printCheck renders the readiness report in the tool's primary/secondary style:
// one aligned status line per step (icon + label + detail), with any fix shown
// as a muted line beneath the step it resolves, then a closing hint.
func (a *App) printCheck(res checkResult) {
	cs := a.cs()
	width := 0
	for _, s := range res.Steps {
		if len(s.Label) > width {
			width = len(s.Label)
		}
	}
	for _, s := range res.Steps {
		a.msg("%s %-*s  %s", checkIcon(cs, s.Status), width, s.Label, s.Detail)
		if s.Fix != "" {
			// Align the fix under the detail column: icon(1) + space(1) + label
			// width + the 2-space gap.
			a.dim("%*s%s", width+4, "", s.Fix)
		}
	}
	a.blank()
	if res.Ready {
		if res.Location == "codespace" {
			a.dim("This Codespace is ready.")
		} else {
			a.dim("You're ready for Codespaces.")
		}
		return
	}
	if res.Location == "local" {
		a.dim("Fix the items above, or run: gh copilot-instructions codespaces setup")
	} else {
		a.dim("Fix the items above to finish setting up this Codespace.")
	}
}

// checkIcon maps a step status to its colored status glyph, reusing the icon
// vocabulary of the rest of the tool (green ✓ / red ✗ / yellow ! / gray -).
func checkIcon(cs *ColorScheme, status string) string {
	switch status {
	case checkPass:
		return cs.Green("✓")
	case checkFail:
		return cs.Red("✗")
	case checkWarn:
		return cs.Yellow("!")
	default:
		return cs.Gray("-")
	}
}

// relTime renders a secret's updated-at as a relative phrase ("2 days ago"),
// or a plain fallback for a zero time.
func relTime(t time.Time) string {
	if t.IsZero() {
		return "at an unknown time"
	}
	return text.RelativeTimeAgo(time.Now(), t)
}
