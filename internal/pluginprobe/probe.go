// Package pluginprobe loads CCF agent plugins the way the agent does and reports the protocol each
// speaks and the agent library it was built with. cmd/plugin-probe drives it.
package pluginprobe

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/hashicorp/go-hclog"
)

// Report is the probe's JSON report.
type Report struct {
	// AgentVersion is the version of github.com/compliance-framework/agent the probe is built
	// with: the runner it loads plugins through.
	AgentVersion string         `json:"agent_version"`
	Plugins      []PluginResult `json:"plugins"`
}

// PluginResult is what the probe found out about one plugin.
type PluginResult struct {
	Source string `json:"source"`
	// Digest is the registry digest an OCI source's tag resolved to.
	Digest string `json:"digest,omitempty"`
	// AnnotatedProtocol is the org.ccf.plugin.protocol.version annotation of an OCI source,
	// which the agent runs the plugin as; 0 when there is none.
	AnnotatedProtocol int `json:"annotated_protocol,omitempty"`
	// Protocol is 1 if Init is unimplemented, 2 if not; 0 when the plugin failed to load.
	Protocol int `json:"protocol,omitempty"`
	// LibVersion is the agent library version in the binary's build info, "" if it has none.
	LibVersion string `json:"lib_version"`
	// InitError is the error a v2 plugin's Init returned for the empty request (informational).
	InitError string   `json:"init_error,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	// Error is why the plugin failed to load; "" when it loaded.
	Error string `json:"error,omitempty"`
}

// Failed reports whether any plugin failed to load.
func (r Report) Failed() bool {
	for _, p := range r.Plugins {
		if p.Error != "" {
			return true
		}
	}
	return false
}

// Prober probes plugins. Set WorkDir; the other fields have defaults.
type Prober struct {
	// WorkDir is where OCI artifacts are extracted, one subdirectory per artifact.
	WorkDir string
	// Platform selects the plugin image of a multi-platform OCI artifact.
	Platform string
	// Timeout bounds starting one plugin and its Init call.
	Timeout time.Duration
	// PluginEnv is the plugins' whole environment. The probe's own (tokens included) is not
	// passed on; go-plugin adds the handshake variables.
	PluginEnv []string
	Logger    hclog.Logger
	// Remote are extra options for registry calls (tests use them for a local registry).
	Remote []remote.Option
}

// DefaultPlatform is the platform the probe runs on, os/arch.
func DefaultPlatform() string { return runtime.GOOS + "/" + runtime.GOARCH }

// Run probes every plugin, in order. Failures are recorded in the report.
func (p *Prober) Run(ctx context.Context, plugins []string) Report {
	report := Report{Plugins: []PluginResult{}}
	for i, src := range plugins {
		report.Plugins = append(report.Plugins, p.ProbePlugin(ctx, src, filepath.Join(p.WorkDir, fmt.Sprintf("%02d", i+1))))
	}
	return report
}

// ProbePlugin fetches the plugin at src (an OCI tag, a binary, or a directory holding a `plugin`
// binary) into outDir if it is OCI, reads its agent library version, then launches it.
func (p *Prober) ProbePlugin(ctx context.Context, src, outDir string) PluginResult {
	res := PluginResult{Source: src}
	art, err := p.fetch(ctx, src, outDir, "plugin", p.Platform)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Digest = art.digest
	res.AnnotatedProtocol, res.Warnings = annotatedProtocol(art.annotations)

	if info, err := os.Stat(art.path); err != nil || !info.Mode().IsRegular() {
		res.Error = fmt.Sprintf("no plugin executable at %s", art.path)
		return res
	}
	lib, err := LibVersion(art.path)
	if err != nil {
		res.Warnings = append(res.Warnings, "no Go build info: "+err.Error())
	} else if lib == "" {
		res.Warnings = append(res.Warnings, "the binary does not depend on "+AgentModule)
	}
	res.LibVersion = lib

	protocol, initErr, err := p.detectProtocol(ctx, art.path)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Protocol, res.InitError = protocol, initErr
	switch {
	case res.AnnotatedProtocol == 2 && protocol == 1:
		res.Error = "annotated as protocol 2 but Init is unimplemented: the agent would fail to start it"
	case art.oci && res.AnnotatedProtocol != 2 && protocol == 2:
		res.Warnings = append(res.Warnings, "speaks protocol 2 but is not annotated as such: the agent runs it as protocol 1, without Init, unless its config sets protocol_version: 2")
	}
	return res
}
