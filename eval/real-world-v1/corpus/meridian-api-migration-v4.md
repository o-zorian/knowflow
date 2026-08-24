# Meridian API Migration Guide v4.0

**Status:** Current  
**Effective:** 2026-07-01  
**v3 sunset:** 2026-11-30

## 1. Version authority

Version 4.0 is the current migration contract. Meridian v3 remains readable only during the transition window and is retired at 23:59 UTC on 2026-11-30. An archived v3 example later in this guide uses camelCase fields; it is intentionally retained as a migration contrast and must not be copied into new v4 clients.

## 2. Authentication

V4 uses OAuth 2.0 client credentials. Clients request a token from `/oauth2/token` with scope `meridian.write`; static `X-API-Key` authentication belongs to v3 and is rejected by v4 write endpoints. Tokens should be refreshed when fewer than 120 seconds remain.

### 2.1 Idempotency

Every create request must send `Idempotency-Key`. Meridian keeps the key and response binding for 24 hours. Reusing the same key with a different body returns HTTP 409; retrying the same body returns the original operation result.

## 3. Field mapping

| v3 field | v4 field | Rule |
|---|---|---|
| `customerId` | `customer_id` | required UUID |
| `completedAt` | `completed_at` | UTC RFC3339 timestamp |
| `orderItems` | `order_items` | array, max 200 |

The v4 completion timestamp is `completed_at` and must be UTC RFC3339, for example `2026-07-18T09:30:00Z`. A local time without an offset is invalid.

## 4. Rate limits and retry

The default tenant limit is 120 requests per minute with a burst of 30. Retry only HTTP 429 and 503, using delays of 200 ms, 400 ms, and 800 ms. Do not automatically retry HTTP 400 because it represents a caller correction, not transient capacity.

## 5. Connector dependency

Project Lumen release 3.4 requires Meridian v4 before rollout wave 2 begins. Wave 1 may observe v3 traffic, but wave 2 validation fails if the connector still sends `customerId` or `X-API-Key`.

## Appendix A - archived v3 contrast

V3 used `X-API-Key`, retained idempotency keys for 6 hours, and exposed `customerId`. These values are historical distractors, not valid v4 behavior.
