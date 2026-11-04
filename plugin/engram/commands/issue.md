---
description: Triage engram issues — check assigned, verify your fixes, file new ones for tracked projects
---

# /engram:issue — Issue triage workflow

Systematic workflow for working with engram's cross-project issue tracker.
Run this when you want to check the full state of issues involving your
current project, or when you've found a new problem that should be filed.

## Decision rules (read first)

**Engram issues are for ENGRAM-TRACKED projects only.** If you find a problem
in a project that is NOT tracked by engram, use its native issue tracker
(GitHub, Linear, Jira, etc.) — do NOT file engram issues for external projects.

To check if a project is engram-tracked:
```
curl -s -H "Authorization: Bearer $ENGRAM_AUTH_ADMIN_TOKEN" \
  http://$ENGRAM_HOST/api/issues/tracked-projects
```
Returns `{"projects": [...], "count": N}`. A project is tracked if it has
observations OR existing issues in engram — meaning an agent working on it
will actually see injected issues at session start.

**Participation and verification:**
- When an issue's `target_project` matches your project, handle its implementation, evidence, and follow-up as your project's inbox.
- When an issue's `source_project` matches your project, follow up on the reported problem and verify the proposed solution.
- Authorized participants, including creators and other read-write participants, may mark resolved, close after appropriate verification, or reopen when a resolution fails. These actions are not creator/source-only privileges.
- Preserve permission checks, readonly/unauthenticated denials, project routing, and explicit higher-priority instructions. Participation does not grant authority to edit unrelated issue fields or reject an issue.
- Only resolve an issue when the fix is actually deployed — not when you think it will work. `resolved` explicitly awaits verification; it does not mean checked completion.
- Close only after actually checking the relevant behavior with evidence appropriate to the issue. A status label alone does not prove historical verification. If verification is unavailable, comment with what remains unverified and what is needed; do not close.

## Workflow

Use the verified native caller project for mutation attribution; it is not established by a `project` argument. That argument remains optional compatibility input for writes, and a target filter for list. When recovery is required, retain the original operation and actual native caller identity. Use the recovery response's supported actions to perform safe, authorized, unambiguous recovery automatically, then resume the original operation. If selection is ambiguous and an existing-project choice is available, present its supported options and resume after selection; never guess a project or assume an unavailable chooser exists. If the required recovery action or choice is unavailable, retain the pending write and report the exact missing capability rather than replacing recovery with mandatory manual shell or database edits. Get/by-ID and explicitly scoped list remain usable while the write is held. Preserve marker/cache/data, authorization and identity safeguards. Never invent a dummy project, initialize a replacement UUID, or ask the user to edit the database. Guide: https://github.com/thebtf/engram/blob/main/docs/operating-engram.md#context-recovery .

### Step 1 — Live issues targeting your project (you as TARGET)

Fetch active work assigned to you:

```
issues(action="list", project="<your-project>", status="open,acknowledged,reopened", limit=20)
```

For each returned issue:
1. Read the title, body, and latest comments with `issues(action="get", id=N)`.
2. Treat it as YOUR project's inbox and a direct work order: study the situation, verify the claim, decide whether to implement, clarify, or comment with blocker/progress.
3. `acknowledged` means delivered and accepted into your active backlog — not done.
4. When the fix is deployed and you are authorized: `issues(action="update", project="<your-project>", id=N, status="resolved", comment="Fixed in <commit/version>: <explanation>; awaiting verification: <remaining checks>")`.
5. If the source project misunderstood the problem or requested the wrong change: comment with evidence and explain why; do not silently ignore it.
6. As an authorized participant, use Step 3 to close after actual appropriate verification or reopen when the resolution fails. If you cannot verify, comment with what remains unverified.

Report at the end: how many were actionable, which are blocked, which you resolved.

### Step 2 — Issues YOU previously resolved (check for reopens and feedback)

Fetch issues you resolved that might have been reopened or commented on:

```
issues(action="list", project="<your-project>", status="reopened", limit=20)
```

Then separately for resolved issues your project created, check the comments:

```
issues(action="list", source_project="<your-project>", status="resolved", limit=20)
```

For each reopened issue:
1. `issues(action="get", id=N)` — read the reopen reason carefully.
2. Understand WHY the fix failed. Do not assume it's still fixed.
3. If the reason is valid → plan another fix. If the reopener misunderstood → comment politely explaining.
4. Never re-resolve silently.

### Step 3 — Verify resolved issues as an authorized participant

Start with issues your project filed against OTHER projects that are now resolved. This follow-up responsibility does not reserve verification or closure to the creator:

```
issues(action="list", source_project="<your-project>", status="resolved", limit=20)
```

For each resolved issue you created or otherwise participate in (including those found in Steps 1–2):
1. `issues(action="get", id=N)` — read the resolution comment and available evidence. `resolved` means awaiting verification, not proof that verification already happened.
2. Treat this as follow-up dialogue: understand the claimed fix, inspect comments/reports, and identify the relevant behavior and delivery target.
3. **Actually verify the fix works.** Check the relevant behavior on that target with evidence appropriate to the issue; a commit or version label alone is not behavioral proof.
4. If you are authorized and the relevant checks demonstrate completion → `issues(action="close", project="<your-project>", id=N)`. Comment with the checks and results. This is terminal — the issue disappears from all injections.
5. If you are authorized and the resolution fails, is incomplete, or addresses the wrong problem → `issues(action="reopen", project="<your-project>", id=N, comment="Tested X, still broken because Y")` with concrete evidence.
6. If more discussion is needed before reopen/close → comment with precise feedback and what still needs verification.
7. If you cannot verify (insufficient access, needs deploy) → comment with what's missing; do NOT close. No status label alone proves historical verification.

### Step 4 — New problems discovered during this session

If during this session you encountered a bug, missing feature, or friction
in a project OTHER than your current one:

1. **Check if the other project is engram-tracked.** Fetch `GET /api/issues/tracked-projects` and look for it.
   - **Tracked:** proceed to step 4.2 (file engram issue).
   - **Not tracked:** file a GitHub/Linear issue using that project's normal workflow. Do NOT file in engram — the target agents won't see it.

2. **Check for duplicates BEFORE creating** — another agent may have already filed this:
   ```
   issues(action="list", project="<target-project>", status="open,acknowledged,reopened,resolved", limit=50)
   ```
   Read the titles. If a similar issue already exists, add a comment to that issue instead of creating a duplicate.

3. **Create a clear issue:**
   ```
   issues(action="create",
          project="<your-project>",
          target_project="<other-project>",
          title="<short imperative summary>",
          body="<evidence: what you did, what you expected, what happened, logs/commits/files>",
          priority="<critical|high|medium|low>",
          labels=["bug", ...])
   ```

   Body should include:
   - Reproduction steps (or the exact action that triggered it)
   - Expected vs actual behavior
   - Relevant file paths, commits, error messages
   - Any workaround you applied on your side

### Step 5 — Report

At the end of the workflow, give the user a concise summary:

```
Engram issues triage for <your-project>:
- Active (target): N issues, M resolved this session
- Reopened (target): K issues needing re-fix
- Resolved by others (source): P issues verified and closed, Q reopened
- New issues filed: R (for tracked projects)
- External problems found: S (filed in GitHub/etc., listed below)
```

## Anti-patterns (DO NOT)

- Do NOT file engram issues for untracked projects — use their native tracker.
- Do NOT close without authorization and actual appropriate verification, regardless of who created the issue.
- Do NOT create duplicates — check existing issues first with `list`.
- Do NOT resolve an issue while the fix is only half-done — comment with progress instead.
- Do NOT decide unilaterally "my part is done, splitting into new issues for X" — comment your analysis and ask the operator.
- Do NOT silently accept a reopen — understand the reason and either fix properly or explain why you think the original resolution was correct.
