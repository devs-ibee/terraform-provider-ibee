# Implemented coverage and portal dependencies

This is the implementation status after the 2026-09-27 source review. The earlier [portal assessment](PORTAL_PARITY_ASSESSMENT.md) records the baseline and service-side requirements. All 19 portal categories remain in scope; navigation visibility does not establish a supported public API.

| Portal category | Provider implementation | Remaining dependency or boundary |
| --- | --- | --- |
| Cloud VMs | `ibee_cloud_vm` | All configuration changes replace. Catalog and canonical GET requirements below must be present in the target deployment. |
| GPU VMs | `ibee_gpu_vm` | Same requirements as cloud VMs; real GPU capacity is unverified. |
| Bare metal | Blocked | Reviewed portal handler does not provision; no public lifecycle contract. |
| Object storage | `ibee_bucket` | No retention lifecycle or object data management. S3 credentials deferred because GET omits creation scope and secret retrieval semantics. |
| Block storage | Cloud/GPU `*_vm_volume_attachment` | Existing volumes only. No public standalone volume CRUD/catalog. VM GET must expose attachment projection. |
| VPC | `ibee_vpc`, `ibee_vpc_subnet`, `ibee_vpc_node_attachment`, `ibee_nat_gateway`, `ibee_nat_port_forwarding_rule` | Authoritative network quotas and any NAT pricing remain backend responsibilities. |
| Reserved IPs | `ibee_reserved_ip`, `ibee_reserved_ip_attachment` | Site availability and charges enforced by service; account admission check has no public quote. |
| Load balancers | `ibee_load_balancer_l4`, `ibee_load_balancer_l7` | Readable name/protocol/backends and L7 rules only. Custom-domain, routing-mode and TLS settings are omitted where GET cannot round-trip them. |
| DNS | Blocked | Portal coming-soon route; no public zone/record contract. |
| CDN | Blocked | No public distribution/domain lifecycle. |
| Firewalls | `ibee_firewall_group`, `ibee_firewall_rule`, `ibee_firewall_attachment` | Group configuration replaces where no update API exists. |
| SSL certificates | Blocked | No public certificate issue/import/read/delete contract. |
| Backups | Cloud/GPU `*_vm_backup_policy` | Destroy disables scheduling; existing backups are retained. One-off restore is outside resource lifecycle. |
| Snapshots | Cloud/GPU `*_vm_snapshot` | Immutable snapshots with creation polling and API-confirmed deletion. Restore is an explicit operation. |
| ISOs | Blocked | Image discovery only; no public upload/management contract. |
| SSH keys | Existing key IDs on VMs | No public standalone key CRUD. |
| Email service | Blocked | No public control-plane domain/webhook/limit lifecycle. |
| Secret manager | `ibee_secret_store`, `ibee_secret` | Write-only values; soft delete/archive semantics, retained names/history. No permanent erase or unarchive API. |
| Container registry | Blocked | Registry service exists, but no public token-authenticated provisioning/plan/credential contract in reviewed spec. |

## Required API alignment before live validation

The implementation is based on both the published contract and actual service source. The public OpenAPI under-documents some canonical compute fields. The provider fails on incomplete responses instead of inventing state:

- Compute plans must expose the trusted `billing_catalog` used by the VM create request, an active/selectable plan, CPU/memory/disk shape, SKU code, and the selected interval price. VM creates explicitly request the monthly catalog in the organization currency returned by billing; discovery can also request hourly prices. The backend must price additional components and reserve capacity/funds as needed.
- Cloud/GPU VM GET must return its stable ID (`id`, `_id`, or `vm_id` as supported in the implementation), name, plan/site/template identifiers, placement/network state, SSH key IDs, tags, and the configuration fields needed for refresh/import. Confirm actual wire names against `internal/provider/cloud_vm_resource.go`.
- Volume attachments require the VM `data_volumes` projection, including volume identity and attachment mode. A missing projection is an error, not proof that the attachment disappeared.
- Public billing responses must include `organization_id`, a Boolean `allowed`, `reason`, `billing_mode`, `billing_state`, `currency`, and RFC3339 `evaluated_at`. When a SKU was requested, the response must identify the same SKU. The optional configured organization must match.
- Operation polling requires a recognized terminal result. Product creates must return recoverable identities. Product APIs must enforce scopes, tenant ownership, quotas, pricing and billing at mutation time; a provider preflight does not provide concurrency protection.
- Bucket reads must return canonical region/public-access/object-lock fields. Empty-bucket protection additionally requires trustworthy object-count and byte-count values.

## Billing scope

`ibee_billing_eligibility` exposes the supported public read-only admission query. It does not expose wallet mutation, payment processing, credit-limit administration, or purchase reservation. Missing public operation-specific admission and product quote endpoints prevent full portal billing parity. See the assessment for the exact backend work packages, including safe payment-attempt idempotency and confirmation.

## Validation boundary

Unit tests and local real-Terraform tests use mock HTTP contracts. They do not prove current production/dev deployments expose those fields, enforce all billing modes, have capacity, or complete async operations. Live integration, payment workflows, Registry installation and release signing are deferred until explicitly scheduled after code review. No code has been pushed and no real resources or payments have been created by these tests.
