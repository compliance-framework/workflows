package bump

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
)

// Identity is a commit author or committer.
type Identity struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// BranchHead returns the commit branch points at, or "" when repo has no such branch.
func (g *GitHub) BranchHead(ctx context.Context, repo, branch string) (string, error) {
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/git/ref/heads/%s", g.Owner, repo, branch), nil, &ref)
	if se := (*StatusError)(nil); errors.As(err, &se) && se.Code == http.StatusNotFound {
		return "", nil
	}
	return ref.Object.SHA, err
}

// CommitTree returns the tree and the parents of commit sha.
func (g *GitHub) CommitTree(ctx context.Context, repo, sha string) (tree string, parents []string, err error) {
	var c struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
	}
	if err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/git/commits/%s", g.Owner, repo, sha), nil, &c); err != nil {
		return "", nil, err
	}
	for _, p := range c.Parents {
		parents = append(parents, p.SHA)
	}
	return c.Tree.SHA, parents, nil
}

// CreateTree creates the tree base with files (path -> content) replaced, and returns its SHA.
// The same content always gives the same SHA.
func (g *GitHub) CreateTree(ctx context.Context, repo, base string, files map[string][]byte) (string, error) {
	type entry struct {
		Path    string `json:"path"`
		Mode    string `json:"mode"`
		Type    string `json:"type"`
		Content string `json:"content"`
	}
	in := struct {
		BaseTree string  `json:"base_tree"`
		Tree     []entry `json:"tree"`
	}{BaseTree: base}
	for _, p := range slices.Sorted(maps.Keys(files)) {
		in.Tree = append(in.Tree, entry{Path: p, Mode: "100644", Type: "blob", Content: string(files[p])})
	}
	var out struct {
		SHA string `json:"sha"`
	}
	err := g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/git/trees", g.Owner, repo), in, &out)
	return out.SHA, err
}

// CreateCommit creates a commit of tree on parent and returns its SHA. A zero author leaves the
// author and committer to GitHub: the token's identity.
func (g *GitHub) CreateCommit(ctx context.Context, repo, message, tree, parent string, author Identity) (string, error) {
	in := map[string]any{"message": message, "tree": tree, "parents": []string{parent}}
	if author != (Identity{}) {
		in["author"], in["committer"] = author, author
	}
	var out struct {
		SHA string `json:"sha"`
	}
	err := g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/git/commits", g.Owner, repo), in, &out)
	return out.SHA, err
}

// SetBranch points branch at sha, forcing it, and creates the branch when create is set.
func (g *GitHub) SetBranch(ctx context.Context, repo, branch, sha string, create bool) error {
	if create {
		in := map[string]string{"ref": "refs/heads/" + branch, "sha": sha}
		return g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/git/refs", g.Owner, repo), in, nil)
	}
	in := map[string]any{"sha": sha, "force": true}
	return g.do(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/%s/git/refs/heads/%s", g.Owner, repo, branch), in, nil)
}

// BranchAuthors returns the author logins of the commits on head that aren't on base (GitHub's
// compare), "" for a commit GitHub doesn't link to an account. It refuses more than 100 commits.
func (g *GitHub) BranchAuthors(ctx context.Context, repo, base, head string) ([]string, error) {
	var cmp struct {
		TotalCommits int `json:"total_commits"`
		Commits      []struct {
			Author *struct {
				Login string `json:"login"`
			} `json:"author"`
		} `json:"commits"`
	}
	if err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/compare/%s...%s?per_page=100", g.Owner, repo, url.PathEscape(base), url.PathEscape(head)), nil, &cmp); err != nil {
		return nil, err
	}
	if cmp.TotalCommits > len(cmp.Commits) {
		return nil, fmt.Errorf("%s has %d commits that aren't on %s, more than one page", head, cmp.TotalCommits, base)
	}
	out := make([]string, len(cmp.Commits))
	for i, c := range cmp.Commits {
		if c.Author != nil {
			out[i] = c.Author.Login
		}
	}
	return out, nil
}
