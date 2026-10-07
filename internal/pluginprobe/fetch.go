package pluginprobe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/compliance-framework/gooci/pkg/oci"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// ProtocolAnnotation is the OCI annotation the agent reads a plugin's protocol version from.
const ProtocolAnnotation = "org.ccf.plugin.protocol.version"

// artifact is a fetched plugin.
type artifact struct {
	path        string // the plugin executable
	oci         bool
	digest      string
	annotations map[string]string
}

// fetch resolves src the way the agent does: an existing file is used as is, an existing directory
// must hold entry (`plugin`), and anything else must be an OCI tag, which gooci downloads and
// extracts into outDir. platform ("os/arch") selects the image of a multi-platform index. Local
// paths are made absolute, so a bare name like "plugin" is never looked up in PATH.
func (p *Prober) fetch(ctx context.Context, src, outDir, entry, platform string) (artifact, error) {
	if info, err := os.Stat(src); err == nil {
		abs, err := filepath.Abs(src)
		if err != nil {
			return artifact{}, err
		}
		if info.IsDir() {
			return artifact{path: filepath.Join(abs, entry)}, nil
		}
		return artifact{path: abs}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return artifact{}, err
	}

	tag, err := name.NewTag(src, name.StrictValidation)
	if err != nil {
		return artifact{}, fmt.Errorf("%s is neither a local path nor an OCI tag (registry/repository:tag): %w", src, err)
	}
	opts := append([]remote.Option{remote.WithContext(ctx), remote.WithAuthFromKeychain(oci.ECRKeychain())}, p.Remote...)

	// The digest and annotations come from the tag's top-level manifest (for a plugin, the index),
	// which is where the agent reads the protocol annotation.
	desc, err := remote.Get(tag, opts...)
	if err != nil {
		return artifact{}, fmt.Errorf("resolving %s: %w", src, err)
	}
	art := artifact{oci: true, digest: desc.Digest.String(), annotations: manifestAnnotations(desc.Manifest)}

	if platform != "" {
		plat, err := v1.ParsePlatform(platform)
		if err != nil {
			return artifact{}, fmt.Errorf("platform %q: %w", platform, err)
		}
		opts = append(opts, remote.WithPlatform(*plat))
	}
	dl, err := oci.NewDownloader(tag, outDir)
	if err != nil {
		return artifact{}, err
	}
	if err := dl.Download(opts...); err != nil {
		return artifact{}, fmt.Errorf("downloading %s: %w", src, err)
	}
	art.path = filepath.Join(outDir, entry)
	return art, nil
}

func manifestAnnotations(manifest []byte) map[string]string {
	var m struct {
		Annotations map[string]string `json:"annotations"`
	}
	if json.Unmarshal(manifest, &m) != nil {
		return nil
	}
	return m.Annotations
}

// annotatedProtocol parses the protocol annotation as the agent does: 0 when it is absent, and a
// warning when it is present but not 1 or 2 (the agent then ignores it).
func annotatedProtocol(annotations map[string]string) (int, []string) {
	value, ok := annotations[ProtocolAnnotation]
	if !ok {
		return 0, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || (n != 1 && n != 2) {
		return 0, []string{fmt.Sprintf("ignoring %s=%q: not 1 or 2", ProtocolAnnotation, value)}
	}
	return n, nil
}
