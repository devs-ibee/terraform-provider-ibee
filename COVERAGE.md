# Implemented coverage and portal dependencies

This is the implementation status after the 2026-09-27 source review and development API checks. Some deployed routes are newer than the checked-in public API specification; verified native service contracts take precedence over an absent spec entry. The earlier [portal assessment](PORTAL_PARITY_ASSESSMENT.md) records the baseline and service-side requirements. All 19 portal categories remain in scope; navigation visibility does not establish a supported public API.

| Portal category | Provider implementation | Remaining dependency or boundary |
| --- | --- | --- |
| Cloud VMs | `ibee_cloud_vm` | All configuration changes replace; explicit power operations use ibee_vm_power (Terraform 1.14+). Catalog and canonical GET requirements below must be present in the target deployment. |
| GPU VMs | `ibee_gpu_vm` | Same requirements as cloud VMs; real GPU capacity is unverified. |
| Bare metal | Blocked | Reviewed portal handler does not provision; no public lifecycle contract. |
| Object storage | `ibee_bucket`, `ibee_bucket_retention`, `ibee_bucket_cors`, `ibee_bucket_lifecycle`, `ibee_bucket_notifications`, `ibee_s3_credential` | Default retention cannot be cleared; destruction is guarded. CORS/notification configuration readback is not data-plane delivery proof. Standalone versioning API is absent; uploads/downloads remain S3 data operations. |
| Block storage | `ibee_block_volume`, `ibee_block_volume_attachment` plus Cloud/GPU `*_vm_volume_attachment` | Standalone volume create/read/grow/delete implemented from current service contracts. Public volume listing works; public plan catalog returned 404. VM attachment projection and safe detach remain required. |
| VPC | `ibee_vpc`, `ibee_vpc_subnet`, `ibee_vpc_node_attachment`, `ibee_nat_gateway`, `ibee_nat_port_forwarding_rule` | Authoritative network quotas and any NAT pricing remain backend responsibilities. |
| Reserved IPs | `ibee_reserved_ip`, `ibee_reserved_ip_attachment` | Site availability and charges enforced by service; account admission check has no public quote. |
| Load balancers | `ibee_load_balancer_l4`, `ibee_load_balancer_l7` | Readable name/protocol/backends and L7 rules only. Custom-domain, routing-mode and TLS settings are omitted where GET cannot round-trip them. |
| DNS | Blocked | Portal coming-soon route; no public zone/record contract. |
| CDN | `ibee_cdn_distribution`, `ibee_cdn_origin`, `ibee_cdn_website`, `ibee_cdn_domain` | Distribution and website lifecycle, URL purge and actual SPA HTTPS delivery verified live. Custom-origin route returned 404 in development; SPA requires an existing index object, and custom domains require DNS validation. Purge and domain verification use explicit Terraform actions (1.14+); metrics are observational, not a managed resource. |
| Firewalls | `ibee_firewall_group`, `ibee_firewall_rule`, `ibee_firewall_attachment` | Group configuration replaces where no update API exists. |
| SSL certificates | Blocked | No public certificate issue/import/read/delete contract. |
| Backups | Cloud/GPU `*_vm_backup_policy` | Destroy disables scheduling; existing backups are retained. One-off restore is outside resource lifecycle. |
| Snapshots | Cloud/GPU `*_vm_snapshot` | Immutable snapshots with creation polling and API-confirmed deletion. Restore is an explicit operation. |
| ISOs | Blocked | Image discovery only; no public upload/management contract. |
| SSH keys | Existing key IDs on VMs | No public standalone key CRUD. |
| Email service | Blocked | No public control-plane domain/webhook/limit lifecycle. |
| Secret manager | `ibee_secret_store`, `ibee_secret` | Write-only values; soft delete/archive semantics, retained names/history. Native recovery and permanent-removal endpoints exist, but the provider deliberately does not invoke them as ordinary destroy operations; separate recovery/destructive actions remain unimplemented. |
| Container registry | Blocked | Registry service exists, but no public token-authenticated provisioning/plan/credential contract in reviewed spec. |

## Deployed blockers found during live tests

Current production evidence is in [CROSS_CLIENT_RESULTS.md](CROSS_CLIENT_RESULTS.md): 15/34 resource lifecycle passes, 4 blocked and 15 not production lifecycle tested. Cloud VM canonical readback is repaired. GPU and block writes returned empty HTTP 403; snapshots/backups require billing catalogs unavailable through the public API; S3 TLS remains blocked. SDK/CLI coverage is separately tracked in [CLIENT_PARITY_AUDIT.md](CLIENT_PARITY_AUDIT.md).

The current development pass exposed missing cloud-image backing resources, gateway GPU admission using monthly rather than selected hourly terms, exhausted public IPv4 capacity, block catalog/site/size disagreement, and S3 AccessDenied for a correctly scoped generated key. Missing custom-origin routes and unavailable load-balancer quotes also limit testing. Exact evidence and cleanup are linked from [LIVE_PRODUCT_VALIDATION.md](LIVE_PRODUCT_VALIDATION.md).

Only INR pricing was exercised live. Reviewed catalog code can stamp a requested currency without verifying the underlying price currency, so non-INR pricing needs backend validation; the provider's response-currency consistency check alone cannot establish the actual denomination.

## Required API alignment before live validation

The implementation is based on both the published contract and actual service source. The public OpenAPI under-documents some canonical compute fields. The provider fails on incomplete responses instead of inventing state:

- Compute plans must expose the trusted `billing_catalog` used by the VM create request, an active/selectable plan, CPU/memory/disk shape, SKU code, and the selected interval price. VM creates select an advertised `HOURLY` option by default, or an explicit `MONTHLY` commitment, in the organization currency returned by billing. The exact selected term is sent in `billing_catalog`; refresh/import require its canonical billing interval. The backend must price additional components and reserve capacity/funds as needed.
- Cloud/GPU VM GET must return its stable ID (`id`, `_id`, or `vm_id` as supported in the implementation), name, plan/site/template identifiers, placement/network state, SSH key IDs, tags, and the configuration fields needed for refresh/import. Confirm actual wire names against `internal/provider/cloud_vm_resource.go`.
- Volume attachments require the VM `data_volumes` projection, including volume identity and attachment mode. A missing projection is an error, not proof that the attachment disappeared.
- Public billing responses must include `organization_id`, a Boolean `allowed`, `reason`, `billing_mode`, `billing_state`, `currency`, and RFC3339 `evaluated_at`. When a SKU was requested, the response must identify the same SKU. The optional configured organization must match.
- Operation polling requires a recognized terminal result. Product creates must return recoverable identities. Product APIs must enforce scopes, tenant ownership, quotas, pricing and billing at mutation time; a provider preflight does not provide concurrency protection.
- Bucket reads must return canonical region/public-access/object-lock fields. Empty-bucket protection additionally requires trustworthy object-count and byte-count values.

## Billing scope

`ibee_billing_eligibility` exposes the supported public read-only admission query. It does not expose wallet mutation, payment processing, credit-limit administration, or purchase reservation. Missing public operation-specific admission and product quote endpoints prevent full portal billing parity. See the assessment for the exact backend work packages, including safe payment-attempt idempotency and confirmation.

## Validation boundary

Unit tests and local real-Terraform tests use mock HTTP contracts. They do not prove current production/dev deployments expose those fields, enforce all billing modes, have capacity, or complete async operations. Partial development live checks and direct portal comparisons are recorded in [VALIDATION.md](VALIDATION.md). Code is pushed to GitHub. Payment workflows, Registry installation, release signing, and live lifecycle tests for remaining products are still unverified.
