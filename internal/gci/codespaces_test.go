package gci

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeCSAPI is an in-memory codespacesAPI for exercising the detectors without a
// network.
type fakeCSAPI struct {
	secrets map[string]time.Time
	err     error
}

func (f fakeCSAPI) ListUserSecrets() (map[string]time.Time, error) {
	return f.secrets, f.err
}

// runCheck wires a fake API, runs the JSON check, and decodes the result.
func runCheck(t *testing.T, a *App) checkResult {
	t.Helper()
	if err := a.CodespacesCheck(true); err != nil {
		t.Fatalf("CodespacesCheck: %v", err)
	}
	var res checkResult
	if err := json.Unmarshal([]byte(outBuf(a)), &res); err != nil {
		t.Fatalf("decode check JSON: %v\n%s", err, outBuf(a))
	}
	return res
}

func stepByID(t *testing.T, res checkResult, id string) checkStep {
	t.Helper()
	for _, s := range res.Steps {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no step %q in result", id)
	return checkStep{}
}

func TestCheckLocalNoSourcesNoScope(t *testing.T) {
	t.Setenv("CODESPACES", "")
	a := newTestApp(t, &fakeFetcher{})
	a.CSAPI = fakeCSAPI{err: errCodespaceScope}

	res := runCheck(t, a)
	if res.Location != "local" {
		t.Fatalf("location = %q, want local", res.Location)
	}
	if res.Ready {
		t.Fatal("should not be ready with no sources and no scope")
	}
	if got := stepByID(t, res, "sources").Status; got != checkFail {
		t.Fatalf("sources status = %q, want fail", got)
	}
	scope := stepByID(t, res, "scope")
	if scope.Status != checkFail || scope.Fix != "gh auth refresh -s codespace" {
		t.Fatalf("scope step = %+v, want fail with refresh fix", scope)
	}
	// Secret and drift can't be evaluated without the scope/secret; they skip.
	if got := stepByID(t, res, "secret").Status; got != checkSkip {
		t.Fatalf("secret status = %q, want skip", got)
	}
	if got := stepByID(t, res, "drift").Status; got != checkSkip {
		t.Fatalf("drift status = %q, want skip", got)
	}
}

func TestCheckLocalSecretMissing(t *testing.T) {
	t.Setenv("CODESPACES", "")
	a := newTestApp(t, &fakeFetcher{})
	if err := a.Paths.AddSource(Source{Repo: "o/a"}); err != nil {
		t.Fatal(err)
	}
	a.CSAPI = fakeCSAPI{secrets: map[string]time.Time{}} // scoped, but no secret

	res := runCheck(t, a)
	if got := stepByID(t, res, "sources").Status; got != checkPass {
		t.Fatalf("sources status = %q, want pass", got)
	}
	if got := stepByID(t, res, "scope").Status; got != checkPass {
		t.Fatalf("scope status = %q, want pass", got)
	}
	secret := stepByID(t, res, "secret")
	if secret.Status != checkFail || secret.Fix != "gh copilot-instructions codespaces setup" {
		t.Fatalf("secret step = %+v, want fail with setup fix", secret)
	}
	if res.Ready {
		t.Fatal("should not be ready without the secret")
	}
}

func TestCheckLocalInSync(t *testing.T) {
	t.Setenv("CODESPACES", "")
	a := newTestApp(t, &fakeFetcher{})
	src := Source{Repo: "o/a"}
	if err := a.Paths.AddSource(src); err != nil {
		t.Fatal(err)
	}
	// Record the signature this machine "pushed".
	st, _ := a.Paths.LoadState()
	st.Codespaces = &CodespacesState{SecretSignature: ConfigSignature([]Source{src}), PushedAt: time.Now()}
	if err := a.Paths.Save(st); err != nil {
		t.Fatal(err)
	}
	a.CSAPI = fakeCSAPI{secrets: map[string]time.Time{CodespacesSecretName: time.Now().Add(-time.Hour)}}

	res := runCheck(t, a)
	if !res.Ready {
		t.Fatalf("should be ready when in sync; steps=%+v", res.Steps)
	}
	if got := stepByID(t, res, "drift").Status; got != checkPass {
		t.Fatalf("drift status = %q, want pass", got)
	}
}

func TestCheckLocalDriftMismatch(t *testing.T) {
	t.Setenv("CODESPACES", "")
	a := newTestApp(t, &fakeFetcher{})
	if err := a.Paths.AddSource(Source{Repo: "o/a"}); err != nil {
		t.Fatal(err)
	}
	// Recorded a *different* signature => sources changed since last push.
	st, _ := a.Paths.LoadState()
	st.Codespaces = &CodespacesState{SecretSignature: "stale", PushedAt: time.Now()}
	if err := a.Paths.Save(st); err != nil {
		t.Fatal(err)
	}
	a.CSAPI = fakeCSAPI{secrets: map[string]time.Time{CodespacesSecretName: time.Now()}}

	res := runCheck(t, a)
	drift := stepByID(t, res, "drift")
	if drift.Status != checkWarn || drift.Fix != "gh copilot-instructions codespaces update" {
		t.Fatalf("drift step = %+v, want warn with update fix", drift)
	}
	// A warning does not block readiness (all hard steps still pass).
	if !res.Ready {
		t.Fatalf("a drift warning should not make the result not-ready; steps=%+v", res.Steps)
	}
}

func TestCheckLocalSecretNotPushedFromHere(t *testing.T) {
	t.Setenv("CODESPACES", "")
	a := newTestApp(t, &fakeFetcher{})
	if err := a.Paths.AddSource(Source{Repo: "o/a"}); err != nil {
		t.Fatal(err)
	}
	// Secret exists, but no local record of a push (e.g. set from another machine).
	a.CSAPI = fakeCSAPI{secrets: map[string]time.Time{CodespacesSecretName: time.Now()}}

	res := runCheck(t, a)
	if got := stepByID(t, res, "drift").Status; got != checkWarn {
		t.Fatalf("drift status = %q, want warn (can't verify from here)", got)
	}
}

func TestCheckInCodespace(t *testing.T) {
	a := newTestApp(t, &fakeFetcher{})
	// Set env after newTestApp, which clears EnvSources (== CodespacesSecretName).
	t.Setenv("CODESPACES", "true")
	t.Setenv(CodespacesSecretName, "o/a")

	// Install one file so the instructions detector passes.
	dir := filepath.Join(a.Paths.InstallDir, FileDir, "abcd1234")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x.instructions.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := runCheck(t, a)
	if res.Location != "codespace" {
		t.Fatalf("location = %q, want codespace", res.Location)
	}
	if got := stepByID(t, res, "secret").Status; got != checkPass {
		t.Fatalf("secret status = %q, want pass", got)
	}
	if got := stepByID(t, res, "instructions").Status; got != checkPass {
		t.Fatalf("instructions status = %q, want pass", got)
	}
	if !res.Ready {
		t.Fatalf("should be ready in a set-up Codespace; steps=%+v", res.Steps)
	}
}

func TestCheckInCodespaceMissingSecret(t *testing.T) {
	a := newTestApp(t, &fakeFetcher{})
	t.Setenv("CODESPACES", "true")
	t.Setenv(CodespacesSecretName, "")

	res := runCheck(t, a)
	secret := stepByID(t, res, "secret")
	if secret.Status != checkFail {
		t.Fatalf("secret status = %q, want fail", secret.Status)
	}
	if got := stepByID(t, res, "instructions").Status; got != checkWarn {
		t.Fatalf("instructions status = %q, want warn", got)
	}
	if res.Ready {
		t.Fatal("should not be ready without the secret in-Codespace")
	}
}
