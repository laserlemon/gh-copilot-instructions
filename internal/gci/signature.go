package gci

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// ConfigSignature is a stable fingerprint of a source set's *fetch identity* -
// the inputs that determine what a fresh `pull` would install. Two machines with
// the same signature would produce identical configs if both pulled at the same
// moment, which is exactly the property the Codespaces secret needs to track.
//
// It is deliberately NOT sha256(list --raw): that would be coupled to the config
// line's exact formatting and would treat source ordering (and the default-path
// spelling) as meaningful. Instead we hash a normalized, sorted set of per-source
// identity tuples:
//
//   - include: repo, ref as-is (an empty "follow the default branch" ref is its
//     own canonical value - we do not resolve it, which would need the network
//     and reintroduce transience), the effective path (so "o/r" and
//     "o/r:**/*.instructions.md" - which fetch identically - sign identically),
//     and the token (it lives in the secret, so a token change must re-push).
//   - exclude: everything transient in SourceState (resolved SHA, pulledAt,
//     installed filenames). Those move on every `pull` but do not mean the
//     secret is stale, because a new Codespace re-pulls to the latest SHA anyway.
//   - canonicalize: sort the tuples so source ordering never counts as drift.
//
// The result is a lowercase hex sha256. An empty source set hashes to the digest
// of the empty string (a stable, non-empty sentinel), so "no sources" is itself a
// comparable signature.
func ConfigSignature(srcs []Source) string {
	tuples := make([]string, 0, len(srcs))
	for _, s := range srcs {
		// NUL-separate the fields so no combination of values can collide across
		// field boundaries (repo/ref/path/token can't contain NUL).
		tuples = append(tuples, strings.Join([]string{
			s.Repo,
			s.Ref,
			s.effectivePath(),
			s.Token,
		}, "\x00"))
	}
	sort.Strings(tuples)
	// Newline-join the (NUL-delimited) tuples; neither byte appears in a field.
	sum := sha256.Sum256([]byte(strings.Join(tuples, "\n")))
	return hex.EncodeToString(sum[:])
}
