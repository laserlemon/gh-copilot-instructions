package gci

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/auth"
)

// FetchedFile is one matched file's repo-relative path and verbatim content.
type FetchedFile struct {
	Rel     string
	Content []byte
}

// resolveToken returns the token to use for a source, in precedence order:
// inline -> GH_COPILOT_INSTRUCTIONS_TOKEN -> gh auth (go-gh) -> "" (anonymous).
func resolveToken(s Source) string {
	if s.Token != "" {
		return s.Token
	}
	if t := strings.TrimSpace(os.Getenv(EnvToken)); t != "" {
		return t
	}
	if t, _ := auth.TokenForHost("github.com"); t != "" {
		return t
	}
	return ""
}

func newClient(token string) (*api.RESTClient, error) {
	return api.NewRESTClient(api.ClientOptions{AuthToken: token})
}

type commitInfo struct {
	SHA    string `json:"sha"`
	Commit struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	} `json:"commit"`
}

// resolveCommit returns the commit info for a source's ref. A 40-hex ref is an
// immutable commit SHA. When the ref is empty, the repo's default branch tip is
// used (resolved fresh each time, so pulls always follow the current default).
func resolveCommit(client *api.RESTClient, s Source) (commitInfo, error) {
	ref := s.Ref
	if ref == "" {
		ref = defaultRef()
	}
	var ci commitInfo
	if ref == "" {
		var commits []commitInfo
		if err := client.Get(fmt.Sprintf("repos/%s/commits?per_page=1", s.Repo), &commits); err != nil {
			return ci, err
		}
		if len(commits) == 0 {
			return ci, fmt.Errorf("%s: no commits", s.Repo)
		}
		return commits[0], nil
	}
	if err := client.Get(fmt.Sprintf("repos/%s/commits/%s", s.Repo, ref), &ci); err != nil {
		return ci, err
	}
	return ci, nil
}

type treeResponse struct {
	Tree      []treeEntry `json:"tree"`
	Truncated bool        `json:"truncated"`
}

type treeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

type blobResponse struct {
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

// Fetcher fetches matched files for sources via the GitHub API.
type Fetcher struct{}

// ResolveSHA returns just the commit SHA for a source (cheap skip check: a
// single API call, and none at all when the ref is an immutable commit SHA).
func (Fetcher) ResolveSHA(s Source) (string, error) {
	ref := s.Ref
	if ref == "" {
		ref = defaultRef()
	}
	if isFullSHA(ref) {
		return ref, nil
	}
	client, err := newClient(resolveToken(s))
	if err != nil {
		return "", err
	}
	ci, err := resolveCommit(client, s)
	if err != nil {
		return "", err
	}
	return ci.SHA, nil
}

// Fetch resolves the source, lists its tree, and downloads the glob-matched
// blobs. Returns the commit SHA and the matched files (content verbatim). When
// onProgress is non-nil it is called first with the resolved SHA (files=0), then
// with the running count after each blob, so callers can fill in the SHA early
// and animate a live progress counter during the download.
func (Fetcher) Fetch(s Source, onProgress func(sha string, files int)) (string, []FetchedFile, error) {
	client, err := newClient(resolveToken(s))
	if err != nil {
		return "", nil, err
	}
	ci, err := resolveCommit(client, s)
	if err != nil {
		return "", nil, err
	}
	if onProgress != nil {
		onProgress(ci.SHA, 0) // SHA is known now, before any blob downloads
	}
	var tree treeResponse
	if err := client.Get(fmt.Sprintf("repos/%s/git/trees/%s?recursive=1", s.Repo, ci.Commit.Tree.SHA), &tree); err != nil {
		return "", nil, err
	}
	if tree.Truncated {
		return "", nil, fmt.Errorf("%s: tree too large (truncated); narrow the path", s.Repo)
	}
	var files []FetchedFile
	for _, e := range tree.Tree {
		if e.Type != "blob" || !s.matches(e.Path) {
			continue
		}
		var blob blobResponse
		if err := client.Get(fmt.Sprintf("repos/%s/git/blobs/%s", s.Repo, e.SHA), &blob); err != nil {
			return "", nil, fmt.Errorf("%s: %s: %w", s.Repo, e.Path, err)
		}
		content, err := decodeBlob(blob)
		if err != nil {
			return "", nil, fmt.Errorf("%s: %s: %w", s.Repo, e.Path, err)
		}
		files = append(files, FetchedFile{Rel: e.Path, Content: content})
		if onProgress != nil {
			onProgress(ci.SHA, len(files))
		}
	}
	return ci.SHA, files, nil
}

func decodeBlob(b blobResponse) ([]byte, error) {
	if b.Encoding != "base64" {
		return []byte(b.Content), nil
	}
	clean := strings.ReplaceAll(b.Content, "\n", "")
	return base64.StdEncoding.DecodeString(clean)
}

// isHex reports whether s is non-empty and made up entirely of hex digits.
func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// isFullSHA reports whether ref is a full 40-character hex commit SHA (immutable).
func isFullSHA(ref string) bool {
	return len(ref) == 40 && isHex(ref)
}

// refPinsTo reports whether a configured ref provably identifies the commit
// recorded as sha - i.e. ref is at least 7 hex digits and a left-pinned prefix
// of sha (case-insensitive). Such a ref is immutable, so a fresh pull can be
// skipped. Examples (sha=e28eb6df72fb90a84015cb6fda9104bff345ae48):
//
//	ref=e28eb6d  -> true   (≥7 hex, prefix of sha)
//	ref=e28eb6   -> false  (only 6 hex digits)
//	ref=345ae48  -> false  (hex, but a suffix, not a prefix)
//	ref=main     -> false  (not hex)
func refPinsTo(ref, sha string) bool {
	if len(ref) < 7 || !isHex(ref) || sha == "" {
		return false
	}
	return strings.HasPrefix(strings.ToLower(sha), strings.ToLower(ref))
}
