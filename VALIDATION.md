# Validation results

Validated on 2026-09-27 with Go 1.26.5 on macOS arm64. The automated suites below use isolated local HTTP fixtures. The separate live development checks are documented below. Code has been pushed to GitHub; no release or payment was initiated.

| Check | Result |
| --- | --- |
| Full Go suite with race detector and real Terraform fixtures | 103 top-level tests passed |
| Terraform 1.15.8 | All seven CLI suites passed |
| Terraform 1.11.4 (minimum supported) | All seven resource CLI suites passed; native action invocations require 1.14+ |
| Compute CLI lifecycle | All 8 cloud/GPU VM, snapshot, backup-policy and attachment resources |
| Networking CLI lifecycle | All 12 VPC/subnet/attachment, firewall, NAT, reserved-IP and load-balancer resources |
| Storage/secrets CLI lifecycle | Bucket, secret store, secret, CORS, lifecycle, notifications, scoped S3 credentials; write-only secret excluded from state |
| Block-storage CLI lifecycle | Standalone volume create/import/grow and safe node attachment/detach |
| CDN CLI lifecycle | Distribution, origin, SPA website and custom domain; alias resolution, CNAME output, import, update, drift and failure preservation |
| Explicit actions | Real CLI invokes CDN purge and domain verification on Terraform 1.15.8; focused tests cover all cloud/GPU start/stop/reboot paths and failure handling |
| Retention lifecycle | Focused tests cover set/read/import/update, malformed responses and explicit retain-on-destroy guard |
| Data sources | Billing eligibility, compute sites, compute plans and images exercised through mock Terraform; networking sites tested with Go fixtures and live Terraform |
| Lifecycle behavior | Create, no-op plan, import/no-op, supported updates, portal-style drift, and teardown while billing blocks purchases |
| Negative cases | Billing denial/unavailability/malformed decisions, wrong tenant/SKU/estimate/currency, partial operations, authorization failures, missing projections, deletion ownership, secret CAS conflicts |
| Static checks | `go vet`, Go formatting, Terraform formatting, `git diff --check` passed |
| Documentation/examples | 43 schema pages generated; all 8 example roots validated on 1.15.8 (7 resource roots on 1.11.4) |
| Packaging configuration | GoReleaser v2.18.2 `check` passed |
| Unsigned snapshot packaging | Baseline 23-resource build: 8 archives built (Linux, macOS, Windows, FreeBSD × amd64/arm64), checksums verified. Expanded build has not been repackaged or published. |

## Reproduce

```sh
IBEE_TF_TEST=1 go test -race -count=1 -v ./...
go vet ./...
terraform fmt -check -recursive examples
python3 scripts/generate_docs.py --check
goreleaser check
goreleaser release --snapshot --clean --skip=publish,sign
```

`IBEE_TF_TEST=1` enables **mock** Terraform CLI tests, not live acceptance tests. Test subprocesses remove user IBEE/TF environment variables and supply fixture-only credentials and loopback endpoints. Each test uses temporary provider builds and Terraform state.

## Still unverified

Live compatibility beyond the checks below, compute/GPU provisioning capacity, all portal billing/enforcement modes, payment workflows, GPG signing, and clean installation from a published Terraform Registry release. Those require the next development-workspace validation phase and the missing APIs described in [COVERAGE.md](COVERAGE.md). Mock lifecycle success is not certification of deployed service behavior.

## Initial networking live pass — 2026-09-27 (historical)

Used an authorized temporary development token and isolated Terraform state. Compared the resources directly in `portal.ibee.co.in` within the same workspace. Credentials and tenant identifiers are excluded from this report.

| Check | Result |
| --- | --- |
| Billing/catalog preflight | Allowed status-only admission, INR currency; compute sites, plans and images readable. This is not a purchase-specific quote. |
| Networking catalog | `/networking/sites` reported Amaravati available; compute catalog also includes sites unavailable to VPCs. Added `ibee_network_sites` and corrected the networking example. |
| VPC and subnet | Created successfully in Amaravati; portal and state matched resource IDs, names and CIDRs. Portal showed active, zero attached nodes, and included network pricing. |
| Firewall group | Initial create connection reset; list reconciliation located the created group and import recovered it without duplicate creation. Portal showed the same group and zero linked instances. |
| Disabled firewall rule | Created, refreshed, and updated TCP port 443 to 8443. Portal counted the rule but hid it from its table; reviewed portal code intentionally excludes disabled rules. |
| Convergence and import | Full plan returned no changes. All four resources imported into independent state and again produced no changes. |
| Cleanup | Terraform destroyed all four disposable resources. API lists confirmed zero VPCs and firewall groups; portal confirmed the VPC was absent. |

The firewall creation transport failure remains a deployed reliability concern; success after reconciliation does not establish that every create request completes reliably. No paid VM/GPU, NAT, load-balancer, reserved-IP, storage or secret lifecycle was exercised in this live pass. No credits were added or payments submitted. Remaining public-API gaps are listed in COVERAGE.md.


## Initial read-only expansion checks (historical)

The expanded implementation has 34 resources, 5 data sources and 3 explicit actions. See [LIVE_PRODUCT_VALIDATION.md](LIVE_PRODUCT_VALIDATION.md) for a product-by-product evidence table. Existing bucket, CDN distribution and secret-store metadata each imported into isolated local state and produced no-change plans. They were not updated or deleted. Read-only live checks also reached bucket CORS/lifecycle/notification and CDN SPA/custom-domain routes.

The later promotional-credit live pass is documented in LIVE_PRODUCT_VALIDATION.md and the four LIVE_*_RESULTS.md reports; it supersedes the earlier pending-budget status. Versioning, in-place VM resize/rebuild/restore, some networking topology choices and several portal products still have implementation or public-contract gaps; see [ALL_PRODUCTS_API_GAPS.md](ALL_PRODUCTS_API_GAPS.md), [NETWORK_FEATURE_MATRIX.md](NETWORK_FEATURE_MATRIX.md), and [STORAGE_FEATURE_MATRIX.md](STORAGE_FEATURE_MATRIX.md). This expansion is not certification of every portal feature.

## Subsequent promotional-credit live pass

Disposable storage, secrets, VPC/subnet and CDN control-plane lifecycles ran against the actual development API. CDN SPA delivery returned HTTP 200 with the exact uploaded test marker. Cloud image backing definitions, GPU admission term handling, public IPv4 capacity and direct S3 credential access blocked other tests. Those failures are not hidden by mock-suite success. Resource cleanup and intentional secret history/archive retention are documented per product.

The pass also corrected explicit VM billing-term selection and VPC connectivity/compound-NAT ownership. See [LIVE_PRODUCT_VALIDATION.md](LIVE_PRODUCT_VALIDATION.md) for current evidence and [LIVE_COMPUTE_RESULTS.md](LIVE_COMPUTE_RESULTS.md), [LIVE_NETWORK_RESULTS.md](LIVE_NETWORK_RESULTS.md), [LIVE_STORAGE_RESULTS.md](LIVE_STORAGE_RESULTS.md), [LIVE_CDN_RESULTS.md](LIVE_CDN_RESULTS.md) for exact operations.
