# Bot PRs that need a human

Most bot PRs merge on their own: Renovate merges its non-major group, ccf-bump turns on auto-merge,
and the train merges its bump and release PRs. The rest wait for a person: a major update, an OPA
bump in the api, a ccf-bump PR whose auto-merge is off. Without a nudge they sit there until the
next train trips over them (Renovate majors on `mock-ui`, #13 and #14, waited for weeks).

The team works in Slack, so these PRs are surfaced there, not through GitHub review requests.
Every bot PR that needs a human carries one label, **`needs-human`** (the same name as the train's
`needs-human` status, see [train.md](train.md)), color `d93f0b`, "A bot PR waiting for a person".

## Who adds the label

| Bot | PRs | How |
| --- | --- | --- |
| Renovate (`renovate/default.json`) | Majors, and OPA in the api (its own `renovate/opa` PR, never auto-merged) | The rules add `"addLabels": ["needs-human"]`, merged with any other labels. Renovate sets labels when it opens the PR; GitHub creates the label if the repo lacks it. |
| ccf-bump | A PR whose auto-merge is off: a major update or a pin that was not a version, or a PR on which GitHub refused auto-merge | After opening or updating the PR, ccf-bump adds the label, creating it with the color and description above when the repo lacks it. A failure is a warning, as for auto-merge: the PR is open either way. `--dry-run` prints `would add label needs-human`. |

Both run as `ccf-release-bot`; adding and creating a label needs **Pull requests: write** (or
Issues: write), which their live tokens already have.
