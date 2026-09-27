# Terraform Provider for IBEE

Manage IBEE infrastructure through the workspace-scoped public API, using the same service-side billing decisions as the portal and SDKs.

**Status: partial development live validation completed; not yet published to the Terraform Registry.** This repository contains 23 resources and 5 data sources. Product coverage, API dependencies, and remaining portal gaps are listed in [COVERAGE.md](COVERAGE.md). Nothing here automatically funds an account.

## Local usage

Requirements: Go as specified in `go.mod`, Terraform **1.11+**, an IBEE API token with the relevant product permissions and `billing.read`, and a workspace ID.

```sh
make build
```

Create a local `dev.tfrc` with the absolute path to the built provider:

```hcl
provider_installation {
  dev_overrides {
    "devs-ibee/ibee" = "/absolute/path/to/terraform-provider-ibee/bin"
  }
  direct {}
}
```

```sh
export TF_CLI_CONFIG_FILE="$PWD/dev.tfrc"
export IBEE_ENV=dev
export IBEE_WORKSPACE_ID=your-development-workspace
# Inject IBEE_TOKEN from your local secret manager or shell environment.
terraform -chdir=examples validate
terraform -chdir=examples plan -var='site_id=your-site-id'
```

A development override uses the local binary directly; skip `terraform init` for these provider-only examples. After a signed Registry release exists, remove the override, pin its published version, and run `terraform init` normally. Review a plan before applying it to a real workspace; provisioning incurs charges.

```hcl
terraform {
  required_version = ">= 1.11.0"
  required_providers {
    ibee = { source = "devs-ibee/ibee" }
  }
}

provider "ibee" {}

data "ibee_billing_eligibility" "account" {}

resource "ibee_vpc" "main" {
  name    = "application"
  site_id = var.site_id
}
```

The provider address retains this repository's existing `devs-ibee/ibee` namespace. Publishing under the `IBEE` organization would require an intentional address/module migration and Registry ownership setup.

## Configuration

| Attribute | Default / environment |
| --- | --- |
| `token` | `IBEE_TOKEN`; sensitive; prefer the environment |
| `workspace_id` | `IBEE_WORKSPACE_ID`; required |
| `organization_id` | Optional `IBEE_ORGANIZATION_ID`; rejects billing decisions for another organization |
| `endpoint` | Explicit value, then `IBEE_ENDPOINT`, then `IBEE_BASE_URL`, then `IBEE_ENV` |
| `request_timeout` | `90s`, configurable from `1s` to `10m` |
| `operation_timeout` | `20m`, configurable from `1s` to `2h` |

`IBEE_ENV=dev` or `development` selects `https://api.ibee.co.in/v1`; `prod`, `production`, or unset selects `https://api.ibee.ai/v1`. HTTPS is required except for loopback mock servers. Known development/production token prefixes must match the selected IBEE host. Authentication and workspace authorization remain enforced by the gateway.

## Resources and examples

| Area | Implemented resources | Example |
| --- | --- | --- |
| Compute | Cloud/GPU VMs, cloud/GPU snapshots, cloud/GPU backup policies, cloud/GPU volume attachments | [Compute](examples/compute/main.tf) |
| Networking | VPC, subnet, VM attachment, NAT gateway, port-forwarding rule, reserved IP and attachment, L4/L7 load balancers | [Networking](examples/networking/main.tf) |
| Firewalls | Group, rule, VM attachment | [Networking](examples/networking/main.tf) |
| Storage and secrets | Bucket, secret store, write-only secret | [Storage](examples/storage/main.tf) |

Data sources: `ibee_sites` (compute), `ibee_network_sites` (networking availability), `ibee_compute_plans`, `ibee_images`, `ibee_billing_eligibility`. Attribute documentation is generated in [docs](docs/index.md). Each example directory is an independent root configuration and requires environment-specific input values.

## Billing and credits

Billable creates perform a fresh `POST /billing/resource-eligibility` before the product request. VM purchases resolve the organization currency from billing, select its trusted monthly catalog SKU and price, and verify the currency again in the final admission decision. Catalog discovery supports explicit currency and hourly/monthly filters; VM resources currently use the monthly catalog. Where the public API provides no catalog/quote, the check verifies account admission only; product services must enforce actual pricing, entitlements, add-ons, quotas, and concurrent affordability. The provider cannot reserve funds or replace server enforcement.

A denied, unavailable, malformed, or mismatched billing decision stops the purchase. Refresh, import, and destruction are not blocked by this creation preflight. A public API 403 is an error; only a resource-specific 404 removes it from state.

For `initial_topup_required` or `insufficient_balance`, Terraform instructs the user to use **Add Credits** in the organization's portal Billing page, wait for confirmed payment, and rerun apply. The public contract currently exposes no supported checkout/payment-status or wallet-management API. Credit purchases, manual grants, credit-limit changes, and payment confirmations are not simulated as Terraform resources.

## Lifecycle behavior

- VM configuration changes replace the VM; resizing and one-off restore/start/stop actions remain explicit API/CLI operations. Review replacement plans carefully.
- Import uses stable public IDs. Association resources document composite IDs. Imported resources do not silently adopt unmanaged children.
- VPC deletion owns only its recorded auto-created default subnet. Other subnets and attachments must be removed explicitly.
- Bucket deletion is recursive at the API. The default guard requires both usage counters to be zero. This check is not atomic: stop writers before destruction. Apply `force_destroy=true` only to deliberately permit content deletion.
- Secret values use Terraform write-only arguments with ephemeral sensitive inputs. They are excluded from plan/state; increment `value_wo_version` to rotate with check-and-set. Metadata reads currently require the value-read endpoint to obtain its version, so the token needs that permission. Avoid debug HTTP logging around secrets.
- Secret deletion is a soft delete; history and its name remain. Destroying a store archives it. The default archive guard refuses active secrets; apply `force_archive=true` to explicitly archive them. Store/secret names may therefore remain unavailable after destroy.
- Destroying backup policies disables future runs and retains recovery points, which may continue to incur storage charges. Detached block volumes and retained reserved IPs may also continue billing.
- Volume detach requires an explicit `confirm_unmounted` acknowledgement after unmounting inside the guest. The provider never forces detach.
- GET requests and requests carrying a documented idempotency key retry bounded transient HTTP responses. Mutations without such a key and ambiguous transport failures are not blindly replayed. Inspect the portal after an ambiguous create before retrying.

## Development and verification

```sh
make test              # Go tests with race detection, local HTTP fixtures
make vet
make test-terraform    # real Terraform CLI lifecycle against local mock APIs
make docs              # regenerate Registry pages and validate examples
make check             # format, vet, tests, generated docs and examples
```

The Terraform lifecycle test builds the real provider and uses isolated temporary state, a loopback API, and fixture credentials. It covers create, unchanged second plan, import, update, drift, billing-denied create, destroy while purchases are blocked, and write-only secret state protection. It never uses a live IBEE account. CI runs against Terraform 1.11.4 and 1.15.8.

Completed local checks are recorded in [VALIDATION.md](VALIDATION.md). Mock tests establish provider behavior, not deployed API compatibility. Follow [RELEASE_CHECKLIST.md](RELEASE_CHECKLIST.md) for the deferred development-workspace test and publication steps. The release workflow is configured to prepare a signed **draft** release only when a version tag is deliberately pushed.

## Read-only development preflight

Save development credentials in the ignored `.ibee-dev.json` file with string fields `token` and `workspace_id` (optionally `organization_id`). Keep that file local and restrict its permissions. Then run:

```sh
python3 scripts/dev_preflight.py
```

This builds the provider and evaluates only billing eligibility and catalog data sources against `https://api.ibee.co.in/v1`. It reports decision/currency and catalog counts, creates no managed resources or payments, and deletes its temporary Terraform state on exit. A passing preflight is the first live check; provisioning tests still require a selected plan/site and spending limit.
