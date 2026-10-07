package vulnsummary

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestRepoAlertsFollowsLinkPages(t *testing.T) {
	var srv *httptest.Server
	var queries []string
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/dependabot/alerts" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q", got)
		}
		queries = append(queries, r.URL.RawQuery)
		if r.URL.Query().Get("after") == "" {
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/o/r/dependabot/alerts?state=open&per_page=100&after=c1>; rel="next", <%s/x>; rel="first"`, srv.URL, srv.URL))
			_, _ = w.Write([]byte(`[{"security_advisory":{"severity":"high"}},{"security_advisory":{"severity":"critical"}}]`))
			return
		}
		_, _ = w.Write([]byte(`[{"security_advisory":{},"security_vulnerability":{"severity":"low"}}]`))
	}))
	defer srv.Close()

	g := &GitHub{BaseURL: srv.URL + "/", Token: "tok"}
	alerts, err := g.RepoAlerts(context.Background(), "o", "r")
	if err != nil {
		t.Fatal(err)
	}
	want := []Alert{{"r", "high"}, {"r", "critical"}, {"r", "low"}}
	if fmt.Sprint(alerts) != fmt.Sprint(want) {
		t.Fatalf("alerts = %v, want %v", alerts, want)
	}
	if len(queries) != 2 {
		t.Fatalf("queries = %v", queries)
	}
	if q, _ := url.ParseQuery(queries[0]); q.Get("state") != "open" || q.Get("per_page") != "100" {
		t.Errorf("first query = %q", queries[0])
	}
}

func TestErrorsLeaveOutTheToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Dependabot alerts are disabled for this repository."}`, http.StatusForbidden)
	}))
	defer srv.Close()
	_, err := (&GitHub{BaseURL: srv.URL, Token: "secret-token"}).RepoAlerts(context.Background(), "o", "r")
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error leaks the token: %v", err)
	}
}

func TestNextURL(t *testing.T) {
	base, _ := url.Parse("https://api.github.com")
	for _, tc := range []struct{ header, want, err string }{
		{header: "", want: ""},
		{header: `<https://api.github.com/x?after=a>; rel="prev"`, want: ""},
		{header: `<https://api.github.com/x?after=a>; rel="prev", <https://api.github.com/x?after=b>; rel="next"`, want: "https://api.github.com/x?after=b"},
		{header: `<https://evil.example/x?after=b>; rel="next"`, err: "not on the API host"},
		{header: `<http://api.github.com/x>; rel="next"`, err: "not on the API host"},
	} {
		got, err := nextURL(tc.header, base)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("nextURL(%q) err = %v, want %q", tc.header, err, tc.err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("nextURL(%q) = %q, %v; want %q", tc.header, got, err, tc.want)
		}
	}
}

func TestListStopsAtMaxPages(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", "<"+srv.URL+r.URL.Path+`?after=x>; rel="next"`)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	_, err := (&GitHub{BaseURL: srv.URL, Token: "t"}).RepoAlerts(context.Background(), "o", "r")
	if err == nil || !strings.Contains(err.Error(), "pages") {
		t.Fatalf("err = %v", err)
	}
}
