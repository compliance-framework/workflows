package imagescan

import (
	"context"
	"net/http"
	"strings"
)

// eolLine is an image whose tag starts with the version of a product endoflife.date tracks, so the
// tag's first Parts numbers name the release cycle ("node:20" is Node.js 20, "alpine:3.20" Alpine
// 3.20).
type eolLine struct {
	Image   string // the image name as written in a Dockerfile
	Product string // the endoflife.date product
	Parts   int    // how many numbers of the tag name the cycle
}

// eolLines are the images the EOL rule knows. An image tagged with a codename (bookworm-slim) or
// none of these (nginx, distroless) is never moved to another release by the rule.
var eolLines = []eolLine{
	{Image: "node", Product: "nodejs", Parts: 1},
	{Image: "debian", Product: "debian", Parts: 1},
	{Image: "alpine", Product: "alpine", Parts: 2},
}

func lineOf(image string) (eolLine, bool) {
	for _, l := range eolLines {
		if l.Image == image || l.Image == strings.TrimPrefix(image, "docker.io/library/") || "library/"+l.Image == image {
			return l, true
		}
	}
	return eolLine{}, false
}

// EOLAPI is endoflife.date's API base URL.
const EOLAPI = "https://endoflife.date/api/v1"

// Cycle is a release cycle of a product.
type Cycle struct {
	Name string // "20", "3.20"
	EOL  bool
}

// EOL reads release cycles from endoflife.date.
type EOL struct {
	BaseURL string // EOLAPI
	HTTP    *http.Client
}

// Cycles returns product's release cycles, newest first, as endoflife.date lists them.
func (e *EOL) Cycles(ctx context.Context, product string) ([]Cycle, error) {
	var resp struct {
		Result struct {
			Releases []struct {
				Name  string `json:"name"`
				IsEOL bool   `json:"isEol"`
			} `json:"releases"`
		} `json:"result"`
	}
	if err := getJSON(ctx, e.HTTP, strings.TrimRight(e.BaseURL, "/")+"/products/"+product+"/", &resp); err != nil {
		return nil, err
	}
	out := make([]Cycle, len(resp.Result.Releases))
	for i, r := range resp.Result.Releases {
		out[i] = Cycle{Name: r.Name, EOL: r.IsEOL}
	}
	return out, nil
}
