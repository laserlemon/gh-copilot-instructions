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

type repoInfo struct {
	DefaultBranch string `json:"default_branch"`
}

// resolveCommit returns the commit info and the resolved ref NAME for a source.
// A 40-hex ref is an immutable commit SHA. When the ref is empty, the repo's
// default branch is resolved (one extra call) so callers can display its name.
func resolveCommit(client *api.RESTClient, s Source) (ci commitInfo, resolvedRef string, err error) {
	ref := s.Ref
	if ref == "" {
		ref = defaultRef()
	}
	if ref == "" {
		var ri repoInfo
		if err = client.Get(fmt.Sprintf("repos/%s", s.Repo), &ri); err != nil {
			return ci, "", err
		}
		ref = ri.DefaultBranch
		if ref == "" {
			return ci, "", fmt.Errorf("%s: could not determine default branch", s.Repo)
		}
	}
	if err = client.Get(fmt.Sprintf("repos/%s/commits/%s", s.Repo, ref), &ci); err != nil {
		return ci, "", err
	}
	return ci, ref, nil
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
	client, err := newClient(resolveToken(s))
	if err != nil {
		return "", err
	}
	ref := s.Ref
	if ref == "" {
		ref = defaultRef()
	}
	if isFullSHA(ref) {
		return ref, nil
	}
	endpoint := fmt.Sprintf("repos/%s/commits/%s", s.Repo, ref)
	if ref == "" {
		// Unknown ref name: the default-branch tip, learned without a repo lookup.
		var commits []struct {
			SHA string `json:"sha"`
		}
		if err := client.Get(fmt.Sprintf("repos/%s/commits?per_page=1", s.Repo), &commits); err != nil {
			return "", err
		}
		if len(commits) == 0 {
			return "", fmt.Errorf("%s: no commits", s.Repo)
		}
		return commits[0].SHA, nil
	}
	var ci commitInfo
	if err := client.Get(endpoint, &ci); err != nil {
		return "", err
	}
	return ci.SHA, nil
}

// Fetch resolves the source, lists its tree, and downloads the glob-matched
// blobs. Returns the commit SHA, the resolved ref name, and the matched files
// (content verbatim).
func (Fetcher) Fetch(s Source) (string, string, []FetchedFile, error) {
	client, err := newClient(resolveToken(s))
	if err != nil {
		return "", "", nil, err
	}
	ci, resolvedRef, err := resolveCommit(client, s)
	if err != nil {
		return "", "", nil, err
	}
	var tree treeResponse
	if err := client.Get(fmt.Sprintf("repos/%s/git/trees/%s?recursive=1", s.Repo, ci.Commit.Tree.SHA), &tree); err != nil {
		return "", "", nil, err
	}
	if tree.Truncated {
		return "", "", nil, fmt.Errorf("%s: tree too large (truncated); narrow the path", s.Repo)
	}
	var files []FetchedFile
	for _, e := range tree.Tree {
		if e.Type != "blob" || !s.matches(e.Path) {
			continue
		}
		var blob blobResponse
		if err := client.Get(fmt.Sprintf("repos/%s/git/blobs/%s", s.Repo, e.SHA), &blob); err != nil {
			return "", "", nil, fmt.Errorf("%s: %s: %w", s.Repo, e.Path, err)
		}
		content, err := decodeBlob(blob)
		if err != nil {
			return "", "", nil, fmt.Errorf("%s: %s: %w", s.Repo, e.Path, err)
		}
		files = append(files, FetchedFile{Rel: e.Path, Content: content})
	}
	return ci.SHA, resolvedRef, files, nil
}

func decodeBlob(b blobResponse) ([]byte, error) {
	if b.Encoding != "base64" {
		return []byte(b.Content), nil
	}
	clean := strings.ReplaceAll(b.Content, "\n", "")
	return base64.StdEncoding.DecodeString(clean)
}

// isFullSHA reports whether ref is a full 40-character hex commit SHA (immutable).
func isFullSHA(ref string) bool {
	if len(ref) != 40 {
		return false
	}
	for _, c := range ref {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}
