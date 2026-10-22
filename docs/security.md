# Security and hardening (Phase 7)

**Tier: T2, internal/team.** A small known group signs in. The data is operational and synthetic. No real money moves and no regulated personal data is held. Move to T3 if the system ever holds real customer addresses or real COD amounts.

**Scaling: none.** One host, a small user base. No scaling work applies.

This page lists each control from the spec (section 17), whether it is in place, and how it was checked. "Checked" means a test or a command I ran. Where a control depends on a person or a provider, it says so.

## Controls in place

| Control | Where | How it was checked |
|---|---|---|
| Secrets stay out of git and out of the browser | `.gitignore`, `deploy/.env` read by Compose only | The first commit holds only `.env.example` files with placeholders. The web token and signing key are read in server code only. |
| Webhook secrets: shown once, encrypted at rest, rotated with overlap | `internal/delivery` | Phase 2 tests |
| API tokens hashed and scoped | `internal/auth` | Role matrix test, one case per scope |
| PostgreSQL has no published port | `deploy/docker-compose.prod.yml` | `docker port` on the db container lists nothing |
| Every route declares a role or a scope; a route with neither is closed | `api.Routes()`, `allowed()` | `TestRoleMatrix` calls every route as every role and as a wrong-scope token |
| Operators see only their own tasks, and can act only on tasks assigned to them | `listTasks`, `checkTaskOwner` | `TestOperatorSeesOnlyTheirOwnTasks` (asks for another operator's tasks and gets none); the offline sync load test rejects commands on another scanner's task |
| Unknown JSON fields are rejected (blocks mass assignment of `role` or `warehouse_id`) | `decode()` uses `DisallowUnknownFields` | `TestUnknownFieldsAreRefused` sends `warehouse_id` and `role` to the order endpoint and gets 400 |
| No string-built SQL | `internal/sqllint` | A test parses every Go file and fails on a query that is not a constant. Twelve places build a query from fixed fragments; each carries a `sqlok:` comment with the reason. A second test shows the rule rejects `Sprintf` and variable concatenation. |
| Rate limits per user and per API token | `internal/api/ratelimit.go` | Test: a burst gets 429 with `Retry-After`; a second user is not affected. Default 50 requests per second per user, 10 per external token (`CROSSDOCK_RATE_LIMIT_USER`, `CROSSDOCK_RATE_LIMIT_SYSTEM`). |
| Sign-in throttle | `web/src/lib/loginlimit.ts` | Unit tests. Live: five wrong passwords, then the correct password is refused for ten minutes. |
| Session cookie is httpOnly, secure, same-site; nothing session-related is in `localStorage` | `web/src/lib/session.ts` | Cookie flags seen on the live login response |
| A removed user or a changed role takes effect on the next request | `getSession()` checks the current user list each time | `resolveSession` unit tests: removed user, changed role, expired, tampered and missing cookies (`web/src/lib/session.test.ts`) |
| Offline queue cleared on sign-out; unsynced work asks first | `web/src/components/SignOutButton.tsx` | Sign-out clicked in a real browser through the proxy. `leaveWithQueue` unit tests: queue cleared, asks first when work is unsynced, stays signed in if declined (`web/src/lib/offline.test.ts`). |
| Offline commands re-checked on sync | `/commands` runs the same auth and ownership checks | Offline sync load test: a command for another scanner's task is rejected with a reason |
| Security headers: HSTS, nosniff, referrer policy, permissions policy (Caddy); CSP with a per-request script nonce and `frame-ancestors 'none'` (web app) | `deploy/Caddyfile`, `web/src/proxy.ts`, `web/src/lib/csp.ts` | `curl -I` shows a different nonce on every request and on every script tag. Unit tests on the policy. A real browser signed in as a supervisor and as an operator and visited the console and scanner pages: no CSP violation, pages hydrate. Styles still allow `unsafe-inline`. |
| HTTP redirects to HTTPS | Caddy default | `curl -I http://...` returned 308 |
| API answers are never cached | `Cache-Control: no-store` on every API response | Test |
| Errors show a generic message and a trace ID; detail goes to the logs | `problem()` | Test checks the ID is present and the detail holds no SQL |
| Logs hide secrets; scanned codes are fields, and a newline in one cannot forge a log line | `telemetry.RedactAttr` | Test |
| Remittance upload: size cap (8 MB), strict CSV parse that names the bad line, whole file refused on any bad row | Phase 4 | Phase 4 tests |
| Responses are built per endpoint, not copied from database rows | handlers use typed structs | Review |
| Traces and the Grafana board are not open | Jaeger behind basic auth; Grafana anonymous access off by default | Live: `/jaeger` returns 401 without a password and 200 with it; `/grafana/api/search` returns 401 |
| Dependency scans in CI | `.github/workflows/ci.yml`, job `dependency-scan` | Run by hand: `govulncheck` found six Go standard library issues, all fixed by moving to Go 1.26.6 (now clean). `npm audit` and `pip-audit` found nothing. The CI job itself has not run. |
| Lockfiles committed | `go.sum`, `web/package-lock.json`; `cmd/slotting/requirements.txt` | Present. The Python file has minimum versions, not exact pins. |
| Source maps and `.git` not served | Next.js production build; Caddy only proxies to the app | Not tested by request |

## Fixed during the audit

- Sign-in and sign-out redirected to the container's internal address behind the proxy. Redirects are now relative.
- API responses had no cache header, no trace ID on errors, and the logger did not hide secrets.
- The offline queue was never cleared and the scanner had no sign-out button.
- Removing a user from `CROSSDOCK_WEB_USERS` left their session valid for up to 12 hours.
- Jaeger and Grafana were open to anyone.
- The Go toolchain had six known standard library vulnerabilities.

## Not done, and why

| Item | Status |
|---|---|
| **Managed authentication provider** | Not done. Sign-in is a stand-in: users and passwords come from `CROSSDOCK_WEB_USERS`. Passwords are compared in constant time but are plain text in the environment, and there is no password change flow. This is the largest gap against the spec. Replace the lookup in `web/src/app/api/login/route.ts` and keep `startSession`. |
| **Two-factor on accounts** | Yours to do. Turn it on for the host provider, domain registrar, object storage account, the auth provider once it exists, and every admin user. |
| **Billing caps and usage alerts** | Yours to do on the host, object storage and the auth provider. |
| **Secret rotation** | Procedure is in `deploy/README.md`. Nothing reminds you to do it on a schedule. |
| **`unsafe-inline` for styles** | Scripts need the per-request nonce. Styles still allow `unsafe-inline` because React writes `style=` attributes, which a nonce cannot cover. An injected style cannot run code, so the risk is low. |
| **Rate limit across several API processes** | Limits are per process. Fine for one container. |
| **Remittance files in a private bucket** | Files are parsed on upload and stored as rows in PostgreSQL, so they sit in the database and its backups. There is no object storage. |
| **Manual test of admin screens** | Not done. The automated role matrix covers the API. I did not click through every admin and replay screen as each role. |
| **Service worker on the real host** | Registers and activates under the new CSP on `localhost` (checked in a real browser). Not checked over the real certificate and domain. |
| **Backup upload to object storage** | The `aws s3 cp` step is untested. |

## Before going live

1. Replace the stand-in sign-in with a managed provider, or accept it knowingly for a closed demo.
2. Set strong values in `deploy/.env`, including `JAEGER_PASSWORD_HASH`. Keep `GRAFANA_ANONYMOUS=false` unless the data is synthetic and you want it public.
3. Turn on two-factor everywhere in the table above and set billing alerts.
4. Run the restore drill on the host and record it in `docs/restore-drills.md`.
5. Check the scanner works offline and installs as an app over the real certificate.
