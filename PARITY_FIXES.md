# SDK/CLI alignment — September 29, 2026

This change addresses the four gaps identified in the SDK/CLI 0.4 audit. It is not a claim that every SDK operation is a Terraform resource, nor that every deployed backend permits successful writes.

## Canonical compute billing catalogs

Cloud/GPU snapshots, backup policies and VM-volume attachments now send `billing_catalog`. Use `jsonencode` on the authoritative BillingCatalogSelection: canonical `sku_id` and uppercase `sku_code`, with matching product/site/currency metadata where present. Numeric identifiers retain full precision. Aliases, malformed selections, root-disk add-ons and invalid price metadata are rejected. No SKU, price or successful billing decision is fabricated.

Snapshot and backup catalogs must be supplied explicitly because the public API has no supported catalog-list operation for them. Attachments can instead resolve the actual block volume's catalog and verify its site against the VM. If that projection is absent, supply the canonical block-storage selection explicitly. Missing permissions or API errors during lookup remain errors.

Omitting catalog inputs preserves existing recorded values. Legacy imports whose API projection lacks the catalog remain readable/deletable; adding the missing creation input to a snapshot/attachment records it without repurchasing. Changing a recorded snapshot/attachment catalog requires replacement. Review replacement plans carefully. Backup catalog updates (including adding one to an old policy) are sent to the backend and require billing admission.

The admission preflight checks the SKU and currency where available. It is not a price quote or reservation; service-side checks remain authoritative. Public OpenAPI generation still needs to expose these required backend fields consistently, as noted in the cross-team handoff.

Known-missing snapshot/backup catalogs are rejected during creation/replacement planning. A legacy snapshot cannot be deleted for a name/VM/mode replacement and only then discover that its required replacement catalog is absent. Explicit unknown catalogs on existing immutable resources conservatively plan replacement rather than introducing that requirement during apply. These guards leave unchanged legacy resources and intentional destruction available.

## Backup schedule compatibility

- **New policy:** daily at 12:00 UTC, minute 0, 30-minute window when omitted.
- **Existing/imported policy:** omitted schedule fields preserve refreshed state, including 20:00 and unchanged hourly schedules. Upgrading the provider alone must not reschedule backups.
- **New/changed schedule:** daily or weekly; weekly requires `day_of_week` (Monday=0), and other frequencies must not carry a day. IANA timezone and numeric bounds are validated.
- **Legacy hourly:** keep the schedule unchanged for retention-only updates or destruction. To migrate, explicitly set daily/weekly and review hour, timezone and day before applying. New/changed hourly schedules fail with migration guidance.

Policy destruction disables future runs; retained recovery points can continue billing. It does not delete historical backups.

## Power-state preconditions

Start requires `stopped`; stop/reboot require `running`. Case and surrounding whitespace are normalized. Unknown/transitional states fail before billing admission or power mutation. Existing idempotency keys, operation waits, cancellation and identity checks remain; stop does not perform the create-time billing preflight. Checks are point-in-time and do not replace server-side enforcement.

This matches the SDK's **opt-in** state-check behavior and the CLI's default behavior. It does not change the SDK default.

## Error diagnostics and state safety

The client parses gateway string errors and billing reasons, nested product errors, FastAPI details and legacy top-level codes. It preserves validated scope/request context, redacts the configured token and bounds metadata. Secret-resource diagnostics only use static allowlisted guidance.

HTTP status remains authoritative. Authorization denials are not retried and cannot remove managed state; only the resource's actual HTTP 404 can indicate absence. Transient retries remain bounded and mutations without documented idempotency are not blindly replayed.

## Verification boundary

`make test` runs race-enabled Go/HTTP fixtures. `make test-terraform` invokes the real Terraform CLI against loopback fixture servers; power actions are tested on Terraform 1.15+ and skipped explicitly on older versions. No fixture uses a real account or authorizes spending. Live create/refresh/import/destroy and cleanup require a separate approved development workspace, scoped credential and spending limit. Registry signing/publication remains a separate release gate.

Local verification on September 29 passed:

- Race-enabled Go suite: 119 top-level tests, 669 including subtests; the nine opt-in CLI fixtures were then run separately.
- Terraform 1.11.4: eight CLI fixtures passed; power-action fixture explicitly skipped because this harness requires 1.15+.
- Terraform 1.15.8: nine CLI fixtures passed, including 24 power-action subcases.
- Catalog-plan safety: 60 unit cases and eight real Terraform scenarios on both versions cover missing-catalog creation/replacement, explicit unknown inputs, unchanged legacy imports and zero API mutations after rejected apply. Both ordinary replacement and explicit `-replace` are checked.
- `go vet`, module verification, Go/Terraform formatting and diff whitespace checks passed.
- Generated documentation/examples checks passed on both versions (40 pages on 1.11.4, 43 with actions on 1.15.8).
- Cross-compilation passed for Linux, macOS, Windows and FreeBSD, each on amd64 and arm64.

CI now selects all `TestTerraform` fixtures, including power-action invocation. These are offline/loopback results, not successful live cloud provisioning evidence.
