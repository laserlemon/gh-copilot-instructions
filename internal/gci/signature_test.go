package gci

import "testing"

func TestConfigSignature(t *testing.T) {
	// Order-independence: the same set in a different order signs identically.
	a := []Source{{Repo: "o/a"}, {Repo: "o/b"}}
	b := []Source{{Repo: "o/b"}, {Repo: "o/a"}}
	if ConfigSignature(a) != ConfigSignature(b) {
		t.Fatal("signature should be independent of source ordering")
	}

	// Default-path equivalence: an omitted path and the explicit default glob
	// fetch identically, so they must sign identically.
	bare := []Source{{Repo: "o/a"}}
	explicit := []Source{{Repo: "o/a", Path: DefaultPath}}
	if ConfigSignature(bare) != ConfigSignature(explicit) {
		t.Fatal("empty path and DefaultPath should sign identically")
	}

	// A distinct non-default path is a different config.
	other := []Source{{Repo: "o/a", Path: "docs/*.md"}}
	if ConfigSignature(bare) == ConfigSignature(other) {
		t.Fatal("a different path should change the signature")
	}

	// Ref and token are part of fetch identity; changing either changes it.
	if ConfigSignature([]Source{{Repo: "o/a"}}) == ConfigSignature([]Source{{Repo: "o/a", Ref: "main"}}) {
		t.Fatal("adding a ref should change the signature")
	}
	if ConfigSignature([]Source{{Repo: "o/a"}}) == ConfigSignature([]Source{{Repo: "o/a", Token: "t"}}) {
		t.Fatal("adding a token should change the signature")
	}

	// Determinism and shape: stable across calls, lowercase 64-char hex.
	sig := ConfigSignature(a)
	if sig != ConfigSignature(a) {
		t.Fatal("signature should be deterministic")
	}
	if len(sig) != 64 {
		t.Fatalf("signature should be 64 hex chars, got %d", len(sig))
	}
	for _, c := range sig {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			t.Fatalf("signature should be lowercase hex, found %q", c)
		}
	}

	// The empty set has a stable, non-empty signature.
	if got := ConfigSignature(nil); len(got) != 64 {
		t.Fatalf("empty-set signature should be 64 hex chars, got %q", got)
	}
}
