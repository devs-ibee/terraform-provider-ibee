# Development storage and secret-manager live test results

Executed 2026-09-27, approximately 16:46–16:57 UTC, after explicit authorization for disposable development tests with an allocation of up to ₹300. Terraform used an isolated local provider build against `https://api.ibee.co.in/v1`. Credentials came from the ignored development configuration through the subprocess environment. Account identifiers, test identities, tokens and secret values are excluded.

**Result:** all eight tested storage/secret resource types passed their supported control-plane lifecycle. A separate S3-compatible data-plane test failed on its first object-list request with `403 AccessDenied`. All test buckets were deleted and both generated S3 keys revoked. The disposable secret is soft-deleted and its store archived, as documented; their reserved identities/history remain.

## Terraform lifecycle results

| Resource | Live checks and result |
| --- | --- |
| `ibee_bucket` | Created an empty private bucket in `in-south-2` with Object Lock enabled; canonical GET reported active, zero objects and zero bytes. No-change plan and import passed. Updated public visibility while empty; full plan then had no changes. Destroy passed with `force_destroy=false`; independent import confirmed the bucket no longer exists. |
| `ibee_bucket_cors` | Create/read/import/update/no-change/destroy passed. Used `https://example.invalid`, GET/HEAD, and updated maximum age from 300 to 600 seconds. Configuration lifecycle only; browser/S3 CORS enforcement was not tested. |
| `ibee_bucket_lifecycle` | Create/read/import/update/no-change/destroy passed with a **Disabled** expiration rule. Changed duration from 30 to 31 days. No expiration was enabled and no objects existed. |
| `ibee_bucket_notifications` | Create/read/import/update/no-change/destroy passed. Used a never-used prefix/suffix and no external webhook URL; updated event categories and suffix. No object events were generated. Delivery was not tested. |
| `ibee_bucket_retention` | Created/read/imported a one-day GOVERNANCE default on the empty locked bucket, updated to two days, and obtained a no-change plan. Default targeted destroy correctly failed because the service cannot clear this rule. Applied `retain_on_destroy=true`; Terraform then relinquished the rule and deleted the empty parent bucket. No object was ever placed under retention. |
| `ibee_s3_credential` | Created an `object_ro` key scoped to exactly the first test bucket. Canonical read, import and no-change passed; imported `secret_access_key` was null. Destroy used explicit revoke plus GET confirmation; independent import could not restore an active key. A second `object_rw` key scoped only to the separate unlocked data-test bucket was created/read and later revoked/verified. Configuration changes require replacement; replacement was tested locally rather than creating further live keys. |
| `ibee_secret_store` | Create/read/import/no-change passed. Updated description and obtained a no-change plan. Destroy archived the store after its secret was soft-deleted. Independent read/import confirmed `status="archived"`. |
| `ibee_secret` | Create/read/import/no-change passed. Rotated the complete JSON value once by increasing `value_wo_version`; subsequent plan had no changes. Generated test values were absent from inspected Terraform states. Destroy soft-deleted the secret; independent read/import confirmed `status="soft_deleted"`. No permanent erasure or history destruction was requested. |

The six primary storage resources converged together before and after import and after mutable updates. The secret/store configuration converged before import, after import and after rotation. Every test used uniquely prefixed disposable identities. Existing customer resources were not modified.

## S3-compatible data-plane failure

The separate data-test bucket was private, empty, `object_lock_enabled=false`, and in `in-south-2`. Its key had canonical `status="active"`, `permission_type="object_rw"`, `bucket_scope="specific"`, and `allowed_buckets` containing exactly that bucket. A fresh Terraform read/no-change plan confirmed these fields again before cleanup.

| Setting/check | Observed value/result |
| --- | --- |
| Endpoint class | `https://{workspace_id}.blob.ibeestorage.com`, from the public S3 guide and portal credential UI. Actual workspace hostname retained privately. |
| Client | Installed `boto3`/`botocore`, normal client signature, explicit generated credentials, HTTPS and path-style addressing. No browser impersonation or alternate endpoint retry. |
| Signing | AWS Signature V4, signing region `us-east-1`, matching the public CLI guide. The bucket storage region is separately `in-south-2`. |
| Retries | One attempt; SDK request retries disabled. |
| First operation | `ListObjectsV2`, restricted to the disposable test object's prefix. |
| Result | HTTP `403`, S3 error code `AccessDenied`. |
| Upload/download/HEAD/delete | Not attempted after access denial. No object uploaded. |
| Revocation verification | Control-plane revoke/inactivity confirmation passed; independent import rejected the key as inactive/absent. Since access was already denied before revocation, this does not establish a successful-access-to-denied-access transition on S3. |

The reviewed authorization mapping classifies `ListObjectsV2` as object-read access, which `object_rw` includes. The provider-generated ACL matched the intended bucket. The deployment-side cause remains unresolved: this result does not distinguish endpoint/environment routing, credential propagation or authorization behavior. Confirm that the documented workspace S3 hostname uses the same development credential store and tenant/workspace context, inspect service logs, and repeat the bounded round trip after resolving access. Do not widen the key to admin/all-bucket permissions merely to make this test pass.

This failure is separate from native public-API uploads or CDN delivery tested elsewhere; those routes do not prove S3-compatible endpoint behavior.

## Other observed issues

- An initial Python `urllib` billing probe received Cloudflare HTTP 403 / error `1010`, `browser_signature_banned`, with `retryable=false` and `owner_action_required=true`. That blocked client path was not retried or modified. The team's independently verified normal Terraform provider client then performed provisioning and verification with its existing truthful client signature. Its billing preflight and operations succeeded. Python-client access remains a separate compatibility observation.
- Sandboxed local validation initially could not start the provider's local plugin socket. Required local/network permissions resolved this; it was not a remote API failure.
- Retention destroy refusal was expected safety behavior. Explicitly applying `retain_on_destroy=true` permitted cleanup; the provider did not claim to clear the remote default rule.

## Cleanup, usage and remaining limits

Two disposable buckets were created; both remained empty and both are deleted. Two S3 keys were created; both were revoked with confirmation and could not be imported as active afterward. The test secret is soft-deleted and its store archived; this preserves service history/names and is not permanent deletion. Configuration sections were removed during the first bucket's Terraform teardown. No customer resource was changed, and no external webhook destination was configured.

The run created no VM, volume, public delivery distribution or payment attempt, and requested no extra credit funding. Usage was limited to empty buckets, two scoped keys and one tiny secret rotated once. Account-level billing admission passed before creates, but this run did not obtain a product quote or independently measure final charges; it does not claim a verified ₹0 charge or enforce a rupee reservation.

Private configurations, timestamps, resource IDs and sanitized command logs are retained in a mode-restricted temporary workspace for audit, outside the repository. After verifying cleanup, all temporary Terraform state/backup files were deleted and generated S3 secrets and ephemeral test values were removed from the private manifest/logs. The ignored platform credential source was not changed. No provider source changes were necessary for these live control-plane checks.

Still unverified: successful S3 object transfer and data-plane revocation transition; CORS browser enforcement; expiration execution; notification delivery; per-object retention/legal hold enforcement; permanent secret-history deletion; and payment workflows. None is inferred from configuration readback.
