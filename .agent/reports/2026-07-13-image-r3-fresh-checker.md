# IMAGE R3 fresh checker report

Verdict: **ACCEPT**

The immutable R3 patch closes the R2 false-red publication defect without
weakening the release authority boundary. Ordinary successful pull-request,
`main`, and manual `Docker` runs now skip publication cleanly. Only a successful
push whose `workflow_run.head_branch` is tag-shaped enters the unprivileged
preparation job, and the unchanged canonical preflight still requires exact
workflow identity, canonical SemVer, same-repository provenance, tag peel to the
triggering SHA, protected-main ancestry, and exact active rulesets before the
privileged publisher can run.

## Immutable review boundary

- Base: `65837cc735e469e7afc973347e943d3af6ec8ebd`
- Candidate HEAD: `a6a88b8836c580324b864ae87b55b94e1cc012cf`
- Candidate tree: `28a93c5634316a24446a8deb3ab4e2c9daacc948`
- Candidate worktree: clean before and after review.
- Changed paths: exactly 2 — `.github/workflows/docker-publish.yml` and
  `tests/critical/runtime/image_runtime_contract_test.go`.
- Path digest, canonical input `sort(git diff --name-status)`, LF, trailing LF,
  UTF-8: `aa44a33f32ec31f52d181cf75d587d0d4c807466e9451b241be315fd71fdeade`.
- Status digest, canonical input `base\nhead\ntree\nclean\npath_digest\n`, UTF-8:
  `1cf6a669c7a822119e27bc7c6448cb587c19b7e250f50a89b07e3fb886e959f8`.
- Patch digest, `git diff --binary`, normalized LF with trailing LF, UTF-8:
  `313f0fa382a0b270ecfc26fa2c5179e35ab07cc96023a63a76d853ed4c22ff0d`.
- Evidence manifest SHA-256:
  `6b6266eacbbf8430a3194669d1752262d4cd808201003b6a8ab417d75a5a66a5`.

## Product and security judgment

No HIGH or CRITICAL finding remains in the R3 scope.

The selector is deliberately a cheap routing filter, not release authority:

- `conclusion == success` rejects failure and cancellation;
- `event == push` rejects pull requests and manual dispatch;
- `startsWith(head_branch, 'v')` skips normal branch runs before checkout;
- a hostile or canonical-looking `v*` branch/ref still cannot publish unless
  the unchanged preflight proves an actual canonical protected tag with the
  same SHA and protected-main ancestry;
- fork repository identity, workflow ID/path/name, event/API parity, tag peel,
  ruleset, artifact ID/digest/run, envelope, traversal, and link mutations all
  fail closed in the full hostile matrix.

The changed workflow line is evaluated by GitHub's expression engine and is
not interpolated into a shell. Candidate source remains checked out without
workflow credentials. Preparation remains `contents: read`; package write
authority exists only in the dependent publisher after both preflight passes.
The commit-keyed `cancel-in-progress: false` concurrency policy is unchanged.

## Current external-source and live evidence

Current official GitHub documentation was retrieved through Parallel, not
recalled from training memory:

- `workflow_run` fires regardless of the upstream conclusion; job conditions
  must inspect `github.event.workflow_run.conclusion`;
- its event payload exposes the triggering workflow run;
- `workflow_run` is privileged and must not check out untrusted code or trust
  artifacts without validation.

Sources:

- <https://docs.github.com/en/actions/using-workflows/events-that-trigger-workflows#workflow_run>
- <https://docs.github.com/en/webhooks/webhook-events-and-payloads#workflow_run>
- <https://docs.github.com/en/actions/reference/security/secure-use>

Parallel evidence IDs: `search_6ef6d6389684453fb99c8d96cff7bfcc` and
`extract_7020ec0cfc394ed5886dc0e5986387e7`. Tavily was also invoked as required,
but returned `monthly_cap_reached_bonus_eligible`; no claim relies on Tavily.

Read-only live `gh api` evidence matched the selector's data model:

- release-tag push: run `28757833445`, `event=push`,
  `head_branch=v6.42.0`, `conclusion=success`;
- same release commit on main: run `28757820662`, `event=push`,
  `head_branch=main`, `conclusion=success`;
- pull-request shape: run `28755117108`, `event=pull_request`,
  non-tag branch, `conclusion=success`.

No GitHub ruleset, registry, tag, release, package, candidate, or main mutation
was performed.

## Executed gates

- Full `TestDockerReleaseRefFreshnessGuard` hostile matrix: PASS in 92.964s.
  This includes single-writer, canonical/hostile refs, workflow provenance,
  selector, immutable artifact bridge, tag rulesets, registry compare-before-
  write, and old-guard movement cases.
- Focused selector matrix plus provenance matrix: PASS.
- Selector matrix repeated with `-count=20`: PASS.
- `actionlint` on `docker-publish.yml`: PASS, `[]`.
- `go build ./...`: PASS.
- `go vet ./...`: PASS.
- `git diff --check <base>..<head>`: PASS.
- `gitleaks` on the one candidate commit: PASS, no leaks.

## Runtime image and UI replay reuse

R3 changes no Dockerfile, Compose file, production-gate script, application,
image, dependency, or UI path. Rebuilding and replaying the exact same image
set would add no evidence. The accepted R2 runtime/UI proof is therefore reused
after this path-level proof:

- R2 checker commit: `c8ac8b216f052745d7fdce9797937e8625f660d4`
- R2 checker report SHA-256:
  `53cd7095fab2fa441b4922d33d9932eabd3a2004b58429b7b76086d8c5499909`
- R2 final image-set SHA-256:
  `2456e01550bb937e777553d35f4d2b7e80597be5323df3de5de39c85c331a652`

That proof covers three exact scanned image IDs, hostile runtime checks,
non-root/read-only execution, persistence/backup/restore, cleanup, and Chrome
DevTools desktop/mobile/restart replay with only HTTP 200 responses. Its three
separate baseline findings remain outside this two-path R3 fix and are not
silently reclassified.

## Reusability candidates

- none — evaluated; this is a one-line release selector and a local contract
  regression, not a reusable component.

REVIEW_COMPLETE: APPROVE
