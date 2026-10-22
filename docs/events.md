# Events and webhooks

External systems subscribe to events by registering an endpoint. An admin does this in the console under **Webhooks**: give a URL and an event filter. The signing secret is shown once.

## Event catalogue

| Event | Emitted when | Ordering key |
|---|---|---|
| `receipt.completed` | all ASN lines for a receipt are scanned or marked short | ASN id |
| `stock.moved` | any movement, with kind, from, to and quantity | movement id |
| `order.allocated`, `order.partially_allocated`, `order.unallocatable` | the allocation sweep decides an order | order id |
| `order.cancelled` | an order is cancelled and its reservations are released | order id |
| `wave.released` | a wave goes to the floor | wave id |
| `shipment.dispatched` | the carrier handoff is scanned | shipment id |
| `shipment.delivery_outcome` | the courier reports delivered, rejected or lost | shipment id |
| `count.variance_classified` | a supervisor confirms a variance class | variance id |
| `return.authorised`, `return.received` | the returns flow | shipment id |
| `return.dispositioned` | a returned unit gets a disposition | return line id |
| `settlement.break_opened`, `settlement.break_resolved` | remittance matching | shipment id (break id if the break has no shipment) |

Your filter accepts a topic (`shipment.dispatched`), a prefix (`shipment.*`) or everything (`*`).

## The request

Each delivery is a `POST` with a JSON body:

```json
{
  "id": "evt_42",
  "type": "shipment.dispatched",
  "occurred_at": "2026-01-01T12:00:00Z",
  "sequence": 42,
  "warehouse_id": "wh-demo",
  "idempotency_key": "evt_42",
  "data": {
    "shipment_id": "shp-1",
    "order_id": "so-1",
    "carrier": "JNT",
    "tracking": "JNT0001",
    "cod_amount_minor": 99900
  }
}
```

`data` depends on the event. `idempotency_key` equals `id`. Use it to ignore a delivery you already handled.

| Header | Meaning |
|---|---|
| `X-Crossdock-Timestamp` | Unix seconds when the delivery was signed |
| `X-Crossdock-Signature` | `v1=<hex>`, HMAC-SHA256. During a secret rotation it holds two values, separated by a comma |
| `X-Crossdock-Event-Id` | Same as `id` |
| `X-Crossdock-Replay-Of` | Present only on a replay |
| `traceparent` | W3C trace context of the request that caused the event |

## Verify the signature

The signature is the HMAC-SHA256, with your endpoint secret as the key, of the timestamp, a dot, and the raw body:

```
v1=hex( HMAC_SHA256(secret, timestamp + "." + body) )
```

Follow these rules:

1. Use the **raw** body bytes. Do not parse and re-encode the JSON first.
2. Reject a timestamp more than five minutes from your clock. This stops replay.
3. Compare in constant time.
4. During a rotation, try every secret you hold and every value in the header.

### Node.js

```js
{{#include snippets/verify.mjs:verify}}
```

### Python

```python
{{#include snippets/verify.py:verify}}
```

### Go

```go
{{#include snippets/verify.go:verify}}
```

### Test vector

Check your code against this before you go live.

```json
{{#include snippets/vector.json}}
```

The three snippets above each pass this vector in CI, together with a tampered body, a stale timestamp and a rotation case. A Go test also checks that the server signs the vector the same way.

## Delivery rules

- **At least once.** Delivery retries until your endpoint returns `2xx` or the retry budget runs out. Expect duplicates and dedupe on `idempotency_key`.
- **Ordered per key.** Events with the same ordering key arrive in sequence order. A `shipment.delivery_outcome` never arrives before its `shipment.dispatched`, because both use the shipment id. Events with different keys can arrive in any order.
- **Backoff.** Exponential with full jitter, from one second to one hour, for up to twenty attempts.
- **Circuit breaker.** After ten failures in a row your endpoint pauses for five minutes. Then one probe event decides. A failing endpoint does not slow other endpoints.
- **Dead letter.** After the budget, the event goes to the dead letter list with the response of every attempt. A supervisor can replay one event, every dead letter for an endpoint, or a time range. A replay is an ordinary delivery with `X-Crossdock-Replay-Of`.
- **Rotation.** Rotating a secret issues a second one. Both are valid for an overlap window.
- **Targets.** In production an endpoint must use `https` and must not point at a private or link-local address.

`node web/scripts/webhook-sink.mjs` is a ready receiver that checks the signature.
