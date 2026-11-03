# Terraform Provider for IBEE

Manage IBEE infrastructure with Terraform.

Object Storage and Container Registry public management APIs are temporarily paused.
Their resources remain in the provider for compatibility. An empty 404 reports
the pause and retains existing Terraform state; it is not treated as deletion.
CDN remains available.

[Getting started](docs/guides/terraform.md) · [Provider documentation](docs/index.md) · [Examples](examples/README.md)

## Requirements

- Terraform 1.11 or newer (1.14 or newer for Terraform actions)
- An IBEE API token and workspace ID

## Installation

Follow the [getting started guide](docs/guides/terraform.md) to install, configure, and use the provider.

## Quick example

```hcl
terraform {
  required_version = ">= 1.11.0"
  required_providers {
    ibee = { source = "devs-ibee/ibee" }
  }
}

provider "ibee" {}

resource "ibee_vpc" "main" {
  name    = "application"
  site_id = var.site_id
}

variable "site_id" {
  type = string
}
```

## Resources

Browse highlighted resources: [Cloud VM](docs/resources/cloud_vm.md), [VPC](docs/resources/vpc.md), [Bucket](docs/resources/bucket.md), [CDN distribution](docs/resources/cdn_distribution.md), and [Container registry](docs/resources/container_registry.md). The [provider documentation](docs/index.md) lists all resources, data sources, and actions.

Explicit Terraform actions can [start a manual VM backup](docs/actions/vm_backup_create.md)
using its enabled policy and [cancel a queued backup restore](docs/actions/vm_backup_restore_cancel.md).
These actions require Terraform 1.14 or later; ordinary plan and refresh do not invoke them.

## Contributing

Contributions are welcome. Open an issue or pull request to get started.

The provider is built against the public API contract in [contract/ibee-cloud.yaml](contract/ibee-cloud.yaml). `go test ./...` fails if the provider sends a request that is not an operation in it; update the vendored copy when the contract changes.
