# Local validation results

Validated on 2026-09-27 with Go 1.26.5 on macOS arm64. All requests used isolated local HTTP fixtures. No IBEE workspace was contacted, no payment was initiated, and no code or release was pushed.

| Check | Result |
| --- | --- |
| Full Go suite with race detector and real Terraform fixtures | 53 top-level tests passed |
| Terraform 1.15.8 | All three CLI suites passed |
| Terraform 1.11.4 (minimum supported) | All three CLI suites passed |
| Compute CLI lifecycle | All 8 cloud/GPU VM, snapshot, backup-policy and attachment resources |
| Networking CLI lifecycle | All 12 VPC/subnet/attachment, firewall, NAT, reserved-IP and load-balancer resources |
| Storage/secrets CLI lifecycle | Bucket, secret store, secret; write-only secret excluded from state |
| Data sources | Billing eligibility, sites, compute plans and images exercised through Terraform |
| Lifecycle behavior | Create, no-op plan, import/no-op, supported updates, portal-style drift, and teardown while billing blocks purchases |
| Negative cases | Billing denial/unavailability/malformed decisions, wrong tenant/SKU/estimate/currency, partial operations, authorization failures, missing projections, deletion ownership, secret CAS conflicts |
| Static checks | `go vet`, Go formatting, Terraform formatting, `git diff --check` passed |
| Documentation/examples | 28 schema pages generated; all 4 example root directories validated |
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

Live gateway/product compatibility, actual provisioning capacity, all portal billing/enforcement modes, payment workflows, GPG signing, and clean installation from a published Terraform Registry release. Those require the next development-workspace validation phase and the missing APIs described in [COVERAGE.md](COVERAGE.md). Mock lifecycle success is not certification of deployed service behavior.
