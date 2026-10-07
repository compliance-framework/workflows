package pluginprobe

import (
	"context"
	"debug/buildinfo"
	"fmt"
	"os/exec"
	"runtime/debug"
	"strings"
	"time"

	"github.com/compliance-framework/agent/runner"
	"github.com/compliance-framework/agent/runner/proto"
	"github.com/hashicorp/go-hclog"
	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AgentModule is the module path of the agent library plugins are built against.
const AgentModule = "github.com/compliance-framework/agent"

// OPAModule is the module path of OPA, which the agent evaluates policies with.
const OPAModule = "github.com/open-policy-agent/opa"

// DefaultTimeout bounds starting a plugin and its Init call when Prober.Timeout is unset.
const DefaultTimeout = time.Minute

// LibVersion reads the agent library version a Go binary was built with from its build info. It
// returns "" if the binary doesn't depend on the agent, and "<version> => <replacement>" if the
// dependency was replaced (a local build against an unreleased agent).
func LibVersion(path string) (string, error) {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return "", err
	}
	return DepVersion(info, AgentModule), nil
}

// DepVersion returns the version of the dependency path in info: "" if there is none, and
// "<version> => <replacement> [<version>]" if it was replaced.
func DepVersion(info *debug.BuildInfo, path string) string {
	for _, dep := range info.Deps {
		if dep.Path != path {
			continue
		}
		if r := dep.Replace; r != nil {
			return strings.TrimSpace(fmt.Sprintf("%s => %s %s", dep.Version, r.Path, r.Version))
		}
		return dep.Version
	}
	return ""
}

// detectProtocol launches the plugin with the agent's handshake, dispenses "runner" and calls Init
// with an empty request. codes.Unimplemented means protocol 1; any other answer means protocol 2,
// and an error Init returned is passed back as initErr. err is a load failure: the plugin didn't
// start, couldn't be dispensed, or died or hung in Init.
func (p *Prober) detectProtocol(ctx context.Context, path string) (protocol int, initErr string, err error) {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	logger := p.Logger
	if logger == nil {
		logger = hclog.NewNullLogger()
	}
	cmd := exec.Command(path)
	cmd.Env = p.PluginEnv
	client := goplugin.NewClient(&goplugin.ClientConfig{
		HandshakeConfig:  runner.HandshakeConfig,
		Plugins:          runner.PluginMap,
		Cmd:              cmd,
		SkipHostEnv:      true,
		StartTimeout:     timeout,
		Logger:           logger,
		AllowedProtocols: []goplugin.Protocol{goplugin.ProtocolGRPC},
	})
	defer client.Kill()

	rpc, err := client.Client()
	if err != nil {
		return 0, "", fmt.Errorf("starting the plugin: %w", err)
	}
	raw, err := rpc.Dispense("runner")
	if err != nil {
		return 0, "", fmt.Errorf("dispensing runner: %w", err)
	}
	r, ok := raw.(runner.RunnerV2)
	if !ok {
		return 0, "", fmt.Errorf("dispensed %T, which is not a runner.RunnerV2", raw)
	}

	done := make(chan error, 1)
	go func() {
		_, err := r.Init(&proto.InitRequest{}, noopAPIHelper{})
		done <- err
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err = <-done:
	case <-timer.C:
		return 0, "", fmt.Errorf("init did not return within %s", timeout)
	case <-ctx.Done():
		return 0, "", ctx.Err()
	}

	switch code := status.Code(err); {
	case err == nil:
		return 2, "", nil
	case code == codes.Unimplemented:
		return 1, "", nil
	case code == codes.Unavailable || client.Exited():
		return 0, "", fmt.Errorf("the plugin exited during Init: %w", err)
	default:
		return 2, err.Error(), nil
	}
}

// noopAPIHelper accepts and drops what a plugin sends during Init (subject and risk templates).
type noopAPIHelper struct{}

func (noopAPIHelper) CreateEvidence(context.Context, []*proto.Evidence) error { return nil }
func (noopAPIHelper) UpsertRiskTemplates(context.Context, string, []*proto.RiskTemplate) error {
	return nil
}
func (noopAPIHelper) UpsertSubjectTemplates(context.Context, []*proto.SubjectTemplate) error {
	return nil
}
