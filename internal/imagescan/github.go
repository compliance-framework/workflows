package imagescan

import (
	"context"
	"errors"
	"net/http"

	"github.com/compliance-framework/workflows/internal/bump"
)

// GitHubFiles reads repos' files on their default branch through the REST API.
type GitHubFiles struct {
	GH       *bump.GitHub
	branches map[string]string
}

func (g *GitHubFiles) branch(ctx context.Context, repo string) (string, error) {
	if b, ok := g.branches[repo]; ok {
		return b, nil
	}
	b, err := g.GH.DefaultBranch(ctx, repo)
	if err != nil {
		return "", err
	}
	if g.branches == nil {
		g.branches = map[string]string{}
	}
	g.branches[repo] = b
	return b, nil
}

// Dir lists dir on repo's default branch.
func (g *GitHubFiles) Dir(ctx context.Context, repo, dir string) ([]string, error) {
	b, err := g.branch(ctx, repo)
	if err != nil {
		return nil, err
	}
	return g.GH.Dir(ctx, repo, b, dir)
}

// File reads path on repo's default branch; ok is false if it doesn't exist.
func (g *GitHubFiles) File(ctx context.Context, repo, path string) ([]byte, bool, error) {
	b, err := g.branch(ctx, repo)
	if err != nil {
		return nil, false, err
	}
	data, err := g.GH.File(ctx, repo, b, path)
	var status *bump.StatusError
	if errors.As(err, &status) && status.Code == http.StatusNotFound {
		return nil, false, nil
	}
	return data, err == nil, err
}
