# plugin-probe

`cmd/plugin-probe` loads CCF agent plugins the way the agent does and reports, for each one, the
plugin protocol it speaks (v1 or v2) and the agent library version it was built with. It also
checks policy bundles with the agent's OPA version. It answers the question a release needs
answered before an agent change ships: do the plugins and policies in the field still load?

The probe imports `github.com/compliance-framework/agent/runner` at the version in this repo's
`go.mod` (`ccf-bump` moves it), so it loads plugins exactly as that agent release would:
same `HandshakeConfig`, same `"runner"` dispense name, same gRPC client.

## What it checks

For each **plugin** (an OCI tag, a binary, or a directory holding a `plugin` binary):

1. An OCI tag is downloaded with gooci's library (`pkg/oci`, the agent's downloader), choosing
   the image for the probe's platform (`--platform`, default the host's `os/arch`). The tag's
   registry digest and its `org.ccf.plugin.protocol.version` annotation are recorded.
2. The agent library version is read from the binary's Go build info (`debug/buildinfo`). A
   replaced dependency shows as `v0.9.0 => ../agent`. A binary without build info, or without the
   agent dependency, gets a warning, not a failure.
3. The plugin is launched with `runner.HandshakeConfig` (gRPC only), `"runner"` is dispensed,
   and `Init` is called with an empty `InitRequest`:
   - `codes.Unimplemented` means **v1**;
   - any other answer means **v2**. An error Init returns for the empty request is recorded as
     `init_error`, for information only. Templates the plugin sends during Init are dropped.

A plugin **fails to load** if it can't be downloaded or found, doesn't start or complete the
handshake, can't be dispensed, exits during `Init` (for example a crash on the empty request), or
`Init` doesn't return within `--timeout` (default 1m). It also fails if it is annotated as
protocol 2 but is v1, because the agent would then refuse to start it. A v2 plugin annotated as 1,
or not annotated, gets a warning: the agent runs it as v1, and never calls `Init`, unless the
plugin config sets `protocol_version: 2`.

Plugins run with only `PATH`, `HOME` and `TMPDIR` from the probe's environment (plus go-plugin's
handshake variables), so tokens in a CI job's environment never reach a plugin binary.
The probe still executes every plugin it loads, and gooci extracts layers without confining their
paths to the work directory, so probe only artifacts you would run.

For each **policy bundle** (an OCI tag, or a directory holding `policies/`):

1. An OCI tag is downloaded with gooci, and its digest recorded.
2. `policies/` must exist: it is the directory the agent evaluates and uploads as the bundle.
3. Every `.rego` file under it is parsed and compiled as `opa check policies/` does (Rego v1, no
   `--strict`, which the agent doesn't apply either), with the OPA library the probe is built
   with. `go.mod` pins that OPA to the version the agent requires, and
   `TestOPAMatchesAgent` fails if the two drift apart. A bundle without `.rego` files fails.

## Output

The markdown report goes to stdout (or `--markdown FILE`), and `--json FILE` writes the JSON
report (`-` for stdout; only one of the two can use stdout). The exit status is 1 if any plugin
fails to load or any policy bundle fails its check, and both reports are still written.

```console
$ go run ./cmd/plugin-probe --plugins bin/mock-plugin-1,bin/mock-plugin-2,ghcr.io/compliance-framework/plugin-github-settings:v0.5.0 --json report.json
## Plugin probe

Agent library v0.9.0, OPA v1.14.1.

| Plugin | Protocol | Agent library | Result | Notes |
| --- | --- | --- | --- | --- |
| `bin/mock-plugin-1` | v2 | v0.9.0 | ok |  |
| `bin/mock-plugin-2` | v1 | v0.8.1 | ok |  |
| `ghcr.io/compliance-framework/plugin-github-settings:v0.5.0` | v2 | v0.8.0-rc4 | ok |  |
```

```json
{
  "agent_version": "v0.9.0",
  "opa_version": "v1.14.1",
  "plugins": [
    {
      "source": "ghcr.io/compliance-framework/plugin-github-settings:v0.5.0",
      "digest": "sha256:872c2b121d681b54d83a0e6acf85023457fe31eddd07a8ff743e6aa3b97f32ae",
      "annotated_protocol": 2,
      "protocol": 2,
      "lib_version": "v0.8.0-rc4"
    }
  ],
  "policies": []
}
```

| Field | Meaning |
| --- | --- |
| `agent_version`, `opa_version` | The agent library and OPA the probe is built with. |
| `plugins[].protocol` | `1` or `2`; absent when the plugin failed to load. |
| `plugins[].lib_version` | The agent library in the binary's build info; `""` when there is none. |
| `plugins[].annotated_protocol` | The OCI protocol annotation; absent when there is none. |
| `plugins[].digest`, `policies[].digest` | The registry digest of an OCI source's tag. |
| `plugins[].init_error` | What a v2 plugin's `Init` returned for the empty request. |
| `plugins[].warnings` | Problems that don't stop the agent loading the plugin. |
| `policies[].modules` | The number of Rego modules checked. |
| `error` | Why the plugin failed to load or the bundle failed its check; absent when it passed. |

## Flags

| Flag | Default | What |
| --- | --- | --- |
| `--plugins` | | Comma- or whitespace-separated plugins. |
| `--policies` | | Comma- or whitespace-separated policy bundles. |
| `--json` | none | File for the JSON report, `-` for stdout. |
| `--markdown` | `-` | File for the markdown report, `-` for stdout, empty for none. |
| `--work-dir` | a temporary directory, removed | Where OCI artifacts are extracted, one subdirectory each; kept when set. |
| `--platform` | the host's `os/arch` | The plugin image to pull from a multi-platform index. |
| `--timeout` | `1m` | Bound on starting each plugin and on its `Init` call. |
| `--log-level` | `warn` | go-plugin's log level; `debug` shows the plugins' own logs. |

Registry credentials come from the Docker config and, for ECR, the ECR credential helper (gooci's
keychain); public GHCR packages need none.

## Running it locally

To probe plugins that have no OCI release yet, build them and pass the binaries:

```sh
git clone --depth 1 https://github.com/compliance-framework/mock-plugin-1 /tmp/mock-plugin-1
go -C /tmp/mock-plugin-1 build -o /tmp/bin/mock-plugin-1 .
go run ./cmd/plugin-probe --plugins /tmp/bin/mock-plugin-1 --policies ../mock-plugin-policies-1
```

To probe with an unreleased agent, point `go.mod` at it with a temporary `replace` (don't
commit it); the report then shows `agent_version` as replaced.

## `plugin-probe.yml`

A reusable workflow (`workflow_call`, and `workflow_dispatch` for trying it by hand) that builds
`cmd/plugin-probe` from the commit the workflow was called at and runs it.

| Input | Default | What |
| --- | --- | --- |
| `plugins` | required | Plugins to probe, comma- or newline-separated OCI tags. |
| `agent-ref` | this repo's `go.mod` | An agent version, tag, branch or commit. The job runs `go get github.com/compliance-framework/agent@<ref>`, then moves OPA to the version that agent's `go.mod` requires, and fails if it can't. |
| `policies` | none | Policy bundles to check, comma- or newline-separated OCI tags. |

The markdown report becomes the job summary, the JSON report is printed to the log and returned
as the `report` output (one line of JSON), and the job fails as the probe does. It needs only
`contents: read`; the plugins and bundles must be pullable anonymously.

```yaml
jobs:
  probe:
    uses: compliance-framework/workflows/.github/workflows/plugin-probe.yml@<sha> # vX.Y.Z
    permissions:
      contents: read
    with:
      agent-ref: v0.9.0
      plugins: |
        ghcr.io/compliance-framework/plugin-github-settings:v0.5.0
```

Workstream 2 runs the probe locally first; workstream 3 wires the workflow into the agent's CI
and the release train.
