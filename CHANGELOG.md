# Changelog

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
