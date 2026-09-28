# Compute recovery and attachment billing

New Cloud and GPU snapshots and backup policies require `billing_catalog`, a JSON-encoded canonical Billing catalog selection. Set the example's catalog variables to real `snapshot_storage` and `backup_storage` objects for the target site and organization currency, then pass them using `jsonencode`. The public API does not list these catalogs; obtain them from an existing matching resource or IBEE. Do not derive them from a VM plan or invent SKU IDs or prices.

The catalog must have `sku_id` and canonical uppercase `sku_code`. If included, `product_code`, `site_id`, and `currency` must match the resource product, target VM, and organization respectively. Use canonical snake_case keys. Supplied prices must be nonnegative integer minor units. Additional canonical metadata is preserved; eligibility checks do not calculate a snapshot/backup price or reserve funds. The backend remains authoritative for SKU validity and pricing.

VM-volume attachments resolve an omitted catalog from the volume's `billing_catalog` or `metadata.billing_catalog`; this requires `block_storage.read` as well as VM access. If the volume does not expose its catalog, explicitly supply its canonical `block_storage` catalog. Missing or mismatched catalog data stops creation before a mutation.

Refresh preserves recorded catalog inputs when historical responses omit them. Legacy snapshots and policies without catalog projections remain importable and deletable. Attachment imports read the volume catalog when the VM attachment projection omits it. Omit `billing_catalog` when importing an older resource whose catalog is unavailable. Adding a previously absent snapshot/attachment input records configuration without purchasing again. Changing a known snapshot/attachment catalog requires replacement. Explicit changes to a backup catalog are sent to the backend after eligibility checks; ordinary schedule or retention updates do not resend it.

If an imported response includes additional default catalog metadata, configuring the same selection with fewer optional fields may produce a one-time local state update. This does not replace the resource or send a replacement catalog. Subsequent plans converge, and removing an optional input is not a request to clear the backend's billing metadata.

# Backup schedule migration

New policies default to daily at **12:00 UTC**, with a 30-minute window. New schedules support `daily` or `weekly`; weekly requires `day_of_week` (Monday 0 through Sunday 6). Supplying `day_of_week` with an explicit nonweekly frequency is an error.

Omitted schedule attributes preserve refreshed state. Upgrading or importing a policy at 20:00 does not change it to 12:00; an existing hourly policy stays hourly. Existing weekly policies retain their saved weekday when it is omitted. Explicitly configured 20:00 is still supported. Copying the example's explicit `hour = 12` onto an existing policy intentionally changes its hour, so inspect the plan.

An unchanged historical hourly schedule can remain in configuration and supports retention changes and destruction. Editing its schedule requires explicitly migrating to daily or weekly. For example, configure `frequency = "daily"`, `timezone = "UTC"`, and `hour = 12` to move deliberately to the new default. Merely changing the hour on an hourly policy produces a compatibility error. Moving from weekly to daily clears the weekday. Terraform never applies the SDK's fallback from a saved hourly schedule to daily implicitly.

Destroying a policy disables future backups and retains existing recovery points, which may continue to incur storage charges.
