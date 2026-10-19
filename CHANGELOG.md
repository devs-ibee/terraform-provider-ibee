# Changelog

## 0.6.0 (unreleased)

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
