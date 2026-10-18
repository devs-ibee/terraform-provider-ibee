# Compute examples

For now the API expects a `billing_catalog` on new VM snapshots and backup policies. Use `jsonencode` with the SKU and currency values provided for your account; the provider passes it to the API unchanged. Volume attachments use the volume's catalog when available.

Changing `plan_id` resizes a VM in place, and changing `ssh_key_ids` updates its access in place. The API decides whether each change is allowed.

New backup schedules default to daily at 12:00 UTC. Existing schedules are preserved; choose `daily` or `weekly` to change one. Weekly schedules require `day_of_week` (Monday is `0`, Sunday is `6`).

See the [snapshot](../../docs/resources/cloud_vm_snapshot.md), [backup policy](../../docs/resources/cloud_vm_backup_policy.md), and [volume attachment](../../docs/resources/cloud_vm_volume_attachment.md) documentation for all options.
