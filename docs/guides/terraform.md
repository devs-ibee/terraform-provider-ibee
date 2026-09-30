# Use IBEE with Terraform

Use Terraform to describe and manage IBEE infrastructure from configuration files. This guide walks through setting up the provider and managing a VPC. See the [provider reference](../index.md) for every resource and argument.

## Before you start

- Install the [Terraform CLI](https://developer.hashicorp.com/terraform/install), version 1.11 or newer. Terraform 1.14 or newer is required for Terraform actions.
- Create an [IBEE API token](https://ibee.ai/docs/api-reference/cli) in the portal under **Settings → API Tokens**. Authorize it for the workspace you plan to manage. For billable resources, the token needs `billing.read` and the relevant product permissions.
- Install Go if you are building the provider from source.

## 1. Build the provider

The provider is not yet published in the Terraform Registry. From the root of this repository, build a local copy:

```sh
make build
```

This creates the provider binary in `bin/`.

## 2. Configure Terraform and credentials

Create a Terraform CLI configuration file with the absolute path to the `bin` directory:

```hcl
provider_installation {
  dev_overrides {
    "devs-ibee/ibee" = "/absolute/path/to/terraform-provider-ibee/bin"
  }
  direct {}
}
```

Set the path to that file and provide your token and workspace ID in the shell:

```sh
export TF_CLI_CONFIG_FILE="/path/to/dev.tfrc"
export IBEE_TOKEN="your-api-token"
export IBEE_WORKSPACE_ID="your-workspace-id"
```

Production is the default API environment. Set `IBEE_ENV=dev` to use the development API. Keep your token out of Terraform files and source control.

With a local development override, skip `terraform init` and use the local provider binary directly. When a Registry version is available, configure that version and initialize the project normally.

## 3. Create a Terraform configuration

Create a directory for your project, change into it, and save the following as `main.tf`:

```hcl
terraform {
  required_version = ">= 1.11.0"
  required_providers {
    ibee = { source = "devs-ibee/ibee" }
  }
}

provider "ibee" {}

variable "site_id" {
  description = "An available networking site ID"
  type        = string
}

resource "ibee_vpc" "example" {
  name    = "terraform-example"
  site_id = var.site_id
}

output "vpc_id" {
  value = ibee_vpc.example.id
}
```

Set `site_id` to an available networking site from the IBEE console or the [`ibee_network_sites` data source](../data-sources/network_sites.md).

## 4. Review and apply the plan

Run a plan with your site ID and review the changes Terraform proposes:

```sh
terraform plan -var="site_id=your-network-site-id"
```

If the plan is correct, create the VPC:

```sh
terraform apply -var="site_id=your-network-site-id"
```

Some resources incur charges. Terraform can also replace or destroy resources when their configuration changes, so review each plan before applying it.

## 5. Clean up

Remove the example VPC when you no longer need it:

```sh
terraform destroy -var="site_id=your-network-site-id"
```

## Next steps

- Browse the [resource, data source, and action reference](../index.md).
- Explore the [compute, networking, storage, and CDN examples](../../examples/README.md).
- See [VPC options](../resources/vpc.md) and [network site data](../data-sources/network_sites.md).
