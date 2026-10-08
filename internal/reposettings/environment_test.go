package reposettings

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// secretValues are the release environment's secrets as the repo-settings job passes them.
var secretValues = map[string]string{"RELEASE_BOT_APP_ID": "123", "RELEASE_BOT_PRIVATE_KEY": "-----BEGIN KEY-----s3cr3t"}

func TestEnvironmentCreated(t *testing.T) {
	f := newFake("a")
	opts := Options{Owner: "o", Repos: []string{"a"}, Desired: desiredAll(t, false), Secrets: secretValues}
	var out bytes.Buffer
	if err := Run(context.Background(), f, opts, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"  environment[release]: missing -> create",
		"  environment[release].policy[branch:main]: (unset) -> add",
		"  environment[release].policy[tag:v*.*.*]: (unset) -> add",
		"  environment[release].policy[tag:*-v*.*.*]: (unset) -> add",
		"  environment[release].secret.RELEASE_BOT_APP_ID: missing -> set",
		"  environment[release].secret.RELEASE_BOT_PRIVATE_KEY: missing -> set",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}

	opts.Apply = true
	f.writes = nil
	out.Reset()
	if err := Run(context.Background(), f, opts, &out); err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, w := range f.writes {
		if strings.Contains(w, " env ") || strings.Contains(w, "policy") || strings.HasPrefix(w, "secret ") {
			env = append(env, w)
		}
	}
	// The policies are in place before any secret is written.
	want := []string{"put env release o/a", "add policy release branch:main o/a", "add policy release tag:v*.*.* o/a",
		"add policy release tag:*-v*.*.* o/a", "secret release RELEASE_BOT_APP_ID o/a", "secret release RELEASE_BOT_PRIVATE_KEY o/a"}
	if !slices.Equal(env, want) {
		t.Errorf("environment writes = %v, want %v", env, want)
	}
	if got := f.repos["o/a"].envs["release"].secrets; got["RELEASE_BOT_PRIVATE_KEY"] != secretValues["RELEASE_BOT_PRIVATE_KEY"] || got["RELEASE_BOT_APP_ID"] != "123" {
		t.Errorf("secrets = %v", got)
	}
	if strings.Contains(out.String(), "s3cr3t") {
		t.Errorf("a secret value is in the output:\n%s", out.String())
	}

	// Idempotent: the secrets exist, so nothing is written (their values can't be read back).
	f.writes = nil
	out.Reset()
	if err := Run(context.Background(), f, opts, &out); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) > 0 || !strings.Contains(out.String(), "o/a: up to date") {
		t.Errorf("second apply: writes = %v, output:\n%s", f.writes, out.String())
	}
}

// TestEnvironmentTightened: an environment every branch may use is switched to the desired
// policies, a stray policy is deleted, and a present secret is left as it is.
func TestEnvironmentTightened(t *testing.T) {
	f := inSync(t)
	r := f.repos["o/a"]
	r.envs = map[string]*fakeEnv{"release": {secrets: map[string]string{"RELEASE_BOT_APP_ID": "old", "OTHER": "x"}}}
	opts := Options{Owner: "o", Repos: []string{"a"}, Desired: desiredAll(t, false), Secrets: secretValues, Apply: true}
	var out bytes.Buffer
	if err := Run(context.Background(), f, opts, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "  environment[release].deployment_branch_policy: all branches -> custom") {
		t.Errorf("output:\n%s", out.String())
	}
	e := r.envs["release"]
	if !e.custom || len(e.policies) != 3 || e.secrets["RELEASE_BOT_APP_ID"] != "old" || e.secrets["RELEASE_BOT_PRIVATE_KEY"] == "" || e.secrets["OTHER"] != "x" {
		t.Errorf("environment = %+v", e)
	}

	e.policies = append(e.policies, DeploymentPolicy{ID: 900, Name: "*", Type: "branch"})
	f.writes = nil
	out.Reset()
	if err := Run(context.Background(), f, opts, &out); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.writes, []string{"delete policy release 900 o/a"}) || !strings.Contains(out.String(), "environment[release].policy[branch:*]: present -> delete") {
		t.Errorf("writes = %v, output:\n%s", f.writes, out.String())
	}
}

func TestEnvironmentRotateSecrets(t *testing.T) {
	f := inSync(t)
	f.repos["o/a"].envs = map[string]*fakeEnv{"release": {custom: true, secrets: map[string]string{"RELEASE_BOT_APP_ID": "old", "RELEASE_BOT_PRIVATE_KEY": "old"}}}
	opts := Options{Owner: "o", Repos: []string{"a"}, Desired: desiredAll(t, true), Secrets: secretValues, Apply: true}
	for _, p := range []string{"main", "v*.*.*", "*-v*.*.*"} {
		typ := map[bool]string{true: "branch", false: "tag"}[p == "main"]
		f.repos["o/a"].envs["release"].policies = append(f.repos["o/a"].envs["release"].policies, DeploymentPolicy{ID: int64(len(p)), Name: p, Type: typ})
	}
	var out bytes.Buffer
	if err := Run(context.Background(), f, opts, &out); err != nil {
		t.Fatal(err)
	}
	if want := []string{"secret release RELEASE_BOT_APP_ID o/a", "secret release RELEASE_BOT_PRIVATE_KEY o/a"}; !slices.Equal(f.writes, want) {
		t.Errorf("writes = %v, want %v", f.writes, want)
	}
	if !strings.Contains(out.String(), "environment[release].secret.RELEASE_BOT_PRIVATE_KEY: present -> rotate") ||
		f.repos["o/a"].envs["release"].secrets["RELEASE_BOT_APP_ID"] != "123" {
		t.Errorf("output:\n%s", out.String())
	}
}

// TestEnvironmentSecretsWaitForPolicies: a secret is never written to an environment whose
// policies failed, nor without a value.
func TestEnvironmentSecretsWaitForPolicies(t *testing.T) {
	f := inSync(t)
	f.fail = map[string]error{"add policy release tag:v*.*.* o/a": errors.New("422 boom")}
	opts := Options{Owner: "o", Repos: []string{"a"}, Desired: desiredAll(t, false), Secrets: secretValues, Apply: true}
	var out bytes.Buffer
	err := Run(context.Background(), f, opts, &out)
	if err == nil || !strings.Contains(out.String(), "environment[release].secret.RELEASE_BOT_PRIVATE_KEY failed: skipped: the environment's deployment policies are not in place") {
		t.Fatalf("err = %v, output:\n%s", err, out.String())
	}
	if len(f.repos["o/a"].envs["release"].secrets) > 0 {
		t.Errorf("secrets written: %v", f.repos["o/a"].envs["release"].secrets)
	}

	f.fail = nil
	opts.Secrets = map[string]string{"RELEASE_BOT_APP_ID": "123"}
	out.Reset()
	err = Run(context.Background(), f, opts, &out)
	if err == nil || !strings.Contains(out.String(), "environment[release].secret.RELEASE_BOT_PRIVATE_KEY failed: no value for RELEASE_BOT_PRIVATE_KEY") {
		t.Fatalf("err = %v, output:\n%s", err, out.String())
	}
	if s := f.repos["o/a"].envs["release"].secrets; s["RELEASE_BOT_APP_ID"] != "123" || s["RELEASE_BOT_PRIVATE_KEY"] != "" {
		t.Errorf("secrets = %v", s)
	}
}
