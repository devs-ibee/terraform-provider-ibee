# Examples

Each directory is an independent Terraform configuration. Set the required values for your workspace before running it. See the [getting started guide](../docs/guides/terraform.md) for installation and setup.

| Area | Example |
| --- | --- |
| Compute | [compute](compute/main.tf) |
| Networking | [networking](networking/main.tf) |
| NAT gateway import | [nat-gateway-import](nat-gateway-import/main.tf) |
| Block storage | [block-storage](block-storage/main.tf) |
| Object storage and secrets | [storage](storage/main.tf) |
| CDN | [cdn](cdn/main.tf) |
| Terraform actions | [actions](actions/main.tf) |

Set your API token and workspace ID in the environment:

```sh
export IBEE_TOKEN="your-api-token"
export IBEE_WORKSPACE_ID="your-workspace-id"
```

Review the Terraform plan before applying. Creating, replacing, or retaining infrastructure can incur charges.
