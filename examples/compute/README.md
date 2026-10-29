# Compute examples

New VMs are created `HOURLY` unless `billing_interval` is set; `MONTHLY` is a one-month commitment. The provider copies the plan's `billing_catalog` from `GET /compute/plans` and merges in the matching `billing_options` entry, as the portal does. The API checks the selection and decides every other rule.

Snapshots and backup policies need a storage `billing_catalog`: take it unchanged from `ibee_snapshot_storage_plans` or `ibee_backup_storage_plans`. Volume attachments need the volume's `billing_catalog`, available as `ibee_block_volume.<name>.billing_catalog`.

Changing `plan_id` resizes a VM in place (the resize operation takes the new plan's `cpu`, `ram_mb` and `disk_gb`), and changing `ssh_key_ids` updates its access in place. The API decides whether each change is allowed.

Backup settings that are not configured are not sent, so the API's defaults apply.

See the [snapshot](../../docs/resources/cloud_vm_snapshot.md), [backup policy](../../docs/resources/cloud_vm_backup_policy.md), and [volume attachment](../../docs/resources/cloud_vm_volume_attachment.md) documentation for all options.
