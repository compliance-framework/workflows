// Command plugin-probe loads CCF agent plugins through the agent's runner library and reports the
// protocol each speaks (v1 or v2) and the agent library it was built with, and checks policy
// bundles with the agent's OPA version:
//
//	plugin-probe [--plugins a,b] [--policies c,d] [--json FILE] [--markdown FILE] [flags]
//
// A plugin is an OCI tag (registry/repository:tag), a binary, or a directory holding a `plugin`
// binary; a policy bundle is an OCI tag or a directory holding policies/. The markdown report goes
// to stdout unless --markdown names a file; --json writes the JSON report ("-" for stdout). The
// exit status is 1 if any plugin fails to load or any bundle fails its check. docs/plugin-probe.md
// has the details.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"

	"github.com/compliance-framework/workflows/internal/pluginprobe"
	"github.com/hashicorp/go-hclog"
)

// errFailed is returned when the probe ran but something failed to load or check.
var errFailed = errors.New("a plugin failed to load or a policy bundle failed its check")

func main() {
	err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "::error::plugin-probe: "+err.Error())
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("plugin-probe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	plugins := fs.String("plugins", "", "comma- or whitespace-separated plugins: OCI tags, binaries or directories holding `plugin`")
	policies := fs.String("policies", "", "comma- or whitespace-separated policy bundles: OCI tags or directories holding policies/")
	jsonOut := fs.String("json", "", `write the JSON report to this file ("-": stdout)`)
	mdOut := fs.String("markdown", "-", `write the markdown report to this file ("-": stdout, "": none)`)
	workDir := fs.String("work-dir", "", "extract OCI artifacts here and keep them (default: a temporary directory, removed)")
	platform := fs.String("platform", pluginprobe.DefaultPlatform(), "os/arch of the plugin images to pull")
	timeout := fs.Duration("timeout", pluginprobe.DefaultTimeout, "bound on starting each plugin and its Init call")
	logLevel := fs.String("log-level", "warn", "go-plugin log level (debug shows the plugins' own logs)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	level := hclog.LevelFromString(*logLevel)
	if level == hclog.NoLevel {
		return fmt.Errorf("--log-level %q: want trace, debug, info, warn or error", *logLevel)
	}
	pluginList, policyList := splitList(*plugins), splitList(*policies)
	if len(pluginList)+len(policyList) == 0 {
		return errors.New("nothing to probe: pass --plugins and/or --policies")
	}
	if *jsonOut == "-" && *mdOut == "-" {
		return errors.New("--json - and --markdown - both write to stdout; send one of them to a file")
	}

	dir := *workDir
	if dir == "" {
		tmp, err := os.MkdirTemp("", "plugin-probe-")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(tmp) }()
		dir = tmp
	}
	prober := &pluginprobe.Prober{
		WorkDir:   dir,
		Platform:  *platform,
		Timeout:   *timeout,
		PluginEnv: pluginEnv(os.Getenv),
		Logger:    hclog.New(&hclog.LoggerOptions{Name: "plugin-probe", Output: stderr, Level: level}),
	}
	report := prober.Run(ctx, pluginList, policyList)
	report.AgentVersion, report.OPAVersion = depVersions()

	if err := writeOutput(*jsonOut, stdout, func(w io.Writer) error {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}); err != nil {
		return err
	}
	if err := writeOutput(*mdOut, stdout, report.WriteMarkdown); err != nil {
		return err
	}
	if report.Failed() {
		return errFailed
	}
	return nil
}

// splitList splits a comma- or whitespace-separated list, dropping empty items.
func splitList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' })
}

// pluginEnv is the environment plugins run with: enough to run, none of the probe's secrets.
func pluginEnv(getenv func(string) string) []string {
	var env []string
	for _, k := range []string{"PATH", "HOME", "TMPDIR"} {
		if v := getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func writeOutput(path string, stdout io.Writer, write func(io.Writer) error) error {
	switch path {
	case "":
		return nil
	case "-":
		return write(stdout)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := write(f); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// depVersions returns the agent library and OPA versions this binary is built with.
func depVersions() (agent, opa string) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", ""
	}
	return versionsFromInfo(info)
}

func versionsFromInfo(info *debug.BuildInfo) (agent, opa string) {
	return pluginprobe.DepVersion(info, pluginprobe.AgentModule), pluginprobe.DepVersion(info, pluginprobe.OPAModule)
}
