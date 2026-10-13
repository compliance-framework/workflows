package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/compliance-framework/workflows/internal/manifest"
)

func TestRun(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(bad, []byte("repos:\n  - {name: a, kind: go-lib, release: true, depends_on: [a]}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		args    []string
		want    string
		wantErr string
	}{
		{
			name: "text",
			args: []string{"--manifest", "../../repos.yaml"},
			// Batches 1 and 2 are active: their released plugin and policy repos join agent-action in stage 3.
			want: "stage 1: api gooci\nstage 2: agent ui\nstage 3: agent-action " +
				"plugin-apt-versions plugin-apt-versions-policies " +
				"plugin-aws-acm plugin-aws-acm-certificate-policies plugin-aws-ec2 plugin-aws-ec2-policies " +
				"plugin-aws-ecr plugin-aws-ecr-image-policies plugin-aws-ecr-registry-policies plugin-aws-ecr-repository-policies " +
				"plugin-aws-eks plugin-aws-eks-addon-policies plugin-aws-eks-nodegroup-policies plugin-aws-eks-policies " +
				"plugin-aws-elb plugin-aws-elb-listener-policies plugin-aws-elb-loadbalancer-policies " +
				"plugin-aws-elb-targetgroup-policies plugin-aws-elb-targethealth-policies " +
				"plugin-aws-rds-aurora-psql plugin-aws-rds-aurora-psql-policies plugin-aws-s3 plugin-aws-s3-policies " +
				"plugin-aws-secretsmanager plugin-aws-secretsmanager-policies " +
				"plugin-cloud-custodian plugin-cloud-custodian-policies " +
				"plugin-dependabot plugin-dependabot-granular-policies plugin-dependabot-policies " +
				"plugin-file-attestation plugin-file-attestation-ctrf-policies plugin-file-attestation-policies plugin-file-attestation-sarif-policies " +
				"plugin-github-repositories plugin-github-repositories-custom-policies plugin-github-repositories-dependency-policies " +
				"plugin-github-repositories-policies plugin-github-repositories-pr-policies plugin-github-settings plugin-github-settings-policies " +
				"plugin-gitlab-repositories plugin-gitlab-repositories-policies plugin-http-collector plugin-http-collector-policies " +
				"plugin-intruder plugin-intruder-policies plugin-jira plugin-jira-custom-policies plugin-jira-policies " +
				"plugin-k8s plugin-k8s-opres-policies plugin-local-ssh plugin-local-ssh-policies\nstage 4: helm-charts\n",
		},
		{
			name: "json mock manifest",
			args: []string{"--manifest", "../../repos.mock.yaml", "--json"},
			want: `[["mock-api","mock-gooci"],["mock-agent","mock-ui"],["mock-agent-action","mock-plugin-1","mock-plugin-2","mock-plugin-policies-1","mock-plugin-policies-2"],["mock-helm-charts"]]` + "\n",
		},
		{
			name:    "invalid manifest",
			args:    []string{"--manifest", bad},
			wantErr: "depends on itself",
		},
		{
			name:    "missing manifest",
			args:    []string{"--manifest", "nope.yaml"},
			wantErr: "read manifest",
		},
		{
			name:    "extra arguments",
			args:    []string{"--manifest", "../../repos.yaml", "extra"},
			wantErr: "unexpected arguments: extra",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := run(tt.args, func(string) string { return "" }, &out, io.Discard)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("run() error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if out.String() != tt.want {
				t.Errorf("output = %q, want %q", out.String(), tt.want)
			}
		})
	}
}

// orgServer serves the organisation's repos two pages at a time: 100 others, then repos.
func orgServer(t *testing.T, repos ...manifest.OrgRepo) func(string) string {
	t.Helper()
	var first []manifest.OrgRepo
	for i := range perPage {
		first = append(first, manifest.OrgRepo{Name: fmt.Sprintf("other-%d", i)})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/orgs/compliance-framework/repos" || r.Header.Get("Authorization") != "Bearer ghs_test" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		page := first
		if r.URL.Query().Get("page") == "2" {
			page = repos
		}
		_ = json.NewEncoder(w).Encode(page)
	}))
	t.Cleanup(srv.Close)
	env := map[string]string{"GITHUB_API_URL": srv.URL, "GH_TOKEN": "ghs_test"}
	return func(k string) string { return env[k] }
}

func TestRunOrgCoverage(t *testing.T) {
	m, err := manifest.Load("../../internal/manifest/testdata/batches.yaml")
	if err != nil {
		t.Fatal(err)
	}
	listed := []manifest.OrgRepo{{Name: "plugin-old", Archived: true}}
	for _, r := range m.Repos {
		listed = append(listed, manifest.OrgRepo{Name: r.Name})
	}
	args := []string{"--manifest", "../../internal/manifest/testdata/batches.yaml", "--org", "compliance-framework"}

	var out, errOut bytes.Buffer
	if err := run(args, orgServer(t, listed...), &out, &errOut); err != nil {
		t.Fatalf("run: %v", err)
	}
	if want := "covers the 113 repos of compliance-framework"; !strings.Contains(errOut.String(), want) || !strings.HasPrefix(out.String(), "stage 1: api gooci\n") {
		t.Errorf("stdout %q, stderr %q; want the stages and %q", out.String(), errOut.String(), want)
	}

	err = run(args, orgServer(t, append(listed[1:], manifest.OrgRepo{Name: "plugin-new"})...), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "neither listed nor excluded: plugin-new") {
		t.Errorf("an unlisted plugin-* repo: err = %v", err)
	}
	if err := run(args, func(k string) string { return map[string]string{"GITHUB_API_URL": "http://127.0.0.1:1"}[k] }, io.Discard, io.Discard); err == nil {
		t.Error("an unreachable API: want an error")
	}
	if err := run(args, orgServer(t), io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "listed but not in the organisation: agent") {
		t.Errorf("no listed repo in the organisation: err = %v", err)
	}
}
