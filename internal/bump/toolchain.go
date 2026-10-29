package bump

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	goversion "go/version"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"golang.org/x/mod/modfile"
)

// goToolchain is the `toolchain goX.Y.Z` line of the root go.mod (DepToolchain). Go
// standard-library security fixes reach a repo only through it: Renovate matches OSV's stdlib
// vulnerabilities for the toolchain line alone (docs/renovate.md). go get drops the line when it
// raises the go line to or past it, and go mod tidy drops one equal to the go line, so KeepToolchain
// puts it back; Decide never plans it.
type goToolchain struct{ c Config }

func (goToolchain) Name() string { return "go toolchain" }

// toolchainKey is the Key of the toolchain line's Ref.
const toolchainKey = "toolchain"

// readMod parses the root go.mod of dir with modfile.Parse (ParseLax drops the toolchain line);
// nil without one.
func readMod(dir string) (*modfile.File, error) {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return modfile.Parse("go.mod", data, nil)
}

func (g goToolchain) Scan(dir string) ([]Ref, error) {
	f, err := readMod(dir)
	if err != nil || f == nil || f.Toolchain == nil {
		return nil, err
	}
	return []Ref{{Updater: g.Name(), Dep: DepToolchain, Key: toolchainKey, Current: f.Toolchain.Name, File: "go.mod"}}, nil
}

// Apply writes the toolchain line (the one change, To: go1.26.10), then runs go mod tidy, and
// fails if tidy removed it again.
func (g goToolchain) Apply(ctx context.Context, dir string, changes []Change) error {
	if len(changes) != 1 {
		return fmt.Errorf("want one toolchain change, got %d", len(changes))
	}
	to := changes[0].To
	f, err := readMod(dir)
	if err != nil {
		return err
	}
	if f == nil {
		return errors.New("no go.mod")
	}
	if err := f.AddToolchainStmt(to); err != nil {
		return err
	}
	data, err := f.Format()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "go.mod")
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, info.Mode().Perm()); err != nil {
		return err
	}
	if err := g.c.Run(ctx, dir, "go", "mod", "tidy"); err != nil {
		return err
	}
	if f, err = readMod(dir); err != nil {
		return err
	}
	if f.Toolchain == nil || f.Toolchain.Name != to {
		return fmt.Errorf("go mod tidy removed toolchain %s again", to)
	}
	return nil
}

// GoReleases lists the stable Go releases (go1.26.10, go1.27.0).
type GoReleases func(ctx context.Context) ([]string, error)

// outsideClient reads go.dev; like the GitHub client, a stalled server can't hang the run (the
// train's dry run must warn, not be killed by the job's timeout).
var outsideClient = &http.Client{Timeout: 30 * time.Second}

// GoDev is the GoReleases of go.dev's download list (all releases, not only the two supported
// lines).
func GoDev(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://go.dev/dl/?mode=json&include=all", nil)
	if err != nil {
		return nil, err
	}
	resp, err := outsideClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("go.dev/dl: %s", resp.Status)
	}
	var rels []struct {
		Version string `json:"version"`
		Stable  bool   `json:"stable"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rels); err != nil {
		return nil, fmt.Errorf("go.dev/dl: %w", err)
	}
	var out []string
	for _, r := range rels {
		if r.Stable {
			out = append(out, r.Version)
		}
	}
	return out, nil
}

// toolchainTarget returns the toolchain to restore once go get raised the go line to goLine and
// dropped old: the newest stable Go release, of any minor, when it is above goLine (Renovate moves
// the line across minors too: go 1.26.10 gets toolchain go1.27.3), else old when it is still above
// goLine; "" when neither is, i.e. goLine is the newest release.
func toolchainTarget(goLine, old string, stable []string) string {
	want, best := "go"+goLine, ""
	for _, v := range stable {
		if goversion.IsValid(v) && goversion.Compare(v, want) > 0 && (best == "" || goversion.Compare(v, best) > 0) {
			best = v
		}
	}
	if best == "" && goversion.IsValid(old) && goversion.Compare(old, want) > 0 {
		best = old
	}
	return best
}

// ToolchainDropped is the hold reason of a plan whose go get dropped the toolchain line while its
// new go line is the newest Go release, so no toolchain above it exists yet: the repo would get no
// stdlib security PRs.
const ToolchainDropped = "go get dropped the go.mod toolchain line, and the new go line is the newest Go release, " +
	"so there is no toolchain above it to restore: without the line the repo gets no Go standard-library security PRs " +
	"(docs/ccf-bump.md#the-toolchain-line), and CI's toolchain line check fails this PR until a newer Go is released; " +
	"then push `toolchain goX.Y.Z` (above the go line) to this branch, or let the next ccf-bump run restore it, and merge"

// KeepToolchain keeps the root go.mod's toolchain line, after ApplyAll applied the plan to the
// checkout in dir: before (the ScanAll of the checkout as cloned) says whether there was one. If
// go get or go mod tidy dropped it, it writes `toolchain <toolchainTarget>` back, runs go mod tidy
// and checks the line stayed; the change is added to the plan (and returned) like any other, so
// the PR lists it and FollowGo's images build it. With no toolchain above the new go line it adds
// ToolchainDropped to the plan's Holds. A repo that had no toolchain line gets none. Reading
// go.dev's releases failing is a *RegistryError, returned before anything is written.
func KeepToolchain(ctx context.Context, dir string, us []Updater, before []Ref, p Plan) (Plan, []Change, error) {
	i := slices.IndexFunc(us, func(u Updater) bool { _, ok := u.(goToolchain); return ok })
	j := slices.IndexFunc(before, func(r Ref) bool { return r.Dep == DepToolchain })
	if i < 0 || j < 0 || !slices.ContainsFunc(p.Changes, Change.GoModule) {
		return p, nil, nil
	}
	f, err := readMod(dir)
	if err != nil || f == nil || f.Toolchain != nil || f.Go == nil {
		return p, nil, err
	}
	g := us[i].(goToolchain)
	if g.c.GoReleases == nil {
		return p, nil, &RegistryError{errors.New("no list of Go releases to restore the toolchain line from")}
	}
	stable, err := g.c.GoReleases(ctx)
	if err != nil {
		return p, nil, &RegistryError{fmt.Errorf("the list of Go releases: %w", err)}
	}
	to := toolchainTarget(f.Go.Version, before[j].Current, stable)
	if to == "" {
		p.Holds = append(slices.Clone(p.Holds), ToolchainDropped)
		return p, nil, nil
	}
	c := Change{Ref: before[j], To: to}
	if err := g.Apply(ctx, dir, []Change{c}); err != nil {
		return p, nil, fmt.Errorf("%s: %w", g.Name(), err)
	}
	p.Changes = append(slices.Clone(p.Changes), c)
	return p, []Change{c}, nil
}
