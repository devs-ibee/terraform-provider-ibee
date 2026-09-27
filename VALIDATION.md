# Validation results

Validated on 2026-09-27 with Go 1.26.5 on macOS arm64. The automated suites below use isolated local HTTP fixtures. The separate live development checks are documented below. Code has been pushed to GitHub; no release or payment was initiated.

| Check | Result |
| --- | --- |
| Full Go suite with race detector and real Terraform fixtures | 55 top-level tests passed |
| Terraform 1.15.8 | All three CLI suites passed |
| Terraform 1.11.4 (minimum supported) | All three CLI suites passed |
| Compute CLI lifecycle | All 8 cloud/GPU VM, snapshot, backup-policy and attachment resources |
| Networking CLI lifecycle | All 12 VPC/subnet/attachment, firewall, NAT, reserved-IP and load-balancer resources |
| Storage/secrets CLI lifecycle | Bucket, secret store, secret; write-only secret excluded from state |
| Data sources | Billing eligibility, compute sites, compute plans and images exercised through mock Terraform; networking sites tested with Go fixtures and live Terraform |
| Lifecycle behavior | Create, no-op plan, import/no-op, supported updates, portal-style drift, and teardown while billing blocks purchases |
| Negative cases | Billing denial/unavailability/malformed decisions, wrong tenant/SKU/estimate/currency, partial operations, authorization failures, missing projections, deletion ownership, secret CAS conflicts |
| Static checks | `go vet`, Go formatting, Terraform formatting, `git diff --check` passed |
| Documentation/examples | 29 schema pages generated; all 4 example root directories validated |
| Packaging configuration | GoReleaser v2.18.2 `check` passed |
| Unsigned snapshot packaging | 8 archives built: Linux, macOS, Windows, FreeBSD × amd64/arm64; archive and manifest checksums verified |

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

## Live development checks — 2026-09-27

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
