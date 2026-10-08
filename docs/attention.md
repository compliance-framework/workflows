# Bot PRs that need a human

Most bot PRs merge on their own: Renovate merges its non-major group, ccf-bump turns on auto-merge,
and the train merges its bump and release PRs. The rest wait for a person: a major update, an OPA
bump in the api, a ccf-bump PR whose auto-merge is off. Without a nudge they sit there until the
next train trips over them (Renovate majors on `mock-ui`, #13 and #14, waited for weeks).

Every bot PR that needs a human gets two things:

- the **`needs-human`** label (the same name as the train's `needs-human` status, see
  [train.md](train.md)), color `d93f0b`, "A bot PR waiting for a person";
- a **review request from the org team `admins`**, so GitHub notifies each member.

## Who adds them

| Bot | PRs | How |
| --- | --- | --- |
| Renovate (`renovate/default.json`) | Majors, and OPA in the api (its own `renovate/opa` PR, never auto-merged) | The rules add `"addLabels": ["needs-human"]` (merged with any other labels) and `"reviewers": ["team:admins"]` (Renovate's syntax for a GitHub team). Renovate requests reviewers when it opens the PR. |
| ccf-bump | A PR whose auto-merge is off: a major update or a pin that was not a version, or a PR on which GitHub refused auto-merge | After opening or updating the PR, ccf-bump adds the label (creating it in the repo when missing) and requests a review from `--review-team` (env `CCF_BUMP_REVIEW_TEAM`, default `admins`; `--review-team ""` for none). Failures are warnings, as for auto-merge: the PR is open either way. `--dry-run` prints what it would do. |

## Permissions

Both run as `ccf-release-bot`. Adding a label, creating it and requesting reviewers need
**Pull requests: write**, which their live tokens already have. A team review request also needs
the app's organization permission **Members: read** (the app has it), so `renovate.yml`,
`ccf-bump-sync.yml` and `train.yml` (which runs ccf-bump) add `permission-members: read` to their
live tokens.

GitHub only accepts a team as a reviewer when the team has access to the repo: give `admins` at
least read access to every manifest repo, or the request fails (a ccf-bump warning; Renovate logs
it and the PR keeps its label).
