package gci

import (
	"fmt"
	"io"
	"net/http"
	"sort"
)

// A gist is fetched via the Gists API (GET /gists/{id}), which returns a flat
// files map and a history whose first entry is the latest version SHA - no
// tree/blob walk. This file implements the gist arm of the Fetcher: ResolveSHA
// and Fetch dispatch here when the source is a gist (Source.IsGist).

// gistFile is one file in a gist's files map.
type gistFile struct {
	Filename  string `json:"filename"`
	Content   string `json:"content"`
	RawURL    string `json:"raw_url"`
	Truncated bool   `json:"truncated"`
}

// gistResponse is the subset of GET /gists/{id} we use: the files map and the
// version history (history[0] is the most recent revision).
type gistResponse struct {
	Files   map[string]gistFile `json:"files"`
	History []struct {
		Version string `json:"version"`
	} `json:"history"`
}

// gistPath returns the Gists API path for a source: a pinned version when the
// source has a ref, else the gist's current state.
func gistPath(s Source) string {
	if s.Ref != "" {
		return fmt.Sprintf("gists/%s/%s", s.GistID(), s.Ref)
	}
	return fmt.Sprintf("gists/%s", s.GistID())
}

// gistVersion picks the version SHA to record for a gist: its latest
// (history[0].version) when the response carries a history, else the pinned ref.
func gistVersion(g gistResponse, ref string) string {
	if len(g.History) > 0 && g.History[0].Version != "" {
		return g.History[0].Version
	}
	return ref
}

// gistResolveSHA returns just the version SHA a gist source resolves to (the
// cheap skip check). A ref that is already a full 40-hex version is immutable
// and returned without a call; otherwise the gist's latest version is fetched.
func gistResolveSHA(s Source) (string, error) {
	if isFullSHA(s.Ref) {
		return s.Ref, nil
	}
	client, err := newClient(resolveToken(s))
	if err != nil {
		return "", err
	}
	var g gistResponse
	if err := client.Get(gistPath(s), &g); err != nil {
		return "", err
	}
	return gistVersion(g, s.Ref), nil
}

// gistFetch resolves the gist, then returns its version SHA and the glob-matched
// files (content verbatim). Files are matched by name against the source's path
// glob - gists are flat, so ":path" is a filename glob. onProgress mirrors the
// repo fetcher: it is called first with the resolved version (files=0), then
// with the running count after each file.
func gistFetch(s Source, onProgress func(sha string, files int)) (string, []FetchedFile, error) {
	token := resolveToken(s)
	client, err := newClient(token)
	if err != nil {
		return "", nil, err
	}
	var g gistResponse
	if err := client.Get(gistPath(s), &g); err != nil {
		return "", nil, err
	}
	version := gistVersion(g, s.Ref)
	if onProgress != nil {
		onProgress(version, 0) // version known now, before any raw downloads
	}
	// A gist's files are a map, so sort by name before matching to keep the file
	// list (and progress) deterministic across runs.
	names := make([]string, 0, len(g.Files))
	for name := range g.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	var files []FetchedFile
	for _, name := range names {
		gf := g.Files[name]
		rel := gf.Filename
		if rel == "" {
			rel = name
		}
		if !s.matches(rel) {
			continue
		}
		content := []byte(gf.Content)
		if gf.Truncated {
			// Large gist files come back truncated (content elided in the API
			// response); fetch the verbatim bytes from the file's raw URL.
			b, err := fetchRaw(gf.RawURL, token)
			if err != nil {
				return "", nil, fmt.Errorf("gist %s: %s: %w", s.GistID(), rel, err)
			}
			content = b
		}
		files = append(files, FetchedFile{Rel: rel, Content: content})
		if onProgress != nil {
			onProgress(version, len(files))
		}
	}
	return version, files, nil
}

// fetchRaw GETs a gist file's raw_url. Gist raw content is served from a
// separate host (gist.githubusercontent.com), so this uses a plain
// authenticated request rather than the api.github.com REST client.
func fetchRaw(rawURL, token string) ([]byte, error) {
	if rawURL == "" {
		return nil, fmt.Errorf("no raw url for truncated file")
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("raw fetch returned HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
