# Development validation and release

Code and mock tests are prepared. Branch publication and CI testing are authorized; live provisioning requires the selected development workspace and test budget. Registry publication remains a separate release step.

## 1. Review the local implementation

- Run `make check`, `make test`, and `make test-terraform` with Go and Terraform 1.11+ installed.
- Review `COVERAGE.md` against the public gateway and exact product-service deployment revisions. Align under-documented compute projections before trying live imports.
- Review deletion defaults, backup retention, soft-deleted secret names, and any migration from an existing prototype state. Back up existing Terraform state before changing provider versions.
- Confirm the Registry namespace (`devs-ibee/ibee` is retained), repository ownership, licensing, and intended first version.

## 2. Validate in an isolated development workspace

Choose a workspace with an explicitly approved spend limit and scoped token. Inject credentials locally; never commit them. Use `IBEE_ENV=dev`, `IBEE_WORKSPACE_ID`, and optionally `IBEE_ORGANIZATION_ID`.

For each of the 23 resources: create, read, no-op second plan, import, supported update or replacement, portal-originated drift, failed/partial creation, and destroy. Check both standalone and combined examples with the actual site, image, plan, region, VM, and volume IDs. Confirm retained assets and remove any billable remnants deliberately.

Test prepaid initial top-up, insufficient funds, eligible balance/promo, postpaid headroom/limit, overdue restrictions, missing/revoked scopes, cross-workspace access, quotas, async failure and cancellation. Verify create denial never provisions and permitted cleanup still works. Verify server-side concurrent admission across Terraform and portal requests. Payment flows remain portal workflows until their public API is delivered.

For secrets, inspect plan and state for plaintext using a disposable value. Verify compare-and-set conflicts, soft-delete/archive behavior and name retention. For buckets, validate recursive-delete guards while writers are stopped.

## 3. Prepare publication only after validation

- Confirm a public repository named `terraform-provider-ibee` and access to the chosen Registry organization.
- Configure the GitHub `release` environment with required reviewers, `GPG_PRIVATE_KEY` and `PASSPHRASE`. Register its public signing key in the Terraform Registry.
- Install GoReleaser v2 locally to validate `.goreleaser.yml` with `goreleaser check`, then test unsigned packaging with `goreleaser release --snapshot --clean --skip=publish,sign`.
- Deliberately push the reviewed code and a semantic-version tag only after authorization. The workflow runs checks and produces a signed draft GitHub release.
- Inspect the zip files, manifest, SHA256 checksums and detached signature. Confirm the injected binary version and intended OS/architecture matrix.
- Publish the reviewed release and register/resync the provider in the Registry. Test a clean `terraform init` without a development override.

The GoReleaser config targets Linux, macOS, Windows and FreeBSD on amd64/arm64. The GoReleaser configuration passes `goreleaser check` with v2.18.2; all eight unsigned snapshot archives build and their checksums verify. Signing and a clean Registry installation remain unverified. See [VALIDATION.md](VALIDATION.md) for the completed local checks.
