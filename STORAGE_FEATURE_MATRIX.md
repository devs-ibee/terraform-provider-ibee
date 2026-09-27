# Object storage and secret manager feature matrix

Source audit date: 2026-09-27. This inventory compares the portal UI, portal API, native services, public gateway manifests, and this provider. A feature marked implemented has local contract coverage; that does not prove it is deployed or that the data plane enforces it. See [VALIDATION.md](VALIDATION.md) for separately recorded live checks.

The provider manages persistent infrastructure configuration. File transfers, temporary access URLs, secret recovery, and permanent history erasure have different semantics and are identified below instead of being silently run during refresh or destroy.

## Evidence and routing boundaries

The source checkout names below identify the material reviewed. Paths are relative to each named repository or snapshot.

| Evidence | Relevant source |
| --- | --- |
| Portal UI snapshot `portal-client-ui-workspace-activity-0926` | `app/components/client/CloudStorage/index.tsx`; `app/routes/client/tools/secret-manager.tsx` and its API helpers |
| Portal API snapshot `portal-client-api-workspace-activity-0926` | `app/routes/object_storage.py`; `app/schemas/object_storage.py`; `app/routes/secret_store_proxy.py`; `app/utils/config.py` |
| Native `object-storage` service | `app/api/native/v1/buckets.py`, `objects.py`, `credentials.py`, `custom_domains.py`; `app/schemas/native.py`; bucket service and empty-job implementation; `app/api/cdn/v1/` |
| Secret service snapshot `ibee-secret-activity-0926` | `app/api/v1/control/stores.py`, `secrets.py`, `identities.py`; identity, secret, lifecycle and store schemas/services; runtime endpoints |
| `platform-docs` public contract and gateway | `fern/openapi/ibee-cloud.yaml`; `gateway/source/public-api.route-map.yaml`; generated `gateway/deploy/prod/object-storage-httproute.yaml` and `secret-store-httproute.yaml` |
| `auth-service` | `app/services/api_key_service.py`, including `required_scope_for_request` and tenant/workspace enforcement |

The generated object-storage gateway uses prefixes for `/v1/object-storage/buckets` and `/v1/object-storage/credentials`. Consequently native subresources can fit the route layout even when the public OpenAPI omits them. Generated manifests contain deployment placeholders: source routing compatibility is not evidence of an installed, working route.

The secret gateway only has `/v1/secret-store/stores` and `/v1/secret-store/secrets` prefixes, rewritten to `/api/v1/control/stores` and `/api/v1/control/secrets`. Native recovery and version operations beneath these prefixes exist. Identity CRUD needs additional `/identities` and `/scopes` routing and a published token-authenticated contract. Creating an identity through the existing stores prefix alone would leave its normal read/update/delete operations unsupported.

Tables use paths relative to the provider endpoint, normally ending in `/v1`. `B` means `/object-storage/buckets/{bucket_name}`; `S` means `/secret-store/stores/{store_id}`; `K` means `/secret-store/secrets/{secret_id}`.

## Object storage: persistent configuration

| Portal feature | Native/public API evidence | Provider mapping and limitation | Local verification |
| --- | --- | --- | --- |
| Create bucket, name and region | POST `/object-storage/buckets`; canonical GET `B` | `ibee_bucket`. Name and region replace; region is an Object Storage region, not a compute site ID. | Unit lifecycle; real Terraform create/import/no-change/destroy. |
| Public/private access | PATCH `B`; equivalent settings/visibility routes exist | `ibee_bucket.is_public` updates and refreshes from canonical GET. Separate visibility resources would conflict with this ownership. | Unit update; real Terraform drift correction. |
| Bucket statistics and status | GET `B` returns flat `object_count`, `total_size`, region and status; portal uses a different nested projection | Computed bucket outputs include counts, size, status, plan and site ID. No invented mapping from portal response wrappers. | Canonical fixture decoding; failed/malformed reads preserve identity. |
| List/search/filter/paginate buckets | GET `/object-storage/buckets`; portal UI filters its list | No inventory data source yet; individual buckets support refresh/import. UI search is not a managed setting. | No list data-source test. |
| Automatic, specific and jurisdiction placement | Portal builds choices using configured sites; proxy supplies defaults | Explicit `region` only. No verified public Object Storage region discovery endpoint was found. Automatic default selection and jurisdiction/sales requests are not reproduced. | Region passed unchanged in unit and CLI fixtures. |
| Storage plan selection and prices | Portal catalog `/billing/catalog/object-storage/skus`; portal create accepts `plan` | Native `BucketCreate` does not declare a selectable plan, while GET exposes plan metadata. `ibee_bucket.plan` is output-only. Requires canonical selectable plan/price contract for full parity. | No claim that a portal plan selection changes native provisioning. |
| Standalone versioning checkbox | UI sends `versioning`; reviewed portal create model and native `BucketCreate` do not declare that input; no standalone versioning GET/PUT route found | **Backend/API gap.** Not exposed as a misleading provider flag. Object Lock's service-side versioning prerequisite is separate. | No standalone versioning claim or test. |
| Enable Object Lock when creating bucket | POST accepts `object_lock_enabled`; GET uses `bucket_lock_enabled` | `ibee_bucket.object_lock_enabled`, replacement required. Does not bypass object retention or legal hold on deletion. | Unit and schema tests. |
| Default retention mode/duration | GET/PUT `B/object-lock-configuration`, response `object_lock_enabled: "Enabled"`, `rule` with mode and days/years | `ibee_bucket_retention`; GOVERNANCE or COMPLIANCE, exactly one positive days/years duration. Preflight verifies lock support and refuses to overwrite an existing rule without import. | Unit create/read/update/import, validation, missing-route and malformed-response tests. |
| Clear default retention | PUT rejects `rule: null`; no DELETE route in reviewed native service | **Backend lifecycle gap.** Destroy fails unless `retain_on_destroy=true` was applied first. That opt-in only relinquishes Terraform management; it leaves remote retention intact. Existing retained objects are never erased. | Guard/default-on-import and explicit-retain tests. |
| CORS | GET/PUT/DELETE `B/cors` | `ibee_bucket_cors` owns the whole section; import existing rules first. Native service stores metadata and attempts public-URL/CDN synchronization; API readback does not establish browser enforcement. | Unit lifecycle/validation/failure tests; real Terraform configuration lifecycle suite. |
| Expiration lifecycle rules | GET/PUT/DELETE `B/lifecycle` | `ibee_bucket_lifecycle` owns the section. Enabled rules can permanently expire matching objects. Clearing rules does not recover expired objects. | Unit round-trip/validation; real Terraform configuration lifecycle suite. |
| Event notifications | GET/PUT/DELETE `B/notifications` | `ibee_bucket_notifications`: upload/delete categories, prefix/suffix filters, webhook URL, computed expanded S3 events. Regional destination setup and webhook delivery require separate validation. | Unit normalization/failure cases; real Terraform configuration lifecycle suite. |
| Encryption information | GET `B/encryption`; no setter in reviewed source | Read-only service capability, no encryption configuration resource. | Not represented as mutable Terraform state. |
| Bucket tags | Native create accepts tags; canonical bucket response omits them and no tag update lifecycle was found | **Round-trip API gap.** A create-only input would make import and drift unreliable, so no managed tag attribute. | No fabricated readback. |
| Delete bucket | DELETE `B`; source service checks MinIO emptiness and may reject nonempty/retained versions | `ibee_bucket` defaults to a fresh zero-count/zero-size preflight and fails closed if unknown. Check is not atomic with other writers. `force_destroy=true` only skips provider counters; it does not purge objects or override service restrictions. Deployment behavior must be verified. | Unknown/nonempty guard tests; server rejection retains state; CLI empty-bucket destroy. |

## Object storage: credentials, delivery and operations

| Portal feature | API evidence | Provider mapping or operation boundary | Local verification |
| --- | --- | --- | --- |
| Create S3 access key | POST `/object-storage/credentials` returns secret once plus identity, tenant and complete ACL metadata | `ibee_s3_credential`; name, permission and bucket scope must be explicit. Any configured change replaces the key. | Unit and real Terraform create/no-change/replacement. |
| Object read-only/read-write and bucket restrictions | `permission_type`, `bucket_scope`, `allowed_buckets` round-trip in native create/GET | `object_ro`/`object_rw`, explicitly `specific` or `all`. Specific requires bucket names; all rejects a nonempty bucket list. No implicit broad permission default. | Invalid scope tests; real Terraform remote permission drift requires replacement. |
| Admin read-only/read-write credentials | Backend normalizes admin credentials to all buckets | Provider accepts `admin_ro`/`admin_rw` only with explicit `bucket_scope="all"` and an empty list. Rejects misleading admin+specific configuration. | Validation matrix. |
| Credential read/list/import | GET credential by access key ID includes ACL, organization and workspace; list endpoint also exists | Import by access key ID, refresh verifies tenant/identity and exact readable scope. No inventory data source. | Unit import/failed-read tests; CLI import/no-change. |
| Generated secret display/download | Secret only appears on POST creation, never GET | `secret_access_key` is computed sensitive **and persisted as plaintext in Terraform state**. Sensitive redacts presentation; it is not encryption. Imported keys have null secret. Use protected encrypted state; never assume import recovers it. | Unit sensitive schema/redaction; CLI confirms stored creation secret, null import secret, no console leak. |
| Revoke credential | POST `/credentials/{id}/revoke`; GET then reports revoked or absent | Destroy uses explicit revoke and confirms inactivity. Does not require billing admission. A revoked credential is absent from active managed state on refresh. | Unit ambiguous/failed/404/inactive confirmation; CLI replacement and destroy with billing denied. |
| Permanently remove credential metadata | Native DELETE now permanently removes metadata; older public schema describes DELETE as revoke | Not automatically used: provider selects the explicit revoke operation. Cleanup of revoked audit rows remains a deliberate operation. Public docs should align with current source. | Unit verifies POST revoke, not ambiguous DELETE. |
| CDN distribution and cache policy | `/cdn/distributions` CRUD; create enables by default; enabled is update-only | `ibee_cdn_distribution`; configured disabled creation performs the follow-up disable. Canonical bucket ID is output; configured bucket-name aliases retained only with matching API `bucket_name`. | Real Terraform disabled creation/update/drift/import, alias regression. |
| Custom HTTP(S) origin | `/cdn/origins` CRUD | `ibee_cdn_origin`; target deployment must expose this route. | Real Terraform lifecycle and failure tests. |
| Static website / SPA routing | Distribution website-config GET/PUT/DELETE | `ibee_cdn_website`; destroy disables routing and preserves distribution and objects. An index object must exist for features whose service validates it. | CLI create/update/import/targeted destroy/recreate. |
| Distribution custom domain and DNS instructions | Custom-domains CRUD; GET includes `validation.cname_record` | `ibee_cdn_domain`; `cname_name`/`cname_target` outputs. Pending validation remains pending. No fabricated TLS status or DNS automation. | CLI pending status and CNAME outputs before/after import. |
| Verify custom domain | POST distribution custom-domain `/verify` | Explicit `ibee_cdn_verify_domain` action, Terraform 1.14+. Does not run during refresh. DNS/TLS can remain pending. | Unit domain identity/status checks; actual CLI `-invoke` verifies pending warning. |
| Purge cached content | POST distribution `/purge`, targeted or all modes | Explicit `ibee_cdn_purge` action, Terraform 1.14+. Clears CDN cache, not origin objects; entitlement restrictions remain service-owned. | Unit mode/target validation; actual CLI `-invoke` exact URL payload. |
| Legacy bucket dev/CDN public URLs | `B/public-url/dev`, `/cdn`, common GET and DELETE routes | No separate resource for this older bucket-level API. Prefer the implemented distribution/website model where supported; do not manage both APIs as competing owners. Legacy endpoint parity remains unimplemented. | No claim of legacy endpoint equivalence. |
| Legacy bucket custom domain | `B/custom-domain` and target/delete subroutes | Separate from distribution custom domains; no legacy resource. Requires a chosen ownership/migration model if customers depend on both surfaces. | Not covered by distribution fixtures. |
| Linked resource inventory | GET `B/resources` | Observational API; no inventory data source yet. | No managed lifecycle assertion. |
| Usage, bandwidth and metrics | Portal bucket metrics/bandwidth routes and object-storage billing-month usage | Observability/billing reporting, not desired infrastructure configuration. No metrics data source yet. | Not tested as Terraform resources. |
| Empty bucket job, progress and cancel | Native `empty-jobs` create/list/get/cancel operations | Destructive explicit operation; not called implicitly by bucket destroy. Reviewed initial engine rejects unsupported versioning/Object Lock cases. No Terraform empty action implemented. | Bucket guard tests establish that no purge request is sent. |
| Browse objects, folders, pagination and metadata | Native object listing, delimiter/prefix and HEAD/metadata; S3-compatible routes | Data-plane reads. Folders are object-key prefixes/marker objects, not separate infrastructure resources. | No Terraform object inventory. |
| Upload/download/copy/delete and batch deletion | Native/S3 object methods and copy/bulk-delete routes | S3 SDK/CLI operations; no object-content resource implemented. Large or changing datasets should not be represented as bucket configuration. | No upload or object deletion is performed by provider tests. |
| Presigned uploads/downloads | Native presigned-get and presigned-put | Expiring bearer URLs are operational access, not persistent resource state. No URL data source. | No presigned secrets placed in state. |
| Multipart upload, presigned parts, complete/abort/list | Native multipart routes and S3 multipart protocol | Transfer/session operations; use S3 tooling. | Outside provider lifecycle tests. |
| Per-object retention and legal hold | Native GET/PUT object retention and legal-hold; optional object version ID | Data-object policy APIs exist, but no provider resource/action yet. This is separate from default bucket retention and requires explicit irreversible-retention semantics before implementation. | No claim that bucket retention manages existing objects' individual policies. |

## Secret manager

| Portal feature | Native/public API evidence | Provider mapping or remaining boundary | Local verification |
| --- | --- | --- | --- |
| Create store; edit name and description | POST `/secret-store/stores`, GET/PATCH `S` | `ibee_secret_store`; canonical identity/store key/status outputs; import by store ID. | Unit read/update; real Terraform create/import/no-change/archive. |
| List/search/include archived stores | GET stores with archived filtering; UI search | Resource refresh/import is implemented; inventory data source is not. | Archive/tombstone handling tested, not a list data source. |
| Archive store | POST `S/archive` | Destroy archives. Default guard paginates all secrets and rejects active or uncertain records. `force_archive=true` explicitly permits archiving active contents. Names/history remain reserved. | Pagination/unknown count/list failure guards; CLI dependency-ordered secret delete then store archive. |
| Unarchive store | POST `S/unarchive` exists natively under routed prefix | Recovery operation not implemented as resource state or action. An externally archived managed store retains identity with a warning; no automatic recreation or unarchive. | Archived state retained; no claim that API is absent. |
| Permanently delete store | DELETE `S/permanent` exists natively | Destructive purge operation, not normal Terraform destroy. Backend confirmation/eligibility applies. | No implicit permanent-delete calls. |
| Create JSON secret | POST `S/secrets` | `ibee_secret`; required store, immutable secret name, write-only JSON object value. Terraform >=1.11. JSON numbers retain precision. | Unit validation/create; real Terraform write-only lifecycle. |
| Read metadata/value and current version | GET `K` and `K/value` | Outputs metadata/current version; refresh decodes version and discards secret payload. No readable secret-value output or data source. | Unit and CLI prove secret value does not persist in state or appear in diagnostics. |
| Rotate complete value | PUT `K/value` with compare-and-set version | `value_wo` plus increasing `value_wo_version`; changing the write-only value alone cannot trigger a diff. CAS prevents overwriting a concurrent version. | Unit CAS/conflict/precision; CLI incremented trigger creates next version. |
| Partial JSON update | PATCH `K/value` exists | Provider owns the full JSON object and uses PUT; callers can supply a merged object. No partial-field ownership semantics. | Full-value/CAS coverage only. |
| List/search/paginate secrets | GET `S/secrets` with paging/search/status | Used for safe archive guard. No standalone inventory data source. | Multi-page archive check including soft-deleted rows. |
| Bulk import / batch ingest | POST `S/secrets:batchIngest` | One-off ingestion; use individual `ibee_secret` instances for persistent ownership. No batch action. | Individual secret lifecycle verified. |
| Soft-delete secret | DELETE `K` | Normal destroy soft-deletes. Supports native `soft_deleted` and older public `deleted` status. Names/history remain reserved; recreation with the same name may fail. | Unit tombstone/error handling; CLI verifies `soft_deleted`. |
| Restore deleted secret | POST `K/undelete` exists | Explicit recovery operation, not automatic refresh mutation. Tombstones retain identity/warning so a plan does not silently pretend deletion is permanent. | Tombstone read tests; no undelete action. |
| Version list and version detail | GET `K/versions` and `K/versions/{version}` | Current version is an output; history/value data sources are not implemented. | Current version only. |
| Roll back to earlier version | POST `K/rollback` exists | Explicit version-changing operation. No action yet; it must coordinate with Terraform's rotation counter/CAS. | No automatic rollback. |
| Destroy selected versions | POST `K/destroy` exists | Irreversible history erasure; intentionally excluded from resource destroy. | No destroy-version calls. |
| Permanently delete secret | DELETE `K/permanent` exists | Separate destructive operation; not normal resource destroy. | No permanent-delete calls. |
| AppRole machine identity | Store identity POST/list, identity GET/PATCH/delete native routes | **Public lifecycle routing gap.** Backend supports this, but public identity prefix is absent in reviewed gateway. No partially manageable identity resource. | No speculative API use. |
| Kubernetes machine identity | Same identity routes with namespace/service-account binding | Same publication gap. Future resource must round-trip auth method, token policy, binding and enabled state. | No claimed Kubernetes identity lifecycle. |
| Identity rename, policy and enable/disable/revoke | Native identity PATCH and action routes | Public identity prefix required. Not currently exposed in provider. | Not tested through public gateway. |
| Identity access details / AppRole secret ID rotation | GET identity `/access`; POST `/rotate-secret-id` | GET access can generate a fresh secret ID. It must never be used as a harmless refresh. Future credential output/action needs explicit generation and secret handling. | No identity access calls from provider. |
| Identity-to-store scopes and privileges | Native POST/list identity scopes; PATCH/DELETE `/scopes/{id}`; access mode, version-read, rollback and destroy permissions | **Gateway/API gap:** publish identity and scope routes and canonical identity before adding managed scopes. | No fabricated scope resource. |
| AppRole/Kubernetes runtime login and token | Runtime auth endpoints outside reviewed public control prefixes | Runtime application authentication, not a persistent credential resource. Session tokens should not be materialized by ordinary Terraform refresh. | Outside infrastructure lifecycle. |
| Runtime reads by store key/name and batch read | Runtime secret APIs | Application consumption through runtime SDK/client; no plaintext Terraform data source. | Outside provider reads apart from metadata-only control read described above. |
| Service health, readiness and startup | Native health endpoints | Operational diagnostics; no desired-state resource. | Not presented as provisioning coverage. |

## Billing and credentials shared by both products

The provider authenticates through the public platform token and explicit workspace scope, with optional organization verification. Portal browser cookies and S3 credentials are not substitutes for the provider token. Read and write access must be granted by the platform; resource refresh must not treat permission errors as absence.

Bucket, secret-store, secret and S3 credential creation call the shared billing admission helper. These resources use account-level admission without an invented SKU or client-computed price because no verified public product quote/catalog integration is available here. The backend remains responsible for operation-specific pricing, quotas and admission at mutation time; the preflight is not a purchase reservation. Deletes, archive and revoke do not require new-purchase admission.

Adding credits, checkout, payment retries and credit-limit administration are not Terraform resource lifecycles and have no implemented provider workflow. The provider reports billing refusal and the portal recovery step; it must not manufacture credits or assume a requested payment succeeded. See [COVERAGE.md](COVERAGE.md) and [PORTAL_PARITY_ASSESSMENT.md](PORTAL_PARITY_ASSESSMENT.md).

## Reproducible local coverage

| Test source | Main checks |
| --- | --- |
| [storage_secrets_test.go](internal/provider/storage_secrets_test.go) | Bucket lifecycle/failed reads/deletion guards; paginated archive guard; write-only values/CAS/JSON validation; tombstone semantics and error redaction. |
| [bucket_retention_resource_test.go](internal/provider/bucket_retention_resource_test.go) | Retention validation/lifecycle/import, existing-policy protection, missing endpoint versus missing bucket, explicit retain-on-destroy, nonempty service rejection. |
| [bucket_configuration_resource_test.go](internal/provider/bucket_configuration_resource_test.go) and [CLI suite](internal/provider/bucket_configuration_terraform_test.go) | CORS/lifecycle/notifications round-trip, import, schema validation, failed reads/writes, section cleanup. |
| [terraform_lifecycle_test.go](internal/provider/terraform_lifecycle_test.go) | Actual Terraform bucket/store/secret create, import, no-change, secret rotation, no plaintext in state, billing refusal and allowed cleanup. |
| [s3_credential_resource_test.go](internal/provider/s3_credential_resource_test.go) and [CLI suite](internal/provider/s3_credential_terraform_test.go) | Explicit ACL validation, one-time sensitive secret, import null secret, drift replacement, tenant/identity errors, revoke confirmation, no billing on teardown. |
| [cdn_terraform_lifecycle_test.go](internal/provider/cdn_terraform_lifecycle_test.go) and [action unit tests](internal/provider/cdn_action_test.go) | Four resource types, canonical bucket alias, CNAME outputs, no-change/import, mutable settings, failed refresh preservation, cleanup under billing denial; native purge/verify invocation on Terraform >=1.14. |

Run the focused local suites with:

```sh
go test ./internal/provider -run 'Test(Bucket|Secret|S3Credential|CDN)' -count=1
IBEE_TF_TEST=1 go test ./internal/provider -run '^TestTerraformLifecycle($|S3Credential$|CDN$|BucketConfiguration$)' -count=1 -timeout=5m
```

The CLI suites strip inherited provider/Terraform credential variables, use fixture tokens, and talk only to loopback HTTP servers. They do not provision live infrastructure. [examples/storage/main.tf](examples/storage/main.tf) is the standalone bucket/store/secret configuration, using an ephemeral sensitive input and explicit rotation version. Live tests still need environment-specific regions, available names, correct token scopes, billing authorization and independently checked cleanup semantics.
