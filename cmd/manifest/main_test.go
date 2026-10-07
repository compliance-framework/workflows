package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
			want: "stage 1: api gooci\nstage 2: agent ui\nstage 3: agent-action\nstage 4: helm-charts\n",
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
			err := run(tt.args, &out, io.Discard)
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
