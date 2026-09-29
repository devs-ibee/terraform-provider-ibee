# IBEE Terraform provider examples

These configurations show how to use the provider's currently implemented public API. Each directory is an independent Terraform root unless the entry links to a file inside an existing root. Values such as site IDs, plan IDs, templates, storage regions, and object names must come from the target workspace and its catalogs; the examples do not invent them.

Set `IBEE_TOKEN` and `IBEE_WORKSPACE_ID` in your environment. Use `IBEE_ENV=dev` for the development API or configure `IBEE_ENDPOINT` explicitly. The root [README](../README.md#local-usage) explains local provider installation and development overrides. Review every plan before applying it: VM, networking, storage, and CDN resources may incur charges, and several resources have persistent or destructive lifecycle behavior.

## Examples by product

| Product | Example | Resources, data sources, and actions shown |
| --- | --- | --- |
| Compute | [compute](compute/main.tf) | `ibee_cloud_vm`, `ibee_gpu_vm`, `ibee_cloud_vm_volume_attachment`, `ibee_gpu_vm_volume_attachment`, `ibee_cloud_vm_snapshot`, `ibee_gpu_vm_snapshot`, `ibee_cloud_vm_backup_policy`, `ibee_gpu_vm_backup_policy`; `ibee_compute_plans`, `ibee_images` |
| Networking | [networking](networking/main.tf) | `ibee_vpc`, `ibee_vpc_subnet`, `ibee_vpc_node_attachment`, `ibee_nat_port_forwarding_rule`, `ibee_firewall_group`, `ibee_firewall_rule`, `ibee_firewall_attachment`, `ibee_reserved_ip`, `ibee_reserved_ip_attachment`, `ibee_load_balancer_l4`, `ibee_load_balancer_l7`; `ibee_network_sites` |
| Separately managed NAT gateway | [nat-gateway-import](nat-gateway-import/main.tf) | `ibee_nat_gateway` (import an existing separately managed gateway before planning; VPC-created default gateways are already owned by `ibee_vpc`) |
| Block storage | [block-storage](block-storage/main.tf) | `ibee_block_volume` |
| Storage-node attachment | [block-storage-node-attachment](block-storage-node-attachment/main.tf) | `ibee_block_volume_attachment` |
| Object storage and secrets | [storage](storage/main.tf), [storage configuration](storage/configuration.tf), [bucket configuration](bucket-configuration/main.tf), [bucket retention](bucket-retention/main.tf) | `ibee_bucket`, `ibee_bucket_cors`, `ibee_bucket_lifecycle`, `ibee_bucket_notifications`, `ibee_s3_credential`, `ibee_bucket_retention`, `ibee_secret_store`, `ibee_secret` |
| CDN | [cdn](cdn/main.tf), [custom origin](cdn-custom-origin/main.tf), [actions](actions/main.tf) | `ibee_cdn_distribution`, `ibee_cdn_website`, `ibee_cdn_domain`, `ibee_cdn_origin`, `ibee_cdn_purge`, `ibee_cdn_verify_domain` |
| Explicit VM operations | [actions](actions/main.tf) | `ibee_vm_power` |
| General/provider-level | [root example](main.tf) | `ibee_sites`, `ibee_billing_eligibility`, plus a small VPC and firewall-group example |

Together the examples declare all 34 provider resources, all five data sources, and all three Terraform actions. Some configurations need pre-existing resources or IDs; check each file's variables and comments before use.

## Products without provider examples

The portal includes products for which this provider has no supported public resource lifecycle today. The provider cannot safely manage a product just because it appears in the portal. See [COVERAGE.md](../COVERAGE.md) for the current implementation and backend boundaries.

Currently blocked categories include bare metal, DNS zones and records, SSL certificate lifecycle, ISO upload/management, standalone SSH key management, email-service control-plane configuration, and container-registry provisioning. Use the product UI or its supported API directly where available; these are not represented by pretend Terraform resources here. Object uploads and downloads also remain S3 data-plane operations, not Terraform resources.

## Important lifecycle notes

- Compute examples create billable resources. VM configuration changes replace the VM; explicit power actions run only when invoked.
- The networking example uses a VPC-created NAT gateway. Do not also declare `ibee_nat_gateway` for that same gateway. The separate NAT example is for imported, independently managed gateways.
- The storage-node attachment exposes a device on a storage node; it does not mount a volume inside a VM guest. Set `confirm_unmounted = true` only after unmounting before detach.
- Bucket retention cannot be cleared through the current API. Its example is disabled by default; enabling it applies a lasting default retention policy. Destroying the Terraform resource requires explicitly allowing Terraform to stop managing the still-active policy.
- Enabled bucket lifecycle rules can permanently delete matching objects. Bucket deletion does not empty objects for you.
- Generated S3 credentials are saved in Terraform state even though the secret is marked sensitive. Use an encrypted, access-controlled state backend.
- Secret values use write-only Terraform arguments and are not saved in plan or state. Destroy soft-deletes the current value; secret history and names remain.
- The custom-origin API route was unavailable in the checked development deployment. Its example is disabled by default; confirm that route is deployed before enabling it.
- CDN domain validation and TLS issuance are asynchronous. Configure the returned CNAME in DNS and use the verification action when appropriate.
