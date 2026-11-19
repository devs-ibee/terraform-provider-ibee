# Terraform Provider for IBEE

Manage IBEE infrastructure with Terraform.

[Getting started](docs/guides/terraform.md) · [Provider documentation](docs/index.md) · [Examples](examples/README.md)

## Requirements

- Terraform 1.11 or newer
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

Browse highlighted resources: [Cloud VM](docs/resources/cloud_vm.md), [VPC](docs/resources/vpc.md), [Load balancer](docs/resources/load_balancer.md), [Bucket](docs/resources/bucket.md), [CDN distribution](docs/resources/cdn_distribution.md), [Container registry](docs/resources/container_registry.md), and [Secret Manager](docs/resources/secret_manager.md). The [provider documentation](docs/index.md) lists all resources and data sources.

The provider manages infrastructure only. One-off operations, such as power actions, restoring a backup or purging a CDN cache, belong in the [CLI or SDKs](https://ibee.ai/docs). Files go through the S3 API, images through the registry, and secret values through the Vault API; Terraform never carries them.

## Contributing

Contributions are welcome. Open an issue or pull request to get started.

The provider is built against the public API contract in [contract/ibee-cloud.yaml](contract/ibee-cloud.yaml). `go test ./...` fails if the provider sends a request that is not an operation in it; update the vendored copy when the contract changes.
