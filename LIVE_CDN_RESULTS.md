# Live CDN validation — 2026-09-27

Authorized Development workspace, using only a uniquely named disposable bucket and distribution. The portal confirmed promotional credit available and consumed first. No payment, funding, customer content, customer distribution or custom DNS record was changed.

| Operation | Result | Evidence and limits |
| --- | --- | --- |
| Empty private bucket create | Passed | Terraform create returned active; usage counters zero. |
| Private bucket CDN origin | Expected rejection | HTTP 400 explained that CDN origins must be public. No distribution was created. |
| Test bucket public-read update | Passed | Applied only to the empty test bucket, then refreshed. |
| CDN distribution create | Passed | Bucket-name alias resolved to canonical origin ID; distribution reached active. |
| Refresh/no-change | Passed | Terraform detailed exit code 0. |
| Explicit URL purge action | Passed | Real Terraform action invoked for a nonexistent test path; service confirmed success. This validates request/response, not eviction of a previously cached object. |
| Distribution import | Passed with documented canonical configuration | Import stores canonical origin ID. Keeping a bucket-name alias in imported configuration proposed replacement; this plan was not applied. Canonical-ID configuration produced no changes. |
| Name/cache policy/disable update | Passed | One in-place apply changed name, static-assets to short cache and enabled=false; no-change plan followed. Live portal showed matching name, bucket, Short Cache and disabled. |
| Re-enable delivery | Passed | Terraform PATCH and active readback succeeded. |
| Tiny HTML upload through public API | Passed | Deployed route requires multipart file input; raw-body request returned 422 without mutation, multipart PUT returned 201. No credentials or customer data in HTML. |
| SPA website create | Passed | Terraform enabled website configuration after index.html existed. |
| Actual SPA fallback delivery | Passed | HTTPS request to a nonexistent page route returned HTTP 200 and the exact test marker from the uploaded HTML. |
| Website import | Passed | Import using distribution ID succeeded. |
| Website index update | Passed | Updated to alternate.html and no-change plan passed. A second new page route returned HTTP 200 with the test marker; both files had identical content, so this confirms continued SPA delivery but does not independently prove which index file the data plane selected. |
| Portal comparison | Passed | Distribution detail showed active, Short Cache, correct bucket and Enable SPA checked. The UI description hardcodes index.html; canonical API readback confirmed the alternate index. |
| Website destroy | Passed | Terraform disabled website routing successfully. |
| Test-object cleanup | Passed | Both test HTML objects deleted through public API, HTTP 200; bucket refresh reported zero objects and bytes. |
| Distribution and bucket destroy | Passed | Terraform destroyed both; independent GETs confirmed HTTP 404. |

No custom domain was associated and no DNS/TLS verification was invoked: those require a disposable domain under confirmed control. Custom-origin public API deployment remains a separate gap. Notification delivery, cache TTL, every purge mode, regional delivery and load testing were not inferred from the successful SPA test.

All temporary resource identities, local state and raw logs remain outside the repository. The origin bucket is deleted and the distribution is soft-deleted by the service; no live test resource remains. This is not a final billing-settlement audit.
