# Bucket configuration coverage

The source contract is `object-storage/app/api/native/v1/buckets.py` and `app/schemas/native.py`. The public OpenAPI does not yet list these suffixes. A read-only live check confirmed HTTP 200 for all three `/object-storage/buckets/{name}/{cors,lifecycle,notifications}` endpoints in the development environment. Provider implementation follows the native GET/PUT/DELETE schemas; deployment parity elsewhere must be checked.

| Feature | Resource and fields | Verification |
| --- | --- | --- |
| CORS rules | `ibee_bucket_cors`: rule ID, allowed origins/methods/headers, exposed headers, max age | Real Terraform CLI create/read/import/no-op/update/drift/destroy against local API fixtures. Tests assert PascalCase wire aliases, two ordered rules, every accepted HTTP method, wildcard origin, optional field omission, and invalid origin/method/age rejection. |
| Object expiration | `ibee_bucket_lifecycle`: Enabled/Disabled status, prefix, expiration days OR UTC timestamp | CLI fixture exercises age and date rules together, changes age, imports and reaches no-op despite API timestamp normalization. Focused tests reject missing/conflicting/invalid expiration. Enabled rules are accepted by schema but no destructive live expiration was performed. |
| Event notifications | `ibee_bucket_notifications`: optional ID, upload/delete categories, optional prefix/suffix filter, optional webhook, computed expanded S3 events | CLI fixture exercises both categories, two configs including one with optional fields absent, updates categories/removes webhook, imports and reaches no-op. Tests ensure computed S3 fields are never submitted. No actual event delivery was attempted. |
| Existing configuration ownership | One complete section per resource; import bucket name | Focused tests refuse create when an existing section is nonempty, reject malformed preflight, and make no mutation. This GET/PUT check is not atomic; use one configuration writer per section. |
| Failure recovery | Strict non-null array and required nested fields; preserve identity after successful write with failed refresh | Focused tests cover null/missing/malformed readback, preserved state, and recoverable ID after malformed refresh. |
| Deletion | DELETE followed by GET that must be empty or 404 | Focused tests reject a falsely successful DELETE that still returns entries; CLI fixture verifies all sections empty. This verifies metadata readback, not downstream cleanup. |

Service limitations must not be confused with successful Terraform configuration:

- CORS route documentation says MinIO cannot apply CORS. `BucketService.set_cors` stores metadata and attempts public URL/CDN synchronization; synchronization errors are logged. No browser/data-plane enforcement claim is made.
- Lifecycle writes expiration to MinIO and stores metadata; GET reads metadata. Actual object expiry is asynchronous and was not tested. Delete catches downstream S3 errors before clearing metadata, so metadata readback cannot prove backend removal.
- Notifications expand upload/delete events and configure the region's notification target. GET reads stored metadata. The service catches some downstream errors on delete; webhook/Kafka delivery and downstream clearing require separate tests.
- All resources replace the complete section on update and clear it on destroy. They do not manage individual rules independently. Import before adopting existing nonempty configuration.
- Lifecycle timestamp input uses canonical UTC RFC3339 ending in `Z` to make creation and import stable when the service emits `+00:00`.

Tests: `TestBucketConfiguration*` in `internal/provider/bucket_configuration_resource_test.go` and `TestTerraformLifecycleBucketConfiguration` in `internal/provider/bucket_configuration_terraform_test.go`. Local focused and real-CLI tests passed on 2026-09-27. No live mutation or object deletion was performed by these tests.
