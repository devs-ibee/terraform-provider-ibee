# Production cross-client validation — 2026-09-28

**Release decision: full portal/Terraform/SDK/CLI parity is not ready.** This pass repaired and live-verified the Cloud VM lifecycle, exercised interoperability for a bucket and private VPC, and found additional public API and client-contract gaps. It used the user-authorized production workspace and ₹2,000 test ceiling. No backend deployment, main-branch merge, package release, payment, or credit purchase was performed.

## Current production coverage

These are resource-type counts, not a claim that every operation or every client passed. They combine the first [production pass](LIVE_PRODUCTION_RESULTS.md) with this follow-up.

| Outcome | Resource types | Evidence / boundary |
| --- | ---: | --- |
| Lifecycle passed | **15 / 34** | VPC, subnet, firewall group/rule, bucket, CORS, lifecycle, notifications, secret store/secret, S3 credential control plane, CDN distribution/website, reserved IP, Cloud VM. S3 data access remains blocked; notification delivery, scheduled expiration and secret recovery were not exercised. |
| Attempted but blocked | **4 / 34** | GPU VM and block volume: empty HTTP 403. Cloud snapshot and Cloud backup policy: HTTP 422 requiring `billing_catalog`. |
| Not production lifecycle tested | **15 / 34** | VPC node attachment; NAT and port forwarding; firewall/IP attachments; L4/L7 load balancers; CDN origin/domain; block attachment; cloud/GPU volume attachments; GPU snapshot/backup policy; bucket retention. |

All **5 data-source types** passed the earlier production preflight. **1 of 3 action types** now has a production success: `ibee_vm_power` start. CDN purge and domain verification actions were not exercised in this production pass. Portal-only products without public lifecycle contracts are outside the 34 implemented types, and remain unsupported.

## Cross-client workflows

| Workflow | Checks and result |
| --- | --- |
| Portal → SDKs/CLI → Terraform bucket | **Passed.** Portal created one empty private bucket in `in-south-1`. Python SDK, TypeScript SDK and CLI read the same name/region/visibility. Terraform imported it and produced no changes. TypeScript changed visibility on the empty bucket; Terraform detected exactly one update and restored private access. Portal settings showed Public Access Disabled and zero objects. Terraform destroy completed; independent Python SDK GET returned 404. The portal inventory needed a reload before showing the newly created bucket. |
| Python SDK → CLI → TypeScript/portal VPC | **Passed.** Python created one private VPC with automatic CIDR and no default subnet/NAT/nodes. CLI read and renamed it; Python, TypeScript and the portal confirmed the same updated name/description, CIDR, private connectivity and active state. CLI deleted it; independent Python GET returned 404 and the final VPC inventory was empty. |
| Terraform → SDKs/CLI/portal Cloud VM | **Passed for the tested lifecycle.** Terraform created the selectable 2-vCPU/4-GB hourly plan quoted at ₹2.47/hour, then refreshed into a no-change plan. Python/TypeScript and portal agreed on identity, sizing, site and status. CLI stop reached a stopped VM; fixed CLI stop `--wait` completed on `succeeded`. Terraform native start and TypeScript start both completed successfully. Import into a separate temporary state produced no changes. Portal restart returned to Running. Terraform destroy succeeded and independent Python GET returned 404. No SSH/application workload, VM resizing, disk attachment, restore or guest filesystem test was performed. |

SDK/CLI provisioning of VMs was not certified: their typed create/delete interfaces still omit the selected billing catalog/term and public-IP release choice. Reading or powering a Terraform-created VM does not prove equivalent VM purchasing behavior.

## Fixes verified

- **Terraform canonical hourly readback:** accept an absent/null commitment period only when the returned term is explicitly HOURLY and uncommitted, with valid price/unit/duration fields. Purchase validation and monthly commitment checks remain strict. The production create/read/import/no-change/destroy sequence now works without manual deletion.
- **Terraform discovery:** default to HOURLY to match new VM resources; explicit MONTHLY queries remain supported. Creation already forwarded the selected interval correctly. This corrects the earlier GPU catalog interpretation: the default monthly catalog was unpriced, while all three hourly plans are selectable and priced.
- **CLI output and waiting:** handle bare VM arrays, preserve nested SDK models as JSON objects, use `secret_name`, recognize successful/failed terminal aliases, return nonzero on timeout/failure, and keep `--json` stdout parseable. Production VM JSON listing and stop waiting passed after the patch. Before the patch, a successful stop waited 300 seconds and printed “still succeeded”.

The CLI patch was applied to its existing local checkout while preserving pre-existing edits. It has not been included in a CLI release. Python and TypeScript SDK source was not changed by this pass.

## Newly confirmed blockers

1. **GPU authorization/admission:** the selected site's hourly RTX5000 plan was selectable and priced at ₹30.14/hour. Provider admission passed, but VM POST returned empty HTTP 403. GPU inventory afterward contained no matching test VM. The cause is not established; verify exact token scopes and correlate gateway/admission logs. The portal displays permission-family badges, which do not distinguish read/write scopes. Do not label this a capacity failure or an unpriced plan.
2. **Snapshot and backup billing contracts:** both public writes returned HTTP 422 because `billing_catalog` is required. The portal resolves `snapshot_storage` / `backup_storage` from its authenticated billing catalog and forwards the selected catalog in snapshot create, backup enable and backup update. Public-token GETs to `/v1/billing/catalog/products/{product}/plans` returned 404 for both products. Publish an authoritative public catalog, then implement selection, forwarding and SKU-specific eligibility. No catalog IDs or prices were invented. Snapshot inventory remained empty and backup policy remained disabled.
3. **Block storage:** earlier create HTTP 403 and missing public catalog remain unresolved. Source review also found placement-ID versus billing-site-code comparison and missing requested-SKU filtering in admission; those are follow-up fixes, not proven explanations for the empty 403.
4. **S3 data plane:** the documented workspace S3 hostname previously failed TLS handshake. Credential creation and native bucket APIs do not establish S3 readiness. Fix DNS/ingress/certificate readiness, then rerun scoped upload/list/download/delete and revocation. TLS validation must remain enabled.
5. **Client feature gaps:** at least 16 provider resource types lack dedicated SDK/CLI APIs. Typed compute billing/deletion policy, credential create/revoke semantics, billing denial handling and optional-field omission also need contract updates. See [CLIENT_PARITY_AUDIT.md](CLIENT_PARITY_AUDIT.md).

## Automated validation

| Component | Result |
| --- | --- |
| Provider | Full `IBEE_TF_TEST=1 go test -race ./...` passed: 106 top-level tests, including 7 real Terraform CLI lifecycle suites against mocked APIs. `go vet`, formatting and generated documentation checks passed. |
| Provider compatibility | Compute create/read/import/destroy fixture passed on Terraform 1.15.8 and minimum supported 1.11.4. |
| Python SDK | 19 passed, 3 optional transport tests skipped. |
| TypeScript SDK | Build passed; 20 tests passed. Live-tested distribution hash matched the fresh audit build. |
| CLI | 40 tests passed, including 27 regression cases. Tests use mocks; only the live workflows above establish production behavior. |

## Cleanup and release sequence

The bucket and Cloud VM were destroyed by Terraform; its primary managed state ended empty. Independent SDK reads returned 404. The SDK-created VPC was deleted through CLI and independently confirmed absent. GPU/snapshot/backup attempts left no matching VM, snapshot or enabled policy. No test-created infrastructure remains from this pass. The earlier secret-store archive/history and revoked credential records are intentionally retained service history.

The live portal Activity Logs showed successful bucket create/update/delete, VPC create/update/delete, VM create/start/stop/reboot/delete, and accepted/succeeded async stages. Portal-originated actions carried the signed-in user; API actions still used `Unknown`, `Network service` or `virtualization-engine`. Mutation logging is confirmed, but consistent API-token attribution and logging of rejected gateway calls are not.

This run did not establish settled charges or invoice correctness. The hourly plan quotes and short resource lifetimes were within the authorized test ceiling; the ceiling is not evidence of zero charges.

Before a full production release: deploy the missing public catalog/authorization/TLS corrections, update the shared API specification and regenerate SDKs, implement the missing client operations, then rerun the blocked and untested matrix with owned DNS and quoted disposable networking resources. Billing eligibility, wallet/payment flows, negative tenant/scope/quota tests, application connectivity, backup restore and delayed metering need their own acceptance evidence. Publish only features whose supported behavior and remaining limits are documented.
