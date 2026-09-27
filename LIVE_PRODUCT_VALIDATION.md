# Live product validation and completion gates

Date: 2026-09-27. Target: authorized development API (`api.ibee.co.in`) and portal (`portal.ibee.co.in`), in the same development workspace. No customer IDs, credentials or billing balances are included here.

**This is not a claim that every product or feature is production-ready.** Source-backed implementation, local mock lifecycle tests, read-only live checks and live mutations are separate evidence levels. The original expansion pass was read-only except for networking. The subsequent authorized promotional-credit pass attempted disposable lifecycles; the current results below supersede pending-budget statements in earlier reports. No payments were submitted by Terraform.

## Promotional-credit live pass

| Area | Current outcome |
| --- | --- |
| Bucket, retention, CORS, lifecycle, notifications, S3 credential | Live control-plane lifecycle passed; see LIVE_STORAGE_RESULTS.md for exact operations and cleanup. This does not establish lifecycle expiration or notification delivery. |
| Secret store and write-only secret | Live create/import/rotation/no-change and soft-delete/archive passed. |
| S3 direct access | Scoped object_rw key received HTTP 403 AccessDenied on ListObjectsV2; data-plane round trip blocked. |
| CDN distribution and website | Live create/import/update/no-change/purge and cleanup passed. Actual SPA fallback returned HTTP 200 and exact harmless test HTML through HTTPS. |
| VPC/subnet | Live create/import/rename/DNS update/prefix replacement/no-change and cleanup passed. Explicit private-only create/import/update/cleanup also passed. Corrected compound NAT remains mock-tested, not live-applied; see LIVE_NETWORK_RESULTS.md. |
| Cloud VM | Two accepted creates failed on missing Ubuntu/Debian backing image definitions. Both failed VMs were deleted. |
| GPU VM | Explicit hourly precheck passed; create rejected by gateway monthly-based admission. No GPU accepted. |
| Reserved IP | Creation rejected because site public IPv4 pool was exhausted; no reservation retained. |
| Block storage | Public catalog discovery still404. After portal price and source-backed SKU review, 10 GB billing precheck passed; create returned HTTP400 because no selectable plan matched. No volume allocated. |
| Load balancers | Live mutations not run because an authoritative test price could not be established. |
| Remaining VM-dependent features | Blocked by unavailable disposable compute; mock tests do not substitute for live validation. |

Provider corrections from this pass: explicit compute billing-term selection and VPC connectivity/compound-NAT ownership handling. See the individual reports for deployed backend blockers and test boundaries. No existing customer resources were changed. Final billing settlement is not established by a wallet snapshot.

Reports: [compute](LIVE_COMPUTE_RESULTS.md), [networking](LIVE_NETWORK_RESULTS.md), [storage/secrets](LIVE_STORAGE_RESULTS.md), [CDN](LIVE_CDN_RESULTS.md).

## Earlier read-only pass (historical)

| Product/feature | Evidence | What remains |
| --- | --- | --- |
| Billing eligibility | Real Terraform read passed: allowed status-only admission, INR. | Operation-specific quotes, concurrent affordability, denied account modes, funding flow. Status-only admission is not a price quote. |
| Compute discovery | Real Terraform returned 3 compute sites, 13 cloud plans, 6 cloud images, 4 GPU plans, 1 GPU image. | Placement capacity and actual provisioning in a selected priced plan. |
| Cloud/GPU VM inventory | Both public list routes returned HTTP 200 with no VMs in the test workspace. | Live create, ready, import, update/replacement, power, cleanup. |
| VPC and subnet | Previous live pass created both in Amaravati, matched portal IDs/CIDRs, converged, imported and destroyed. API and portal confirmed cleanup. | New DNS/prefix/name features and child-ownership guard live validation. |
| Firewall group/rule | Previous live pass created an unattached group and disabled rule, updated TCP port, imported and destroyed. Portal hides disabled rules intentionally. | Attached-VM traffic behavior; investigate initial create transport reset. |
| Reserved IP/load balancer | Public lists returned HTTP 200 with no resources. | Paid reservation, attachment/move, L4/L7 routing, TLS and release. |
| Standalone block volumes | Public volume list returned HTTP 200 with no volumes. | Create, attach, grow, detach and delete against real storage and VM capacity. |
| Block catalog | `/block-storage/plans` and `/catalog/products/block_storage/plans` returned HTTP 404. | Deploy/reconcile the catalog route; use an authoritative operator-selected SKU until then. |
| Object-storage bucket | Existing bucket imported into separate read-only Terraform state; plan returned no changes. Portal and API showed matching bucket inventory. Existing bucket was not modified. | Disposable-bucket create/update/delete and S3 data-plane tests. |
| Bucket lifecycle/CORS/notifications | Each configuration GET returned HTTP 200 with an empty collection on the inspected bucket. | Live PUT/readback/update/delete and separate expiration/CORS/event-delivery tests. No live lifecycle rules or webhooks were configured. |
| Bucket retention | Routed endpoint returned a typed HTTP 404 explaining Object Lock was not enabled on the inspected bucket. | Test against a newly created lock-enabled bucket; clearing a default remains unsupported. |
| S3 credentials | Native source exposes permission and bucket-scope metadata; provider and tests added. | Live least-privilege access/revocation and one-time secret handling. No live credentials created. |
| Secret manager | Public store listing returned HTTP 200; existing store metadata imported and produced a no-change Terraform plan without reading secret values. | Create/read/rotate/CAS/soft-delete/archive on a disposable store; history remains after deletion. |
| CDN distribution | Existing live distribution imported and produced no changes. Portal matched origin, cache policy and active status. Existing distribution was not modified. | Disposable create, enable/disable, cache-policy updates, content delivery and delete. |
| CDN SPA/custom domains | Website-config and custom-domain list GETs returned HTTP 200; portal exposes SPA and domain controls. | Existing index object, DNS ownership and TLS verification, disable/remove. |
| CDN custom origins | `/cdn/origins` returned HTTP 404 with an empty response. | Reconcile/deploy public route before a live custom-origin test. |
| CDN purge/verification | Explicit native Terraform actions implemented; local fixtures verify requests and response handling. | Invoke only on disposable delivery content/domain; no existing CDN cache purged. |
| Registry/email/SSH keys/ISO | Source audit identifies private/session API and/or incomplete lifecycle boundaries. | Public-token routes, scope/tenant enforcement and the concrete gaps in ALL_PRODUCTS_API_GAPS.md. |
| DNS/bare metal/standalone certificates | Source audit found coming-soon or non-provisioning UI paths. CDN managed TLS is distinct from standalone certificates. | Real product lifecycle APIs before truthful Terraform implementation or tests are possible. |

## Required live sequence for every implemented resource

1. Select a disposable target, authoritative plan/site and approved spending ceiling.
2. Review Terraform plan; create and wait for terminal status, preserving recovery IDs on errors.
3. Compare canonical settings with the live portal and public API; check actual product behavior separately (traffic, storage I/O, TLS or notifications).
4. Require a no-change plan; import into isolated state and compare again.
5. Exercise supported updates and portal-side drift; validate denied billing/scopes and failed-operation recovery in controlled cases.
6. Destroy only test-owned resources; verify absence or documented retained/archived status, and confirm billing cleanup.

Local fixture tests cover many failure cases that should not be induced on existing customer resources. They cannot prove hardware capacity, real data-plane behavior or all billing enforcement modes. Detailed source boundaries are in ALL_PRODUCTS_API_GAPS.md; networking and storage feature matrices enumerate individual controls.
