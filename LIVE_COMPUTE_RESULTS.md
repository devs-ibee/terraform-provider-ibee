# Live compute and block-storage validation

Date: 2026-09-27. Target: authorized development public API at `api.ibee.co.in`, using an isolated test workspace and the approved development token. Compute test allocation: at most ₹1,000 incremental spend. All Terraform configurations, state, binaries and detailed logs stayed under a private temporary directory; no credentials or customer identifiers are recorded here. No existing resources were changed.

## Result

The live tests exercised billing/catalog discovery, real Terraform provisioning failure recovery, canonical VM reads and deletion. **Cloud/GPU successful provisioning, guest behavior, no-change/import after successful provisioning, power, snapshots, backups and volume attachments did not pass live validation because backend prerequisites blocked them.** Those capabilities still have local fixture coverage, which must not be represented as live success.

Final authenticated inventories returned HTTP 200 with zero cloud VMs, zero GPU VMs and zero standalone block volumes. Both failed disposable cloud VMs were deleted through Terraform; no compute/block resource was retained. No snapshot, backup, block volume, attachment or payment was created.

| Test | Outcome | Evidence and cleanup |
| --- | --- | --- |
| Billing account read | Passed | INR, PREPAID, CURRENT; status-only decision allowed. |
| Compute discovery | Passed | Three sites, 13 cloud plans, six cloud images, four GPU plans, one GPU image. Site-filtered catalog reads passed for Amaravati. |
| Cloud SKU admission | Passed | Smallest plan `PERFORMA-1-2-50`: ₹0.82/hour and ₹600 monthly display price. Provider's original ₹600 estimate returned allowed. |
| Cloud Ubuntu create | Failed in backend | Terraform validate/plan passed; create returned an operation/VM ID. Operation failed because backing resource definition `img-ubuntu-2404-base-50g` did not exist during image snapshot restore. |
| Failed Ubuntu read/recovery | Passed | Terraform retained the VM identity. Canonical GET returned status `error`, expected site/plan/template/SSH/tags, no public IP and no root volume. `billing_started_at` was null. |
| Failed Ubuntu destroy | Passed | Terraform destroy completed in 35 seconds; subsequent GET returned 404. |
| One Debian fallback | Failed in backend | Same smallest plan and site. The advertised Debian image failed because `img-debian-12-base-50g` did not exist during image snapshot restore. No further cloud image attempts were made. |
| Failed Debian destroy | Passed | Terraform refreshed the failed resource and deleted it in 32 seconds. Final cloud list was empty. |
| GPU original monthly estimate | Correctly denied | The smallest RTX5000 plan advertises ₹35/hour, with only an uncommitted HOURLY billing option, but a ₹22,000 monthly display amount. A read-only ₹22,000 estimate was denied `insufficient_balance`; no create was sent at that point. |
| Explicit hourly GPU precheck | Passed after provider fix | Catalog-selected HOURLY option, `committed=false`, authoritative 3,500 minor units. Fresh SKU estimate returned allowed. |
| Explicit hourly GPU create | Rejected by gateway | POST returned HTTP 402 `billing_denied`, reason `insufficient_balance`. No operation/VM was accepted. Final GPU inventory was empty. |
| Block catalog discovery | Deployment blocker | Both `/v1/block-storage/plans` and `/v1/catalog/products/block_storage/plans` returned HTTP 404. Public-token catalog discovery remains unavailable. |
| Block quote / SKU review | Passed with portal/source evidence | Parent task inspected live Amaravati NVMe SSD pricing: ₹7/GB/month, 10 GB minimum, 2 TB maximum. Production billing SKU mapping identifies `BLOCKSTO-NVME`; a saved catalog snapshot corroborates NVMe identity and 700 minor units/GB/month. |
| 10 GB block precheck and create | Precheck passed; create rejected | Exact ₹70 monthly estimate returned allowed; Terraform plan passed. POST returned HTTP 400 `No selectable Block Storage plan matches the request`. No accepted volume or operation ID was returned. No 11 GB expansion was attempted. |
| Block rejection reconciliation | Passed | Follow-up volume inventory returned HTTP 200 and zero entries; Terraform block state contains zero resources. No cleanup mutation was necessary. |
| Standalone volume list | Passed | `/v1/block-storage/volumes` returned HTTP 200 and an empty array. |

## Provider issue found and corrected

The provider originally used the catalog's monthly display amount for every VM purchase and copied the unselected `billing_catalog`. This differs from the portal's explicit term selection and unnecessarily denied an hourly-only GPU plan.

Cloud/GPU resources now expose `billing_interval`. Omission selects `HOURLY` for a new VM and preserves the canonical term of an existing or imported VM. Creation selects the exact advertised billing option and copies its `billing_interval`, `committed`, `commitment_period`, unit price and available commitment fields, matching portal `app/utils/computeCatalog.ts:251` (`billingCatalogForTerm`). A monthly term requires an advertised one-month commitment; admission uses the authoritative period cost, including committed hours when the catalog rate is hourly. The provider refuses missing, conflicting, unsupported or unpriced options.

Canonical reads/imports validate the selected `billing_catalog` interval, commitment flag, period, duration and pricing shape; unsupported three-/six-month commitments and committed hourly records are rejected without overwriting state. An old VM containing only an unselected list of billing options does not establish contractual intent, so the provider reports that contract gap instead of guessing a term. Changes to the selected term require replacement; deleting a VM does not cancel a commitment.

Validation after the fix:

- Focused billing, compute-resource and VM power tests passed.
- The real Terraform CLI compute fixture lifecycle passed with hourly selection, canonical read and cleanup. It also imported monthly cloud/GPU VMs with the term omitted and confirmed no-change plans; explicitly configuring HOURLY then required replacement for both VMs without applying it.
- Tests cover hourly and monthly pricing, commitment estimates, malformed/duplicate terms, currency checks, imported term hydration and term drift. Ten malformed canonical commitment shapes fail safely, including three-/six-month terms and committed hourly records.

No Terraform/provider mechanism bypassed the gateway billing rejection.

Non-INR behavior remains unverified against the live backend. The public catalog source accepts a currency selector and the provider requests the organization currency and rejects mismatched catalog/admission responses; fixture tests exercise USD. However, the reviewed gateway compute admission source hardcodes INR, so a successful non-INR end-to-end purchase cannot be claimed until the backend pricing and admission paths are aligned and tested.

## Backend corrections required before a rerun

1. **Repair cloud image backing resources or withdraw unavailable images.** The public catalog advertised selectable Ubuntu and Debian templates whose backing Linstor resource definitions were missing. Verify each template's source resource/snapshot in the placement site's storage backend before exposing it.
2. **Align gateway compute admission with selected billing terms and organization currency.** Reviewed `auth-service/app/services/billing_admission_service.py:358` hardcodes `billing_interval=MONTHLY` (and `currency=INR`) while resolving the plan. `_compute_plan_price` at line 596 therefore checks `monthly_price_minor`, ignoring the request's explicit trusted hourly selection. This explains the observed allowed ₹35 precheck followed by a denied GPU create. The gateway must validate the selected term against authoritative catalog options and use that term's amount; it must never trust caller prices.
3. **Align block catalog selection with the live portal and publish discovery.** Despite the live portal showing a 10 GB Amaravati NVMe option and a successful SKU affordability check, create rejected the selection. Source `portal-client-api/app/routes/blockstorage_proxy.py:116` passes the raw site ID directly as the catalog filter, while normal billing proxy catalog lookup expands site IDs to aliases before matching product site codes; this is a source-supported possible cause, not proof of the deployed failure's exact cause. The historical saved catalog snapshot also has 50 GB stepped sizes while the current UI permits 10 GB; reconcile live authoritative allowed sizes. No alternate size, site alias or invented SKU was tried to make the request pass. Expose public SKU/site/size/currency/pricing discovery so Terraform need not depend on a separate portal quote.
4. Rerun ready-state create → no-change → import → power/update → snapshot/backup where safely removable → detach/destroy only after these dependencies are corrected.

## Cost and residual risk

The cloud failures happened during root-image restoration, before successful VM startup. The inspected failed Ubuntu record had no billing start timestamp or root/public-IP allocation. GPU and block creation were rejected before acceptance. The reviewed block attempt was ₹70/month (10 GB); planned but unexecuted expansion would have been ₹77/month (11 GB). No usable compute runtime or standalone storage was produced. **Expected runtime charge is zero, but a final ledger charge was not independently verified by this agent**; confirm the billing ledger for failed provisioning/admission events rather than treating catalog estimates as actual spend.

No monthly GPU commitment was purchased. No credits were added, payment instrument used, SSH private key created, guest password disclosed, guest login attempted, or public application service installed. Temporary state and logs are retained locally for the parent task's review and contain only test-scoped operational metadata; they are not intended for publication.
