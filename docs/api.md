# API

The REST API is described by `api/openapi.yaml`. The [OpenAPI reference](openapi.md) page in this book is that file. A test fails if the file and the router disagree on endpoints, and the TypeScript types in the web app are generated from it.

## Authentication

Send `Authorization: Bearer <token>`.

- **External systems** use a `cdk_` API token. Tokens are hashed at rest and limited to scopes. An admin creates one with `POST /v1/tokens`. The plaintext is shown once.
- **The web app** sends its service token with a signed user identity (`X-Crossdock-Identity` and `X-Crossdock-Signature`). Roles are `operator`, `supervisor` and `admin`. Every route declares a role or a scope. A route with neither is closed.

| Scope | Allows |
|---|---|
| `asn:write` | Create an ASN |
| `orders:write` | Create and cancel orders |
| `outcomes:write` | Report a delivery outcome |
| `remittances:write` | Upload a courier remittance file |

`events:read` is accepted when you create a token, but no route uses it yet.

## Idempotency

Every mutating call accepts an `Idempotency-Key` header. `POST /receipts` requires it. Creating an ASN or an order is idempotent on its `ref`. A delivery outcome and a remittance file are idempotent too: the same file twice does nothing.

The scanner syncs offline work through `POST /v1/warehouses/{wid}/commands`. Each command carries its own key. The reply has one result per command, in the order you sent them. A result is `applied`, `rejected` (with a reason and a code) or `duplicate` (the key was seen before, and the stored result is returned). A rejected command does not stop the ones after it.

## Pagination

List endpoints take `cursor` and `limit` (1 to 200, default 50). The response has `next_cursor`. When it is empty, you have the last page.

## Errors

Errors are RFC 9457 problem details (`application/problem+json`) with `type`, `title`, `status` and a `trace_id`. A server error shows a generic message and the `trace_id`. Quote the `trace_id` to find the full detail in the logs.

## Rate limits

The API limits requests per user and per token. A limited call gets `429` with a `Retry-After` header. The defaults are 50 requests a second per user and 10 per external token. They are set with `CROSSDOCK_RATE_LIMIT_USER` and `CROSSDOCK_RATE_LIMIT_SYSTEM`.

## Caching

API responses carry `Cache-Control: no-store`. Do not cache them.
