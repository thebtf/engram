# Access Administration — structure spec

Two surfaces, one role/permission model. **Self-hosted is NOT a billing-less SaaS
workspace in the UI** — billing/plans/tenancy concepts never appear on the self-hosted
surface. They exist only in the SaaS super-admin.

## Surface A — Self-hosted administration (BUILDING NOW)
Lives in **Settings → group "Доступ"**. Mockup: `access-admin.html`.

Tabs:
1. **Провайдеры входа** — OAuth config. Direct social providers (Google/GitHub/Yandex/VK,
   each independently enable + client_id/secret write-only + redirect_uri) + one generic
   OIDC broker (Authentik/Keycloak/Zitadel: issuer_url, client_id/secret, claim→role map,
   default role). Hybrid = superset; an install narrows by disabling what it doesn't use.
2. **Политика доступа** — master gate `blockSelfReg` (ON by default — forbids self-service
   registration/login entirely; only admin invite/create makes accounts). When OFF, the
   sub-policy applies: `invite-only` / `domain-allowlist` / `open+approval`. Default role
   for new users. *(blockSelfReg added 2026-06-19.)*
3. **Пользователи** — email, linked providers, role, status (active/invited/suspended),
   last login. Owner protected. Two creation paths: **Пригласить** (link) AND **Создать
   пользователя** manually (admin sets name/email/role + invite-link-or-password; bypasses
   self-reg policy, account active immediately). *(manual create added 2026-06-19.)*
4. **Приглашения** — pending invites: email, role, issued-by, expiry, link.
5. **Роли** — fixed ladder Owner → Admin → Operator → Viewer.
6. **Сессии** — admin-wide active sessions, revoke.
7. **Журнал доступа** — login, role change, invite, OAuth-config change, policy change.

**NOT on self-hosted:** plans, tariffs, billing, quotas, feature-gating, multi-tenancy,
custom granular roles. (Removed from mockup per operator, 2026-06-19.)

## Surface B — SaaS super-admin (STRUCTURE FIXED, BUILD AFTER A)
Separate console, platform-operator audience. NOT inside the engram operator console.

Role hierarchy: `Superadmin (platform)` → `Staff/Support` → **[Workspace]** →
`Owner` → `Admin` → `Billing-admin` → `Member` → `Viewer`.

Entities:
- **Workspaces / Organizations** — tenant: name, owner, plan, status, member count, usage.
- **Platform users** — global directory; one person ∈ N workspaces with different roles.
- **Plans / tariffs** — name, price (mo/yr), seat model (per-seat/flat), trial, status
  (public/legacy/custom).
- **Feature catalog** — id, name, category, type: boolean | quota | limit.
- **Plan × Feature matrix** — "feature available in plan X" template; cell = ✓ / quota N /
  — ; plus per-workspace override (enterprise/grandfathering).
- **Subscriptions** — workspace→plan: status (trial/active/past_due/canceled), seats, period.
- **Usage / quotas** — workspace→feature→consumed vs limit.
- **Billing** — invoices, payment methods, usage metering.
- **Invitations** (workspace level).
- **Audit log** (platform + workspace).
- **Impersonation / Support** — superadmin "enter as workspace", strictly audited.
- **Service-wide Модели + Доступы LLM** *(operator add, 2026-06-19)* — the LLM
  models + credentials/endpoints that today live in the operator console's Settings are,
  in SaaS, configured ONCE at the platform level in super-admin (service-wide), not
  per-workspace. All endpoints used service-wide are wired here. Per-workspace model
  selection (if any) sits on top of this platform pool.

**Feature-gate** = one reusable component: input featureId + current workspace plan →
renders `available` / `upgrade to plan X` / `quota exhausted`. Covers both the matrix and
in-product gating.

## Build order for Surface A
1. Settings group "Доступ" + tab "Провайдеры входа" (closes "OAuth настраивается").
2. Пользователи + Приглашения + Роли.
3. Сессии + Журнал.
