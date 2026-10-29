# Changelog

## 0.7.0

Moves the provider to public API contract 0.7.1 (vendored at `contract/ibee-cloud.yaml`) and adds Container Registry. The public API now runs the portal's own checks, so the provider is a thin layer over it: each change is one API operation, settings that are not configured are not sent (the API's defaults apply and the API's values are recorded), and the API decides prices, eligibility, limits and lifecycle. The provider has no validation, defaults or rules of its own; the only value check is a contract enum (`ibee_s3_credential.permission_type`). The one convenience left is the VM `billing_interval`: the provider builds `billing_catalog` from `GET /compute/plans`, as the portal does. Server errors are shown as the server sent them (status, code, message, request id).

### Breaking changes

- Removed `ibee_billing_eligibility`: contract 0.6 has no `POST /billing/resource-eligibility`.
- Removed `ibee_block_volume_attachment`: the block-storage attach and detach operations are gone. Attach a volume to a VM with `ibee_cloud_vm_volume_attachment` or `ibee_gpu_vm_volume_attachment`.
- Removed the `ibee_vm_backup_run` action: the manual backup run operation is gone.
- Removed `ibee_bucket_retention` and `ibee_cdn_origin` (deprecated in 0.6.0; the API never served their routes). Set a bucket's default retention with `ibee_bucket.default_retention`. Run `terraform state rm` for these resources before upgrading.
- `ibee_cloud_vm`, `ibee_gpu_vm`: `os_type` is required (the contract requires it; it defaulted to `linux`).
- `ibee_compute_plans`, `ibee_images`: `vm_type` is required (the contract requires it; it defaulted to `cloud`).
- `ibee_bucket`: `force_destroy` and the provider's object-count check before destroy are removed; destroy sends the delete and the API decides.
- `ibee_cloud_vm_volume_attachment`, `ibee_gpu_vm_volume_attachment`: `billing_catalog` is no longer read from the volume; set it, for example to `ibee_block_volume.<name>.billing_catalog`. `mode` and `confirm_unmounted` have no provider default and are sent only when set; the attachment is no longer refused when the VM already reports the volume.
- Contract 0.6 removed these update operations, so a change now replaces the resource: `ibee_secret_store` `name` and `description` (the archived store keeps its name, so a replacement needs a new name); `ibee_secret_identity` `token_policy_mode`; every `ibee_secret_identity_scope` permission; `ibee_vpc_subnet` `name` and `dns`.
- `ibee_cloud_vm_backup_policy`, `ibee_gpu_vm_backup_policy`: the policy update operation has no `billing_catalog`, so a `billing_catalog` change replaces the policy. Schedule settings no longer have provider defaults (daily, 12:00 UTC, 7 days); the API's defaults apply and are recorded.
- `ibee_vpc`: `owned_default_subnet_id` and `owned_default_nat_gateway_id` are removed. Destroy sends `DELETE /networking/vpcs/{id}` and the API decides what happens to its subnets and NAT gateway; the provider no longer inspects or deletes children first, and create no longer refuses a duplicate name.
- `ibee_load_balancer_l4`, `ibee_load_balancer_l7`: the provider no longer composes TLS settings from `protocol`. Set the new `tls` block (for example `{ mode = "terminate", certificate_source = "managed" }` for `https`).
- `ibee_vm_access`: `admin_username` is removed (not in the contract).
- Provider: `organization_id` is deprecated and unused.

### New data sources

- `ibee_block_storage_plans` (`GET /block-storage/plans`): plans with `sku_code`, sizes, prices, `iops` and `throughput` (display text, as in contract 0.6.3) and `billing_catalog`.
- `ibee_snapshot_storage_plans` (`GET /compute/plans/snapshot-storage`) and `ibee_backup_storage_plans` (`GET /compute/plans/backup-storage`): the storage SKUs. Pass a plan's `billing_catalog` unchanged to a snapshot or backup policy.
- `ibee_container_registries`: the workspace's registries, every page.

### New resources and attributes

- `ibee_container_registry`: a registry with a storage plan (`sku_code`). Create waits until `observed_state` is `ACTIVE` with every change applied, and stops on `ERROR`. `visibility` (with `acknowledge_public_egress_charges`) and `vulnerability_pull_policy` change in place; a new `sku_code` is sent as a plan upgrade with an `Idempotency-Key` header. Destroy sends `confirm_registry_name` and waits until the API reports the registry deleted. Import with the registry ID.
- `ibee_container_registry_access_token`: a docker login credential. Every input replaces the token. `username` and the sensitive `secret` are returned once, at creation, and kept in state. Destroy revokes the token. Import with `REGISTRY_ID/TOKEN_ID`.
- `ibee_bucket`: `default_retention` (`mode`, `days`, `years`) for a bucket with object lock, sent with create and changed with `PUT .../object-lock`; read back from `GET .../object-lock`.
- `ibee_bucket_lifecycle`, `ibee_bucket_cors`, `ibee_bucket_notifications` are no longer deprecated: they use the contract's settings operations (GET, PUT, DELETE `/object-storage/buckets/{name}/{lifecycle,cors,notifications}`). CORS rules are written with the contract's snake_case keys; the API's CORS read currently answers with S3-cased keys (`AllowedOrigins`, ...), which the provider reads as they are. Create replaces whatever rules the bucket has.
- `ibee_block_volume`: `billing_catalog` (read-only, from the volume's metadata) for volume attachments; `replica_count` can be set; `sku_code` and `volume_class` are optional.
- `ibee_load_balancer_l4`, `ibee_load_balancer_l7`: `tls` (`mode`, `certificate_source`, `cert_pem`, `key_pem`), sent with create.
- `ibee_vm_power`: `force`.
- `ibee_images`: `site_id`.

### Changed

- `ibee_cloud_vm`, `ibee_gpu_vm`: `billing_interval` is `HOURLY` or `MONTHLY` (a one-month commitment; `YEARLY` no longer exists) and defaults to `HOURLY` for new VMs; an existing VM without a configured term keeps its recorded term, so upgrading never replaces a VM. Create sends the contract's `ComputeBillingSelection`: a copy of the plan's `billing_catalog` (keeping `price_currency`) with the whole matching `billing_options` entry merged in. When the plan lists no such entry the catalog is sent as listed and the API answers. The API refuses to delete or resize a committed VM (409 `VM_COMMITTED`).
- `ibee_cloud_vm`, `ibee_gpu_vm`: a `plan_id` change calls `POST .../actions/resize` with the new plan's `cpu`, `ram_mb` and `disk_gb` only (resize-plan and resize-root-disk are gone). `ssh_key_ids` and `tags` have no provider default; the API's values are recorded.
- `ibee_s3_credential`: destroy revokes the key (`POST /object-storage/credentials/{id}/revoke`) and then deletes it (`DELETE /object-storage/credentials/{id}`). A key revoked outside Terraform stays in state with `status = "revoked"` and destroy only deletes it. `name`, `permission_type`, `bucket_scope` and `allowed_buckets` are optional; the API's defaults apply. `permission_type` accepts the contract's values `admin_rw` (the API's default), `admin_ro`, `object_rw` and `object_ro`.
- `ibee_cloud_vm_backup_policy`, `ibee_gpu_vm_backup_policy`: destroy calls `backups/disable` with an empty JSON object (`{}`), as contract 0.7.1 declares it; the production virtualization engine refuses a request with no body. An update sends only the settings that changed.
- Compute requests no longer send `requested_by` (removed from the contract). `ibee_vm_power` sends `force` only when it is set; detach-volume and block-volume delete no longer send `force`.
- Removed client-side rules: attribute validators (except the contract enum above) and configuration checks (names, CIDRs, ports, enums, ranges, time zones, URLs, JSON shapes, scope combinations, purge targets), provider-chosen defaults (VPC, subnet, firewall rule, NAT, port-forwarding, CDN, load balancer, block volume, snapshot, bucket, secret store), the checks before create or destroy (existing configuration, attached volumes, NAT inventory and children, reserved IP ownership, bucket usage, secret rotation order), the VM power-state confirmation after a power operation, the response completeness checks that refused to save state, the token/endpoint environment check and the timeout range limits.
- Retries: only reads (GET, HEAD) are retried, on 429, 502, 503 and 504. Writes are never retried: after any error the write may already have been applied. The identity access read (`GET /secret-store/identities/{id}/access`) is never retried, because every call mints a new AppRole secret ID.
- `ibee_cloud_vm`, `ibee_gpu_vm`: `delete_public_ip_action` has no default; when it is unset the destroy sends no `public_ip_action` and the API answers (400 for a VM with an automatically assigned public IP).

### Development

- `contract/ibee-cloud.yaml` is contract 0.7.1. Every request the provider sends is an operation in it; the list of tolerated off-contract routes is now empty.

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
