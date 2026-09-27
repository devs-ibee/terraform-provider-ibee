# IBEE Terraform provider: assessment and release plan

> Baseline assessment before implementation. For current code coverage and remaining dependencies, see [COVERAGE.md](COVERAGE.md).

Reviewed 2026-09-27. This is an implementation proposal based on the local provider, SDK/CLI sources, public API specification, gateway route map, and Latitude's public provider. Local contract readiness does not establish that a route is deployed or behaves identically in development and production.

**Scope update:** the requested target is now parity across the portal's product catalog, including billing eligibility, credits, quotas, and organization restrictions. See [PORTAL_PARITY_ASSESSMENT.md](PORTAL_PARITY_ASSESSMENT.md) for the 19-category matrix and cross-service requirements. The small first release below is a delivery milestone, not the final product scope. Shared billing admission and its public contract alignment must precede further billable resource expansion.

## Recommendation

Continue this Go provider using the Terraform Plugin Framework. Deliver one reliable workflow first: discover a site/plan/image, create a cloud VM with existing SSH key IDs, connect it to a VPC subnet, attach a firewall, and safely import, refresh, update, and destroy the managed resources.

Terraform should use the same public API contracts, tokens, workspace scoping, and infrastructure services as the SDK/CLI and portal. Keep its state and lifecycle logic in this provider. Use Latitude as a reference for the customer experience and engineering practices; adapt resources to IBEE's own contracts.

The Python SDK and TypeScript SDK are generated from the shared Fern OpenAPI definition; the CLI uses the Python SDK. The Terraform provider currently has an independent Go HTTP client. A separate public Go SDK is optional: first make this client typed and tested against the same contracts, then extract or generate a shared Go client when justified. Terraform resource lifecycle code still needs explicit implementation and tests.

## Existing implementation

`internal/provider/provider.go` registers:

| Kind | Names |
| --- | --- |
| Compute resource | `ibee_cloud_vm` |
| Network resources | `ibee_vpc`, `ibee_vpc_subnet`, `ibee_vpc_node_attachment` |
| Firewall resources | `ibee_firewall_group`, `ibee_firewall_rule` |
| Data sources | `ibee_sites`, `ibee_compute_plans`, `ibee_images` |

Existing foundations include sensitive token configuration, `IBEE_TOKEN` and `IBEE_WORKSPACE_ID` fallbacks, endpoint override, workspace query scoping, VM create/delete idempotency headers, operation polling, and some HTTP 404 handling. The provider address is already `registry.terraform.io/devs-ibee/ibee`.

The README describes only two resources and one data source, so it is behind the implementation. No test files, Registry documentation directory, release workflow, GoReleaser configuration, Registry manifest, or license file are currently tracked. This assessment does not verify the provider's Registry publication status.

## Latitude reference

Latitude's current provider uses the Plugin Framework with a Go SDK and includes retry configuration, resource/data-source registration, generated documentation tooling, and release packaging. Its onboarding demonstrates token configuration, HCL resource references, plan/apply, and cleanup. Those are useful patterns for IBEE. Its resource names, project model, billing defaults, and backend behavior should not determine IBEE's schemas.

References:

- [Terraform guide](https://www.latitude.sh/docs/guides/terraform)
- [Provider repository](https://github.com/latitudesh/terraform-provider-latitudesh)
- [Provider configuration and registration](https://github.com/latitudesh/terraform-provider-latitudesh/blob/main/latitudesh/provider.go)
- [Provider dependencies](https://github.com/latitudesh/terraform-provider-latitudesh/blob/main/go.mod)
- [Registry documentation](https://registry.terraform.io/providers/latitudesh/latitudesh/latest/docs) — the page requires JavaScript; source inspection supplied implementation details.

## Fix before release

| Finding | Evidence in this repository | Required behavior |
| --- | --- | --- |
| VPC deletion enumerates and deletes every subnet, including subnets created outside this Terraform state. | `vpc_resource.go`, `Delete` | Define ownership explicitly. Track any default subnet created with the VPC; refuse deletion when other unmanaged children remain. Separate subnet resources should be destroyed through Terraform dependencies. Treat an already absent VPC as successfully deleted. |
| No resources implement import. | All six resource implementations | Add import identifiers and full state hydration. Use parent/child IDs for nested resources. Do not delete or adopt child resources implicitly when importing a parent. |
| Firewall refresh only updates enabled/description. VM refresh only updates name/status/public IP. | `firewall_rule_resource.go`, `Read`; `cloud_vm_resource.go`, `Read` | Refresh every supported API-backed field so Terraform detects portal/SDK changes. If the API cannot return required identity/configuration fields, document and resolve that API gap before claiming reliable import. |
| Rule creation identifies the new rule using only a subset of fields and timestamp ordering. It does not send `enabled`. | `firewall_rule_resource.go`, `matchRule`, `ruleBody`, `Create` | Obtain a deterministic rule ID, including concurrent-create behavior. Honor the enabled setting through the documented API flow. Read canonical state after mutations. |
| Compute polling ignores request errors and reads `error`, while the current API contract exposes `error_code` and `error_message`. It does not handle `cancelled` or `timed_out` as terminal failures. | `helpers.go`, `waitOperation` | Handle documented terminal states, surface useful failures, fail promptly on permanent errors, and use configurable operation timeouts with cancellation. Retain the same idempotency key across retries of one mutation. |
| VM creation silently ignores the final GET failure. | `cloud_vm_resource.go`, `Create` | Preserve the accepted VM identity, report refresh failure, and provide a recoverable state path instead of returning unresolved computed values as success. |
| Attachment decoding can turn an unexpected response into an absent resource. | `vpc_node_attachment_resource.go`, `Read` | Treat malformed responses as errors; remove state only after a trustworthy missing-resource result. Refresh subnet identity too. |
| Input validation and update semantics are incomplete. | Resource schemas and `provider.go` | Validate enums, CIDRs, ports, IDs, endpoint, and unknown provider values. Make creation-only flags immutable or explicitly define their updates. Preserve/refresh computed values when writing updated state. |

## Next implementation sequence

### 1. Contract alignment and lifecycle tests

- Use `platform-docs/fern/openapi/ibee-cloud.yaml` as the documented public API input. `fern/apis/ibee-cloud/generators.yml` points the SDK generators there; `fern/openapi.yaml` is a different sample definition.
- Map every resource field and method to the public contract and `platform-docs/gateway/source/public-api.route-map.yaml`.
- Check development responses for stable IDs, nullable fields, response wrappers, pagination, eventual consistency, deletion constraints, and tenant isolation.
- Reconcile VM billing-catalog behavior: the provider requires a catalog and includes it in create requests, while the reviewed public create-request schema does not list that field. Establish the supported contract before making it a long-term provider requirement.
- Add HTTP fixture tests for errors, scope, idempotency, and polling, plus Terraform lifecycle tests for plan/apply/refresh/import/destroy. Include an unchanged second plan, external deletion, failed creation, malformed reads, concurrent firewall rules, and unmanaged subnet preservation.

**Exit criterion:** the existing six resource types have explicit contracts and regression coverage for the problems above.

### 2. Complete the first customer workflow

- Harden the six existing resources and add import support.
- Add `ssh_key_ids` to the cloud VM schema and request; the public create contract already accepts it. Start with keys created through the portal. No public SSH-key CRUD path was found in the reviewed spec/route map, so a standalone `ibee_ssh_key` requires a verified public API first.
- Add `ibee_firewall_attachment`; public list/attach/detach operations are documented. A firewall group and rules alone do not attach protection to the VM.
- Reconcile placement with the SDK/CLI: the API supports automatic VM placement, while Terraform currently requires `site_id`. For the initial network example, use an explicit compatible site and selected plan/image IDs. Verify compute and networking site compatibility.
- Keep authentication naming consistent and document development endpoint selection. Today the CLI supports `IBEE_ENV`/`IBEE_BASE_URL`; Terraform uses `IBEE_ENDPOINT`.
- Define VPC/default-subnet ownership before writing the example. Prefer an explicitly managed subnet with automatic default-subnet creation disabled where the API supports that workflow.

**Exit criterion:** a development workspace can create a VM plus VPC/subnet and firewall attachments, produce an unchanged second plan, detect a portal edit, import an existing supported resource, and destroy only the managed infrastructure.

### 3. Documentation and Registry release

- Refresh the README; add examples per resource/data source and one complete cloud-VM networking example.
- Generate provider/resource/data-source documentation with `terraform-plugin-docs`, including replacement behavior, import formats, API scopes, timeouts, and public-IP deletion choices.
- Add CI for formatting, build, vet, local tests, and documentation consistency. Run live acceptance tests separately against a designated development workspace.
- Select a license and add GoReleaser, release CI, supported platform binaries, SHA256 checksums, GPG signing, and a protocol-6 Registry manifest.
- Confirm ownership/access to the `devs-ibee` namespace; publish the first tested version and verify installation from a clean Terraform directory.
- Add an IBEE portal/docs guide: obtain a token, choose a workspace, select plans/images, run init/plan/apply, import existing resources, and clean up. Link it from the portal's developer area. Portal-side execution of Terraform is a separate feature and is not needed for this provider release.

HashiCorp's [publishing requirements](https://developer.hashicorp.com/terraform/registry/providers/publishing) cover public repository naming, documentation, the manifest, versioned releases, checksums, and signing. Its [import guide](https://developer.hashicorp.com/terraform/plugin/framework/resources/import) describes the framework import implementation.

### 4. Extend product coverage

After the first workflow passes the release criteria, add independently tested resources in this order:

1. GPU VMs, reserved IPs and their attachments.
2. Object-storage buckets, load balancers, NAT gateways and forwarding rules.
3. Secret-store metadata, backup policies, snapshots, and volume attachments where complete public lifecycle contracts exist.
4. SSH-key management and other portal features after their public APIs are available and verified.

For secret values or generated credentials, design state persistence explicitly; marking an attribute sensitive only redacts display. For reboot/restore/console operations, assess Terraform actions or keep them in the CLI until their Terraform semantics and version requirements are deliberately supported.

## Verification performed

- Go toolchain: `go1.26.5 darwin/arm64`; Terraform: `v1.15.8`.
- `go test ./...`: succeeds, but both packages report **no test files**.
- `go vet ./...`: succeeds.
- `go build -buildvcs=false`: succeeds; binary written under `/private/tmp`.
- `terraform validate` of the existing example using the freshly built local provider: succeeds, with the expected development-override warning. The initial sandbox attempt could not start the provider handshake; the rerun outside the sandbox succeeded.
- Public API contract validator: **145 OpenAPI operations match 145 gateway routes across 6 services**. This validates local definitions, not live deployment.
- No live IBEE provisioning, import, update, or destroy was performed. No provider release was published.

The immediate engineering milestone is steps 1 and 2: contract alignment, tests, lifecycle fixes, import, VM SSH-key inputs, and firewall attachment. Broader resource coverage follows a working, tested first workflow.
