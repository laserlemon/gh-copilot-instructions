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
// single API call, and none at all when the ref is an immutable commit SHA). A
// gist source resolves via the Gists API instead (see gistResolveSHA).
func (Fetcher) ResolveSHA(s Source) (string, error) {
	if s.IsGist() {
		return gistResolveSHA(s)
	}
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

// Fetch resolves the source and downloads its glob-matched files (content
// verbatim), returning the version SHA, the owner login (gists only - "" for a
// repository or an anonymous gist), and the files. A gist is fetched via the
// Gists API (gistFetch); a repository via repoFetch. When onProgress is non-nil it
// is called first with the resolved SHA (files=0), then with the running count
// after each file, so callers can fill in the SHA early and animate progress.
func (Fetcher) Fetch(s Source, onProgress func(sha string, files int)) (string, string, []FetchedFile, error) {
	if s.IsGist() {
		return gistFetch(s, onProgress)
	}
	sha, files, err := repoFetch(s, onProgress)
	return sha, "", files, err
}

// repoFetch resolves a repository source, lists its tree, and downloads the
// glob-matched blobs, returning the commit SHA and the matched files.
func repoFetch(s Source, onProgress func(sha string, files int)) (string, []FetchedFile, error) {
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
	// Scope the (recursive) tree listing to the directory the glob is rooted in,
	// so a narrow path into a huge monorepo doesn't fetch - and truncate on - the
	// whole repository. base is the repo-relative prefix of the scoped subtree
	// ("" when listing from the root); tree entries are relative to it.
	treeSHA := ci.Commit.Tree.SHA
	base := ""
	if prefix := literalTreePrefix(s.matchPatterns()); prefix != "" {
		sub, err := descendTree(client, s.Repo, treeSHA, prefix)
		if err != nil {
			return "", nil, err
		}
		if sub == "" {
			return ci.SHA, nil, nil // the prefix directory doesn't exist: no matches
		}
		treeSHA, base = sub, prefix+"/"
	}
	var tree treeResponse
	if err := client.Get(fmt.Sprintf("repos/%s/git/trees/%s?recursive=1", s.Repo, treeSHA), &tree); err != nil {
		return "", nil, err
	}
	if tree.Truncated {
		return "", nil, fmt.Errorf("%s: tree too large (truncated), narrow the path", s.Repo)
	}
	var files []FetchedFile
	for _, e := range tree.Tree {
		rel := base + e.Path
		if e.Type != "blob" || !s.matches(rel) {
			continue
		}
		var blob blobResponse
		if err := client.Get(fmt.Sprintf("repos/%s/git/blobs/%s", s.Repo, e.SHA), &blob); err != nil {
			return "", nil, fmt.Errorf("%s: %s: %w", s.Repo, rel, err)
		}
		content, err := decodeBlob(blob)
		if err != nil {
			return "", nil, fmt.Errorf("%s: %s: %w", s.Repo, rel, err)
		}
		files = append(files, FetchedFile{Rel: rel, Content: content})
		if onProgress != nil {
			onProgress(ci.SHA, len(files))
		}
	}
	return ci.SHA, files, nil
}

// descendTree walks dir segment by segment from a root tree SHA, using a
// non-recursive tree listing at each level (each is one directory's direct
// children, so it never truncates), and returns the tree SHA of the directory
// dir names. It returns "" (no error) when a segment is missing or isn't a
// directory, so the caller can treat the scoped path as matching no files.
func descendTree(client *api.RESTClient, repo, rootSHA, dir string) (string, error) {
	sha := rootSHA
	for _, seg := range strings.Split(dir, "/") {
		var tree treeResponse
		if err := client.Get(fmt.Sprintf("repos/%s/git/trees/%s", repo, sha), &tree); err != nil {
			return "", err
		}
		next := ""
		for _, e := range tree.Tree {
			if e.Path == seg && e.Type == "tree" {
				next = e.SHA
				break
			}
		}
		if next == "" {
			return "", nil
		}
		sha = next
	}
	return sha, nil
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
