package renovate

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// dockerfileFiles are the managerFilePatterns of Renovate's dockerfile manager: it reads every FROM
// of a file they match, test sources and testdata included, and pins and bumps its images.
var dockerfileFiles = []*regexp.Regexp{
	regexp.MustCompile(`(^|/|\.)([Dd]ocker|[Cc]ontainer)file$`),
	regexp.MustCompile(`(^|/)([Dd]ocker|[Cc]ontainer)file[^/]*$`),
}

// TestFixturesHiddenFromDockerfileManager keeps the shared preset's digest pinning (and any image
// bump) from breaking this repo's tests: Renovate's dockerfile manager must read no _test.go file,
// whose FROM lines sit in Go string literals, and no testdata file, whose images a test expects as
// they are (its golden, a change it plans). A fixture Dockerfile takes a name no managerFilePatterns
// match (internal/imagescan/testdata/patch/*.Dockerfile.in) or is written by the test.
func TestFixturesHiddenFromDockerfileManager(t *testing.T) {
	root := filepath.Join("..", "..")
	read := func(rel string) bool {
		for _, re := range dockerfileFiles {
			if re.MatchString(rel) {
				return true
			}
		}
		return false
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() || !read(rel) {
			return nil
		}
		if strings.HasSuffix(rel, "_test.go") || strings.Contains("/"+rel, "/testdata/") {
			t.Errorf("%s: Renovate's dockerfile manager reads it and would pin and move its images; rename it so no managerFilePatterns match", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
