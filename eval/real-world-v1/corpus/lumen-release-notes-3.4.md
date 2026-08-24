# Project Lumen Release Notes 3.4

Release 3.4 is the current production train for August 2026. It replaces 3.3 and requires database schema version 27 or later.

## Feature activation

Hybrid search is controlled by the exact feature flag `lumen.search.hybrid`. The similarly named `lumen.search.hybrid_preview` flag belonged to a closed beta and has no effect in 3.4.

## Rollout waves

| Wave | Date | Audience | Entry requirement |
|---|---|---|---|
| 1 | 2026-08-05 | internal tenants | schema 27 |
| 2 | 2026-08-12 | 10% external tenants | Meridian v4 connector |
| 3 | 2026-08-19 | 50% external tenants | error budget green |
| 4 | 2026-08-26 | all eligible tenants | change approval |

Wave 2 begins on 2026-08-12 and requires the Meridian v4 connector. This dependency is defined jointly with the Meridian migration guide, which retires v3 later in the year.

## Rollback threshold

Rollback the active wave when the five-minute error rate remains above 2.5% for 10 consecutive minutes. A single five-minute spike does not trigger rollback. The on-call lead records the start and end of the sustained window before disabling the flag.

## Cache behavior

Release 3.4 changes query-result cache TTL from 15 minutes to 5 minutes. The 15-minute value is the 3.3 default and is retained here only to explain the change. Authentication-token caching remains 2 minutes and is unrelated.

## Known non-blocking issue

Exported CSV filenames can omit the tenant display name when it contains emoji. The export data is complete, so this issue does not block rollout and does not change retrieval scoring.
