package imagescan

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Runtime is a language runtime the scanners don't inventory (they flag the Debian brotli package
// in node:20 but not the brotli inside its Node binary), checked by its version against OSV.
type Runtime struct {
	Name string // shown in findings: "node"
	// Env is the variable the official images set to the runtime's version (NODE_VERSION=20.20.2).
	Env string
	// Binary is run with --version when an image has no Env: a runtime installed another way.
	Binary string
	// Ecosystem and Package are the runtime's OSV package.
	Ecosystem, Package string
}

// Runtimes are the runtimes the check knows. Add one with the OSV package that tracks its
// releases; the EOL rule needs an entry in eolLines as well.
var Runtimes = []Runtime{
	{Name: "node", Env: "NODE_VERSION", Binary: "node", Ecosystem: "Bitnami", Package: "node"},
}

// OSVAPI is the OSV query endpoint's base URL.
const OSVAPI = "https://api.osv.dev"

// OSV queries api.osv.dev.
type OSV struct {
	BaseURL string // OSVAPI
	HTTP    *http.Client
	cache   vulnCache // the advisories Vuln fetched
}

// Query returns the advisories that affect version of package name in ecosystem.
func (o *OSV) Query(ctx context.Context, ecosystem, name, version string) ([]Vuln, error) {
	var out []Vuln
	token := ""
	for {
		q := map[string]any{"package": map[string]string{"name": name, "ecosystem": ecosystem}, "version": version}
		if token != "" {
			q["page_token"] = token
		}
		body, err := json.Marshal(q)
		if err != nil {
			return nil, err
		}
		var resp struct {
			Vulns         []Vuln `json:"vulns"`
			NextPageToken string `json:"next_page_token"`
		}
		if err := postJSON(ctx, o.HTTP, strings.TrimRight(o.BaseURL, "/")+"/v1/query", body, &resp); err != nil {
			return nil, fmt.Errorf("OSV query %s %s@%s: %w", ecosystem, name, version, err)
		}
		out = append(out, resp.Vulns...)
		if resp.NextPageToken == "" {
			return out, nil
		}
		token = resp.NextPageToken
	}
}

// runtimeFindings turns the advisories of a runtime version into findings; Fixed lists every
// fixed version above the installed one, lowest first.
func runtimeFindings(rt Runtime, image, version string, vulns []Vuln) []Finding {
	var out []Finding
	for _, v := range vulns {
		f := Finding{
			Image:     image,
			Package:   rt.Name,
			Source:    rt.Package,
			Type:      TypeBinary,
			Ecosystem: rt.Ecosystem,
			Installed: version,
			IDs:       []string{v.ID},
			Aliases:   v.aliases(),
			Score:     v.score(),
		}
		for _, fx := range v.fixed(rt.Ecosystem, rt.Package) {
			if semverLess(version, fx) && !slices.Contains(f.Fixed, fx) {
				f.Fixed = append(f.Fixed, fx)
			}
		}
		slices.SortFunc(f.Fixed, func(a, b string) int { return slices.Compare(numbers(a), numbers(b)) })
		out = append(out, f)
	}
	return out
}

// envValue returns NAME's value in a container's environment (NAME=value entries).
func envValue(env []string, name string) string {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, name+"="); ok {
			return v
		}
	}
	return ""
}

func postJSON(ctx context.Context, client *http.Client, url string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return doJSON(client, req, out)
}

func getJSON(ctx context.Context, client *http.Client, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	return doJSON(client, req, out)
}

func doJSON(client *http.Client, req *http.Request, out any) error {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return &HTTPError{Method: req.Method, URL: req.URL.String(), Code: resp.StatusCode, Body: string(data)}
	}
	return json.Unmarshal(data, out)
}

// HTTPError is a response with a status other than 2xx.
type HTTPError struct {
	Method, URL string
	Code        int
	Body        string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s %s: %d %s: %.200s", e.Method, e.URL, e.Code, http.StatusText(e.Code), e.Body)
}
