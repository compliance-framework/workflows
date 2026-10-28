package imagescan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/go-containerregistry/pkg/crane"
)

// ScannerImage is the osv-scanner the tool runs, from its official image, pinned by digest. This
// repo's renovate.json tracks the line with a custom manager (TestScannerImageRenovate), so
// Renovate updates it like any other dependency. It is the same tool as ci-common.yml's vulns job.
const ScannerImage = "ghcr.io/google/osv-scanner:v2.7.0@sha256:4f3b7e2c64b9d38a5febc5a7e4a3dfef1d1362f726dea7a2ef4762b46469fcd0"

// Platform is the platform the tool pulls and scans: the one the CI runners build and run.
const Platform = "linux/amd64"

// Runner runs a command and returns its standard output. A command that exits non-zero returns
// an *ExitError.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExitError is a command's non-zero exit.
type ExitError struct {
	Code   int
	Stderr string
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("exit status %d: %s", e.Code, strings.TrimSpace(e.Stderr))
}

// ExecRunner runs commands with os/exec.
type ExecRunner struct{}

// Run runs name with args and returns its standard output.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		tail := stderr.String()
		if len(tail) > 2000 {
			tail = tail[len(tail)-2000:]
		}
		return stdout.Bytes(), &ExitError{Code: exit.ExitCode(), Stderr: tail}
	}
	return stdout.Bytes(), err
}

// Scan is the result of scanning one image.
type Scan struct {
	Ref      string
	OS       string // as osv-scanner names it, e.g. "Alpine Linux v3.24"; "" if unknown
	Findings []Finding
}

// Scanner scans images with osv-scanner, run from its image with Docker, plus the runtime check.
type Scanner struct {
	Run     Runner
	Image   string // the osv-scanner image, ScannerImage
	OSV     *OSV   // the runtime check's advisories
	TempDir string // where image archives are written
	// CacheDir holds osv-scanner's offline vulnerability databases. When set, the scan uses them
	// (--offline-vulnerabilities) and refreshes them first (--download-offline-databases, which
	// downloads only the ecosystems the image has, and only what changed); "" queries the API.
	CacheDir string
	// Pull pulls each image for Platform before the scan, and removes it after unless it was
	// already there. Off for an image built locally (image-scan check).
	Pull bool
}

// Scan scans ref: its packages with osv-scanner, then the runtimes it carries against OSV.
func (s *Scanner) Scan(ctx context.Context, ref string) (*Scan, error) {
	// Every reference goes on docker's command line: only well-formed ones.
	if _, err := ParseRef(ref); err != nil {
		return nil, err
	}
	if s.Pull {
		// An image that was already here (a local run) stays; one the scan pulled is removed, so a
		// run over many images doesn't fill the runner's disk.
		_, err := s.Run.Run(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", ref)
		had := err == nil
		if _, err := s.Run.Run(ctx, "docker", "pull", "--quiet", "--platform", Platform, ref); err != nil {
			return nil, fmt.Errorf("pull %s: %w", ref, err)
		}
		if !had {
			defer func() { _, _ = s.Run.Run(context.WithoutCancel(ctx), "docker", "image", "rm", ref) }()
		}
	}
	dir, err := os.MkdirTemp(s.TempDir, "image-scan-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	// The scanner runs as this user, so the archive and the database cache stay readable here.
	if err := os.Chmod(dir, 0o755); err != nil {
		return nil, err
	}
	archive := filepath.Join(dir, "image.tar")
	if _, err := s.Run.Run(ctx, "docker", "save", "--output", archive, ref); err != nil {
		return nil, fmt.Errorf("save %s: %w", ref, err)
	}
	if err := os.Chmod(archive, 0o644); err != nil {
		return nil, err
	}
	args := []string{"run", "--rm", "--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()), "--volume", dir + ":/work:ro"}
	if s.CacheDir != "" {
		args = append(args, "--volume", s.CacheDir+":/cache", "--env", "OSV_SCANNER_LOCAL_DB_CACHE_DIRECTORY=/cache")
	}
	args = append(args, s.Image, "scan", "image", "--archive", "--format", "json")
	if s.CacheDir != "" {
		args = append(args, "--offline-vulnerabilities", "--download-offline-databases")
	}
	args = append(args, "/work/image.tar")
	out, err := s.Run.Run(ctx, "docker", args...)
	// osv-scanner exits 1 when it finds vulnerabilities and 128 when it finds no packages.
	var exit *ExitError
	switch {
	case err == nil, errors.As(err, &exit) && exit.Code == 1:
	case errors.As(err, &exit) && exit.Code == 128:
		out = []byte("{}")
	default:
		return nil, fmt.Errorf("osv-scanner %s: %w", ref, err)
	}
	findings, osName, err := ParseOSVScanner(out, ref)
	if err != nil {
		return nil, err
	}
	rt, err := s.runtimes(ctx, ref)
	if err != nil {
		return nil, err
	}
	return &Scan{Ref: ref, OS: osName, Findings: append(findings, rt...)}, nil
}

// runtimes finds the runtimes of Runtimes in ref, by their version variable or else by running
// their binary (with no network), and returns their OSV advisories as findings.
func (s *Scanner) runtimes(ctx context.Context, ref string) ([]Finding, error) {
	out, err := s.Run.Run(ctx, "docker", "image", "inspect", "--format", "{{json .Config.Env}}", ref)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", ref, err)
	}
	var env []string
	if err := json.Unmarshal(bytes.TrimSpace(out), &env); err != nil {
		return nil, fmt.Errorf("inspect %s: %w", ref, err)
	}
	var findings []Finding
	for _, rt := range Runtimes {
		version := envValue(env, rt.Env)
		if version == "" && rt.Binary != "" {
			// No binary (or no way to run it) is not an error: the image just hasn't this runtime.
			if v, err := s.Run.Run(ctx, "docker", "run", "--rm", "--network", "none", "--entrypoint", rt.Binary, ref, "--version"); err == nil {
				version = strings.TrimSpace(string(v))
			}
		}
		version = strings.TrimPrefix(version, "v")
		if version == "" || len(numbers(version)) == 0 || numbers(version)[0] == 0 {
			continue
		}
		vulns, err := s.OSV.Query(ctx, rt.Ecosystem, rt.Package, version)
		if err != nil {
			return nil, err
		}
		findings = append(findings, runtimeFindings(rt, ref, version, vulns)...)
	}
	return findings, nil
}

// Registry reads image tags and digests.
type Registry interface {
	// Digest returns the digest ref (name:tag) points at: a multi-platform index's own digest,
	// the one Renovate pins.
	Digest(ctx context.Context, ref string) (string, error)
	// Tags lists the tags of the repository name.
	Tags(ctx context.Context, name string) ([]string, error)
}

// Crane is the Registry, anonymous, with go-containerregistry.
type Crane struct{}

// Digest returns ref's digest.
func (Crane) Digest(ctx context.Context, ref string) (string, error) {
	return crane.Digest(ref, crane.WithContext(ctx))
}

// Tags lists name's tags.
func (Crane) Tags(ctx context.Context, name string) ([]string, error) {
	return crane.ListTags(name, crane.WithContext(ctx))
}
