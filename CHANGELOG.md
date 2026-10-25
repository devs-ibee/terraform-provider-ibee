# Changelog

## 0.7.0

Adds Container Registry from the public API contract 0.5.0 (vendored at `contract/ibee-cloud.yaml`). Like the rest of the provider, these call the API and relay its answer; the registry service decides names, plans (it asks Billing), visibility and states.

### New resources

- `ibee_container_registry`: a registry with a storage plan (`sku_code`). Create waits until `observed_state` is `ACTIVE` with every change applied, and stops on `ERROR`. `visibility` (with `acknowledge_public_egress_charges`) and `vulnerability_pull_policy` change in place; a new `sku_code` is sent as a plan upgrade with an `Idempotency-Key` header, and the API refuses smaller plans. Destroy sends `confirm_registry_name` and waits until the API reports the registry deleted; the registry must be empty (the API answers 409 `registry_not_empty` otherwise). Import with the registry ID (`sku_code` is then read from the registry's plan).
- `ibee_container_registry_access_token`: a docker login credential. Every input replaces the token. `username` and the sensitive `secret` are returned once, at creation, and kept in state; reads list the registry's tokens (the API has no single-token read) and never return the secret. Destroy revokes the token. Import with `REGISTRY_ID/TOKEN_ID`.

### New data source

- `ibee_container_registries`: the workspace's registries, every page.

### Changed

- `ibee_cloud_vm`, `ibee_gpu_vm`: `delete_public_ip_action` no longer defaults to `release`. It is optional with no default; when it is unset the destroy sends no `public_ip_action` and the API's answer is relayed, a 400 for a VM with an automatically assigned public IP. Set `release` or `reserve` in the configuration (and apply) before destroying such a VM. A state that still holds the old default shows a one-time in-place change to null on the next plan; no API call is made for it.

### Fixed

- Writes (anything but GET/HEAD) are no longer retried on 503, 502 or 504, with or without an idempotency key: the request may already have been applied, and the API has not confirmed that a replayed create with the same key is de-duplicated, so a retry could leave a second VM or use up a registry name. Writes retry only on a 429 that carries no billing or admission answer. Reads still retry on 429, 502, 503 and 504.
- The identity access read (`GET /secret-store/identities/{id}/access`, made once when `ibee_secret_identity` is created) is never retried: every call mints a new AppRole secret ID. If it fails, the identity stays in state with empty `role_id` and `secret_id`, as before.

## 0.6.0 (2026-10-19)

Adds the VM recovery, VM access and Secret Store identity operations the public API contract (0.4.0) already has. Like the rest of the provider, these call the API and relay its answer.

### New actions (Terraform 1.14 or later)

- `ibee_vm_snapshot_restore`: restores a cloud or GPU VM snapshot set with `target_mode` `replace`, `new_vm` or `volume_only`, and waits for the restore to finish.
- `ibee_vm_backup_restore`: restores a backup recovery point the same way.
- `ibee_vm_backup_run`: runs a manual backup now and waits for the run to finish.
- `ibee_vm_access`: adds or removes inline SSH keys or Secret Store key IDs, resets the Linux user password, or turns SSH password login on or off. `ibee_cloud_vm` and `ibee_gpu_vm` still update `ssh_key_ids` in place.

### New resources

- `ibee_secret_identity`: an AppRole or Kubernetes application identity for a secret store. `token_policy_mode` and `enabled` change in place. The AppRole `role_id` and `secret_id` are read once, at creation, and kept in state (`secret_id` is sensitive).
- `ibee_secret_identity_scope`: grants an identity access to another store; permissions change in place.

### Fixes

- `ibee_s3_credential`: destroy now revokes the key with `DELETE /object-storage/credentials/{access_key_id}`, the contract's revoke operation. The previous `POST .../revoke` route is not served by the API gateway.
- `ibee_secret_store`: the archived-store warning no longer says the API cannot unarchive.

### Deprecated

The public API contract has no operation for these resources, so the API gateway does not serve the routes they call. They will be removed in a future release unless the API adds the operations.

- `ibee_bucket_cors`, `ibee_bucket_lifecycle`, `ibee_bucket_notifications` (`/object-storage/buckets/{bucket_name}/{cors,lifecycle,notifications}`)
- `ibee_bucket_retention` (`/object-storage/buckets/{bucket_name}/object-lock-configuration`)
- `ibee_cdn_origin` (`/cdn/origins`)

### Development

- The public API contract is vendored at `contract/ibee-cloud.yaml`. The test suite now checks that every request the provider sends is an operation in it, and fails when an API call site is neither exercised by a test nor checkable from its source.
- Every action page has an Example Usage section.

## 0.5.0 (2026-10-19)

The provider is now a thin layer over the IBEE API: resources call the API and relay its answer. Business rules (pricing, eligibility, lifecycle, state checks) are decided by the API, not by the provider.

### Breaking changes

- `ibee_secret_store`: the `force_archive` argument is removed. Destroy archives the store and the API decides whether that is allowed. Remove the argument from your configuration.
- `ibee_cloud_vm`, `ibee_gpu_vm`: `billing_interval` no longer defaults to `HOURLY`. Set it explicitly for new VMs; when omitted, the plan's catalog is sent unchanged and the API decides.
- `ibee_compute_plans`: `currency` and `billing_interval` no longer default to `INR` and `HOURLY`; omitted values are not sent and the API's defaults apply.

### Behaviour changes

- `ibee_cloud_vm`, `ibee_gpu_vm`: changing `plan_id` resizes the VM in place through the resize operation instead of replacing it. The API runs its own precheck, stops and starts the VM when it must, and refuses changes it cannot make. Changing `ssh_key_ids` updates access in place.
- `ibee_block_volume`: shrinking `size_gb` no longer replaces the volume. The resize is sent and the API's refusal is shown.
- VM refresh and import accept any billing term the API reports (for example three- or six-month commitments bought in the portal).
- Removed provider-side checks: plan "selectable and priced" gating, VM power-state table in `ibee_vm_power`, snapshot/backup/attachment `billing_catalog` content rules and the plan-time "missing catalog" error, and the eligibility data source's `billing_mode` and completeness checks.
- Error diagnostics show the HTTP status, server code, server message and request id. The provider's own guidance sentences are removed. Secret resources still show only the status.
- Retries: safe reads retry on 429, 502, 503 and 504. Writes, including VM create, retry only on 429 or 503 without a billing or admission answer, and never on 502 or 504.

### Temporary

- VM create and resize still compose the plan's `billing_catalog`, snapshots, backups and attachments still pass a `billing_catalog`, and VM delete still sends `public_ip_action`, until the API resolves these itself.
