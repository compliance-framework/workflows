package pluginprobe

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/open-policy-agent/opa/v1/ast"
	"github.com/open-policy-agent/opa/v1/loader"
)

// CheckPolicies fetches the policy bundle at src (an OCI tag, or a directory holding policies/)
// into outDir if it is OCI, and runs the equivalent of `opa check policies/` with the OPA this
// probe is built with, which go.mod keeps equal to the agent's.
func (p *Prober) CheckPolicies(ctx context.Context, src, outDir string) PolicyResult {
	res := PolicyResult{Source: src}
	art, err := p.fetch(ctx, src, outDir, "policies", "")
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Digest = art.digest
	if info, err := os.Stat(art.path); err != nil || !info.IsDir() {
		res.Error = "the bundle has no policies/ directory"
		return res
	}
	res.Modules, err = opaCheck(art.path)
	if err != nil {
		res.Error = err.Error()
	}
	return res
}

// opaCheck parses and compiles every .rego file under dir as `opa check` does without --strict or
// --bundle (Rego v1, this OPA's capabilities), and returns the number of modules.
func opaCheck(dir string) (int, error) {
	caps := ast.CapabilitiesForThisVersion()
	result, err := loader.NewFileLoader().
		WithRegoVersion(ast.RegoV1).
		WithProcessAnnotation(true).
		WithCapabilities(caps).
		Filtered([]string{dir}, func(_ string, info fs.FileInfo, _ int) bool {
			return !info.IsDir() && filepath.Ext(info.Name()) != ".rego"
		})
	if err != nil {
		return 0, fmt.Errorf("opa check: %w", err)
	}
	modules := result.ParsedModules()
	if len(modules) == 0 {
		return 0, fmt.Errorf("opa check: no .rego files under policies/")
	}
	compiler := ast.NewCompiler().
		WithCapabilities(caps).
		WithEnablePrintStatements(true).
		WithUseTypeCheckAnnotations(true)
	if compiler.Compile(modules); compiler.Failed() {
		return 0, fmt.Errorf("opa check: %s", strings.TrimSpace(compiler.Errors.Error()))
	}
	return len(modules), nil
}
