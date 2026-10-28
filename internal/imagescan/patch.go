package imagescan

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// The managed block's markers. image-scan owns the lines between them, markers included.
const (
	BlockBegin = "# image-scan:os-patches begin"
	BlockEnd   = "# image-scan:os-patches end"
	blockNote  = "# Managed by compliance-framework/workflows image-scan (docs/image-scan.md#os-package-patches); removed once the base image ships the fixes."
)

// Package managers an image can patch its OS packages with (Scan.PackageManager).
const (
	APK = "apk"     // Alpine
	APT = "apt-get" // Debian, Ubuntu
)

// osPackage is the name grammar of Alpine and Debian packages. A name goes into a RUN line, so
// anything else is refused.
var osPackage = regexp.MustCompile(`^[a-z0-9][a-z0-9+._-]*$`)

// block returns the managed block that upgrades pkgs with pm.
func block(pm string, pkgs []string) ([]string, error) {
	for _, p := range pkgs {
		if !osPackage.MatchString(p) {
			return nil, fmt.Errorf("package %q: not an OS package name", p)
		}
	}
	var run string
	switch pm {
	case APK:
		run = "RUN apk upgrade --no-cache " + strings.Join(pkgs, " ")
	case APT:
		run = "RUN apt-get update && apt-get install -y --no-install-recommends --only-upgrade " + strings.Join(pkgs, " ") + " && rm -rf /var/lib/apt/lists/*"
	default:
		return nil, fmt.Errorf("no package manager to upgrade %s with", strings.Join(pkgs, ", "))
	}
	return []string{BlockBegin, blockNote, run, BlockEnd}, nil
}

// PatchDockerfile returns src without its managed block and, when pkgs is not empty, with a new
// one that upgrades pkgs with pm right after the FROM of the stage the final image starts from
// (followed back through FROM <stage>). Right after FROM, the block runs as the base image's user
// (root in the images it patches); a USER instruction of the stage applies only after it.
func PatchDockerfile(src []byte, pm string, pkgs []string) ([]byte, error) {
	lines := strings.Split(string(src), "\n")
	var kept []string
	in := false
	for _, l := range lines {
		switch t := strings.TrimSpace(l); {
		case t == BlockBegin && !in:
			in = true
		case t == BlockEnd && in:
			in = false
		case t == BlockBegin || t == BlockEnd:
			return nil, fmt.Errorf("unbalanced %q / %q markers", BlockBegin, BlockEnd)
		case !in:
			kept = append(kept, l)
		}
	}
	if in {
		return nil, fmt.Errorf("%q without %q", BlockBegin, BlockEnd)
	}
	out := strings.Join(kept, "\n")
	if len(pkgs) == 0 {
		return []byte(out), nil
	}
	b, err := block(pm, pkgs)
	if err != nil {
		return nil, err
	}
	d, err := ParseDockerfile([]byte(out))
	if err != nil {
		return nil, err
	}
	s := d.Stages[d.FinalBase()]
	if s.Scratch() {
		return nil, errors.New("the final image starts from scratch: no packages to upgrade")
	}
	kept = append(kept[:s.End], append(b, kept[s.End:]...)...)
	return []byte(strings.Join(kept, "\n")), nil
}
