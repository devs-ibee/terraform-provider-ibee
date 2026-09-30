# Terraform Provider for IBEE

Manage IBEE infrastructure with Terraform.

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

Browse highlighted resources: [Cloud VM](docs/resources/cloud_vm.md), [VPC](docs/resources/vpc.md), [Bucket](docs/resources/bucket.md), and [CDN distribution](docs/resources/cdn_distribution.md). The [provider documentation](docs/index.md) lists all resources, data sources, and actions.

## Contributing

Contributions are welcome. Open an issue or pull request to get started.
