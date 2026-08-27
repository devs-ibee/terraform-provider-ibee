# Terraform Provider for IBEE

Manage [IBEE](https://ibee.ai) cloud resources with Terraform through the IBEE public API.

> **Status: early preview.** Resources: `ibee_vpc`, `ibee_firewall_group` · Data sources: `ibee_sites`. VM, subnet, reserved-IP, load-balancer, and attachment resources are planned.

## Usage

```hcl
terraform {
  required_providers {
    ibee = { source = "devs-ibee/ibee" }
  }
}

provider "ibee" {
  # endpoint defaults to https://api.ibee.ai/v1
  # token & workspace_id can come from IBEE_TOKEN / IBEE_WORKSPACE_ID env vars
}

data "ibee_sites" "all" {}

resource "ibee_vpc" "main" {
  name    = "production"
  site_id = data.ibee_sites.all.sites[0].site_id
}

resource "ibee_firewall_group" "web" {
  name        = "web-tier"
  description = "Public HTTPS for the web tier"
}

output "vpc_subnet" { value = ibee_vpc.main.default_subnet_id }
```

## Provider configuration

| Attribute | Env fallback | Description |
|---|---|---|
| `endpoint` | `IBEE_ENDPOINT` | API base URL (default `https://api.ibee.ai/v1`) |
| `token` | `IBEE_TOKEN` | IBEE API token (`ibee_prod_key_…`) — keep out of source control |
| `workspace_id` | `IBEE_WORKSPACE_ID` | Numeric workspace every request is scoped to |

## Development

```sh
go build -o bin/terraform-provider-ibee .
```

Point Terraform at the local build with a CLI config file:

```hcl
# dev.tfrc
provider_installation {
  dev_overrides {
    "devs-ibee/ibee" = "/absolute/path/to/repo/bin"
  }
  direct {}
}
```

```sh
export TF_CLI_CONFIG_FILE=$PWD/dev.tfrc
terraform -chdir=examples plan
```

## Notes

- Deleting an `ibee_vpc` also deletes its subnets first — the API requires a VPC to be empty before deletion.
- Firewall groups have no update endpoint in the public API; changing `name`/`description` replaces the group.
