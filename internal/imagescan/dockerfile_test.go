package imagescan

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseDockerfile(t *testing.T) {
	const digest = "sha256:275e79ff9fc4650f2aa7029a371beeab1a4101dcb975f2b29038e391880de902"
	for _, tc := range []struct {
		name string
		src  string
		want []string // "<ref> <stage> <role> <line>"
	}{
		{
			name: "ui: FROM <stage> as the final stage",
			src: `FROM node:22 AS build
WORKDIR /app
RUN npm run build

# production stage
FROM nginx:stable-alpine AS production
RUN apk upgrade --no-cache

# Default to production build
FROM production
`,
			want: []string{"node:22 build build 1", "nginx:stable-alpine production final 6"},
		},
		{
			name: "ARG before the first FROM, with defaults and substitutions",
			src: `ARG GO_VERSION=1.26.1
ARG BASE="gcr.io/distroless/base-debian12"
ARG UNSET
FROM golang:${GO_VERSION} AS builder
ARG VERSION=dev
FROM ${BASE}${UNSET:-}:${TAG:-nonroot}
`,
			want: []string{"golang:1.26.1 builder build 4", "gcr.io/distroless/base-debian12:nonroot 1 final 6"},
		},
		{
			name: "a stage ARG is not visible to a later FROM",
			src: `FROM alpine:3.20
ARG IMG=debian
FROM ${IMG:-busybox}:latest
`,
			want: []string{"alpine:3.20 0 build 1", "busybox:latest 1 final 3"},
		},
		{
			name: "--platform, lower-case keywords, digests and continuations",
			src: `from --platform=$BUILDPLATFORM golang:1.26 as builder
FROM \
  cloudcustodian/c7n@` + digest + `
`,
			want: []string{"golang:1.26 builder build 1", "cloudcustodian/c7n@" + digest + " 1 final 2"},
		},
		{
			name: "scratch final stage: no final base image",
			src: `FROM golang:1.26 AS build
FROM scratch
COPY --from=build /app /app
`,
			want: []string{"golang:1.26 build build 1"},
		},
		{
			name: "a FROM <stage> chain, alias matched case-insensitively",
			src: `FROM debian:bookworm-slim AS Base
FROM base AS final
FROM FINAL
`,
			want: []string{"debian:bookworm-slim Base final 1"},
		},
		{
			name: "registry port and tag with digest",
			src:  "FROM localhost:5000/team/app:1.2.3@" + digest + "\n",
			want: []string{"localhost:5000/team/app:1.2.3@" + digest + " 0 final 1"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := ParseDockerfile([]byte(tc.src))
			if err != nil {
				t.Fatal(err)
			}
			bases, err := d.Bases()
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, b := range bases {
				got = append(got, fmt.Sprintf("%s %s %s %d", b.Ref, b.Stage, b.Role, b.Line))
			}
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Errorf("bases:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

func TestParseDockerfileErrors(t *testing.T) {
	for _, tc := range []struct{ src, err string }{
		{"RUN true\n", "no FROM"},
		{"FROM\n", "want FROM"},
		{"FROM a b\n", "want FROM"},
		{"FROM ${NOPE}\n", "empty after ARG substitution"},
	} {
		if _, err := ParseDockerfile([]byte(tc.src)); err == nil || !strings.Contains(err.Error(), tc.err) {
			t.Errorf("%q: err = %v, want one containing %q", tc.src, err, tc.err)
		}
	}
	d, err := ParseDockerfile([]byte("FROM node@sha256:abc\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Bases(); err == nil || !strings.Contains(err.Error(), "bad digest") {
		t.Errorf("short digest: err = %v", err)
	}
}

func TestParseRef(t *testing.T) {
	for _, tc := range []struct {
		in                string
		name, tag, digest string
	}{
		{"node", "node", "", ""},
		{"node:20", "node", "20", ""},
		{"ghcr.io/compliance-framework/agent:0.9.3", "ghcr.io/compliance-framework/agent", "0.9.3", ""},
		{"localhost:5000/app", "localhost:5000/app", "", ""},
	} {
		r, err := ParseRef(tc.in)
		if err != nil || r.Name != tc.name || r.Tag != tc.tag || r.Digest != tc.digest || r.String() != tc.in {
			t.Errorf("ParseRef(%q) = %+v, %v", tc.in, r, err)
		}
	}
	// Anything else would reach docker's command line: a leading "-" is a flag there.
	for _, bad := range []string{"node:", "", "$IMG:1", "-v", "--network=host", "node:-x", "a b", "node@sha256:ABC", "Repo/Upper"} {
		if _, err := ParseRef(bad); err == nil {
			t.Errorf("ParseRef(%q): want an error", bad)
		}
	}
}

func TestNewestPatch(t *testing.T) {
	tags := []string{"1.26", "1.26.1", "1.26.2", "1.26.10", "1.26.11-alpine", "1.27.0", "1.26.3-rc1", "latest"}
	for _, tc := range []struct {
		current, want string
		ok            bool
	}{
		{"1.26.1", "1.26.10", true},
		{"1.26.10", "", false},
		{"1.26.1-alpine", "1.26.11-alpine", true},
		{"1.26", "", false}, // a floating minor: its candidate is its own current digest
		{"stable-alpine", "", false},
	} {
		got, ok := newestPatch(tc.current, tags)
		if got != tc.want || ok != tc.ok {
			t.Errorf("newestPatch(%q) = %q, %v; want %q, %v", tc.current, got, ok, tc.want, tc.ok)
		}
	}
}
