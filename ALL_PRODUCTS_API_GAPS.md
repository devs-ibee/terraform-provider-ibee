# Every-product Terraform coverage: API evidence and remaining work

Audit date: 2026-09-27. This is a source and contract audit, not a declaration that every service is deployed or that a live create/delete has passed. Authenticated, read-only gateway observations are identified separately from source evidence below.

## Corrections to the earlier coverage assessment

**Standalone block storage and CDN distribution management are real implementations, not absent products.** Their public route families were missing from the inspected local OpenAPI and gateway manifests. The subsequent live checks confirmed authenticated gateway reads for both families. They should be implemented using those existing backends, with their remaining gaps documented separately.

| Capability | Evidence | Current conclusion |
| --- | --- | --- |
| Standalone block volumes | Public-token scopes and admission rule exist in Auth; gateway-ready facade and full volume lifecycle exist in Portal API/Persistent Block. Live checks returned `GET /v1/block-storage/volumes` → HTTP 200 with a bare array. | Provider resource `ibee_block_volume` added in this task. Do not classify volume CRUD as absent. Live create/resize/delete remain unverified. |
| CDN distributions | Full tenant-scoped CRUD exists in Object Storage, with Auth `cdn.read`/`cdn.write`. Live checks returned `GET /v1/cdn/distributions` → HTTP 200 with `{count, distributions}`. | Implement against the existing API; missing generated documentation is a contract-maintenance gap. |
| Block catalog discovery | Facades exist in source. Live checks returned `/v1/block-storage/plans` and `/v1/catalog/products/block_storage/plans` → HTTP 404. | Public catalog discovery is not currently confirmed. Require a real SKU from the portal; let the server validate SKU/size and compute pricing. Do not synthesize catalog prices. |
| CDN custom origins | Full CRUD exists upstream under `/cdn/v1/origins`. Live checks returned `GET /v1/cdn/origins` → HTTP 404 with an empty response. | Source implementation is ready; the tested gateway has no usable origin route. Provider code against this source must be labeled deployment-dependent until routing is supplied. |
| Other CDN routes | Website configuration and custom-domain/managed-TLS routes exist upstream. | Live website-config and custom-domain list GETs returned HTTP 200. Mutations, DNS propagation and TLS issuance are not live-validated. |

A failed discovery route must not be treated as an empty catalog. Conversely, omission from an old OpenAPI must not be treated as proof that a product API does not exist.

## Sources inspected

- Portal API snapshot: `portal-client-api (reviewed snapshot)`, revision `dca35ae`, branch `release`.
- Portal UI snapshot: `portal-client-ui (reviewed snapshot)`, revision `151077f`, branch `feature/workspace-environment-api-dev-0926`.
- Public API contract: `platform-docs/fern/openapi/ibee-cloud.yaml`.
- Gateway route source and generated manifests: `platform-docs/gateway/source/public-api.route-map.yaml` and `gateway/deploy/{dev,prod}`. These inspected files contain no standalone block-storage or CDN family mappings, despite the gateway read results reported above.
- Product implementations: local `persistentblock-service` revision `973a94d`, `object-storage` revision `bdd6053`, and local `auth-service`.
- Current provider: `internal/provider/provider.go`, `block_volume_resource.go`, compute resources, storage resources, and `COVERAGE.md`.

Source references below identify repository-relative paths and line numbers within these snapshots. No customer or organization identifiers, credential values, or private billing records are included.

## P0: make public routing and published contracts describe the deployed API

1. Reconcile the existing `/v1/block-storage/*` and `/v1/cdn/*` gateway routes into the canonical route map and OpenAPI. Export deployed route configuration rather than guessing from the old generated manifests.
2. Preserve the existing public token boundary: `Authorization`, workspace query selection, scope checks, removal of caller-supplied tenant headers, and gateway injection of the verified organization/workspace pair.
3. A provider must never call a `/public-api/*` upstream facade directly and supply its own trusted tenant headers. These facades trust gateway context; their existence is not independent public authentication.
4. Document real response envelopes: standalone volume lists are arrays; mutations return `{volume, operation}`; CDN lists return `{distributions, count}`.

Evidence: Auth `app/services/api_key_service.py:62` adds `block-storage.read/write` and `cdn.read/write`; its owner map at line 124 recognizes `block-storage` and `cdn`. Auth `app/services/billing_admission_service.py:100` recognizes block-volume creates and CDN distribution/custom-domain creates. Portal `app/routes/resource_scope.py:32` only checks the supplied canonical header pair; public authentication must happen at the gateway. The current published gateway contract describes header stripping/injection at `gateway/source/public-api.route-map.yaml:11`.

The portal frontend is not evidence that it uses public API tokens. Its block-storage client uses a product-service base URL and `/v2/volumes` (`app/clientEndPoints.tsx:134`, `:534`); the normal portal session-auth flow uses Auth introspection. In the inspected Auth implementation, customer introspection accepts Zitadel tokens, while public `ibee_*` API keys use the separate ext-auth/API-key path (`auth-service/app/services/introspection_service.py:1`, `:128`; `app/services/api_key_service.py:109`).

## Compute feature parity: resources, explicit actions, and remaining behavior

Cloud and GPU VM categories contain more than instance CRUD. The following matrix tracks the actual behavior, not only product names. Public paths below are relative to `/v1`; replace `{kind}` with `cloud` or `gpu`.

| Portal feature / API surface | Provider behavior in this task | Remaining work or deliberate boundary |
| --- | --- | --- |
| VM create/read/delete, placement, catalog plan/image, SSH-key IDs, tags | `ibee_cloud_vm` and `ibee_gpu_vm`; fresh currency-aware catalog/admission; explicit hourly/monthly term selection; canonical refresh/import; asynchronous operations. | Creation configuration changes currently replace the VM. Billing defaults to uncommitted HOURLY; MONTHLY must be an advertised one-month commitment. Canonical GET must contain the selected billing_catalog.billing_interval; legacy unselected catalogs cannot establish contractual intent. This is not equivalent to portal in-place resize/access updates. |
| Start, stop, reboot: `POST /compute/{kind}-vms/{id}/actions/{start,stop,reboot}` | Explicit `ibee_vm_power` action added, Terraform >=1.14; no forced operation; polls operation and canonical VM state. Start/reboot run account admission; stop still obeys backend policy. | Action must be explicitly invoked or triggered. No power mutation occurs during refresh. Backend power handlers require an idempotency key and check VM billing state even for stop; provider cannot bypass that policy. |
| CPU/RAM/root-disk resize: `POST .../actions/resize/precheck`, `POST .../actions/resize`, `PATCH .../actions/resize-plan`, `PATCH .../actions/resize-root-disk` | Existing instance resource refreshes shape drift but replaces configured shape changes. | In-place provider resize remains unimplemented. Must consume precheck restrictions, canonical catalog/currency and SKU changes, explicit downgrade confirmation and grow-only root-disk semantics; wait on returned operation then refresh. API exists and should not be described as missing. |
| Access update: `PATCH .../actions/access` | Creation supports existing SSH-key IDs; no ongoing access-update action. | API accepts key add/remove and password/auth settings. A future action must distinguish key updates from generated/one-time secrets and verify supported canonical access fields; do not replace a VM silently to mimic an in-place access operation. |
| VM volume hot-plug: `POST .../actions/attach-volume` and `/detach-volume` | Cloud/GPU VM volume-attachment resources; canonical `data_volumes` read, safe detach acknowledgement. | These invoke VM orchestration. They are distinct from the standalone storage-node attachment described below. Never manage one attachment through both surfaces. |
| Snapshot create/read/delete and root/data-volume selection | Cloud/GPU snapshot resources with immutable selection and async readback. | Restore is an explicit operation, not update/delete of a snapshot resource. Snapshot storage billing/catalog enforcement stays server-side. |
| Snapshot restore: `POST /compute/{kind}-vm-snapshots/{id}/actions/restore?vm_id=...`; `GET .../restores/{restore_id}` | Not implemented in this pass. | Source supports `replace`, `new_vm`, and `volume_only`. An action needs explicit destructive target selection, trusted target sizing/billing, resumable restore identity and post-restore readback. New-VM/volume targets create independently managed assets and need explicit import/ownership handoff. |
| Backup policy enable/schedule/retention/disable | Cloud/GPU backup-policy resources. Destroy disables future backups and retains existing recovery points. | One-time next-run rescheduling (`PATCH .../backups/policy/next-run-at`) is not a persistent schedule and has no provider action yet. |
| Manual backup: `POST .../backups/runs`; run list and `GET /compute/{kind}-vm-backups/runs/{run_id}` | Not implemented as an action or discovery data source. | Backend can queue a run and expose status. Reviewed request models do not define an idempotency key, and reviewed services did not establish request-key deduplication; publishing resumable submission semantics avoids duplicate paid backups after an ambiguous response. |
| Backup restore: `POST .../backups/actions/restore`; `GET /compute/{kind}-vm-backups/restores/{restore_id}` | Not implemented in this pass. | Same target-mode/ownership/recovery requirements as snapshot restore. These routes and asynchronous result projections already exist; absence here is a provider/reliability work item, not proof of missing backend functionality. |
| Rebuild/reinstall | No resource/action implemented. | A `rebuild` value in the operation enum is not a callable public endpoint. No public rebuild route was found in the reviewed OpenAPI; publish a tenant-authorized create-operation/read contract, access/image choices, destructive confirmation and billing semantics before implementation. |
| Console: `POST, GET, DELETE /compute/console/sessions` and session ID routes | Intentionally not a persistent Terraform resource. | The console URL contains a short-lived secret. Portal/CLI session creation is appropriate; a future ephemeral Terraform construct must not persist connection credentials in normal state. |
| Metrics, operational history, recovery-point discovery | Portal/API reads exist; provider does not expose all as data sources. | Add demand-driven data sources with pagination, identity and status validation. Monitoring streams, terminal sessions and expiring access are not provisioned infrastructure. |

Sources: public OpenAPI power routes at lines 2202/3174, resize at 2364/3334, attach at 2486/3448, snapshot restore at 2713/3671, backup runs at 2943/3901, backup restore at 2992/3950, and console at 4020. Backend `virtualization-engine/app/api/v2.py:165`, `:207`, `:249` and GPU equivalents consume `X-Idempotency-Key` and return operation IDs. `app/api/v2_backups.py:1551`, `:1844`, `:1902`, `:2037` implement manual runs and restores; `app/models/backups.py:235` and `persistentblock-service/app/backups/models.py:189` show the manual-run request without a request-key field. Snapshot restore orchestration is in `app/api/v2_snapshots.py`; a successful restore includes actual virtualization hooks, unlike ISO metadata attachment.

## P1: finish the block-volume surface

Existing upstream facades in Portal `app/routes/blockstorage_proxy.py:312`:

| Existing upstream facade | Product endpoint | Public family status |
| --- | --- | --- |
| `GET /v1/public-api/blockstorage/plans` | Server billing catalog | Public `/v1/block-storage/plans` returned 404. |
| `GET /v1/public-api/catalog/products/block_storage/plans` | Server catalog used by edge admission | Public catalog URL returned 404; upstream internal use exists. |
| `GET, POST /v1/public-api/blockstorage/volumes` | `GET, POST /v2/volumes` | Public volume list confirmed; mutation lifecycle supported by source. |
| `GET, DELETE /v1/public-api/blockstorage/volumes/{id}` | `GET, DELETE /v2/volumes/{id}` | Source supports safe lifecycle/import. |
| `GET /v1/public-api/blockstorage/volumes/{id}/operations` | `GET /v2/volumes/{id}/operations` | Needed for mutation polling. |
| `POST /v1/public-api/blockstorage/volumes/{id}/resize` | `POST /v2/volumes/{id}/resize` | Source allows increases; rejects shrinking. |
| `POST /v1/public-api/blockstorage/volumes/{id}/attachments` and `/detach` | Corresponding `/v2/volumes/{id}` actions | `ibee_block_volume_attachment` manages the storage-node attachment. VM attachment resources use compute orchestration; do not manage the same attachment twice. |

The new `ibee_block_volume` uses required name/site/SKU/size, optional class and cloud/GPU storage compatibility, computed replica count, and an explicit online-resize setting. Import reads the SKU from `metadata.billing_catalog.sku_code`. Decreasing size replaces; increasing size invokes resize. Deletion reads attachment state, refuses attached volumes, sends `force=false`, and verifies GET returns 404. It never deletes a VM root disk.

Required response projections already exist in `persistentblock-service/app/models/product.py:137` and `app/api/product_endpoints.py:235`: stable `id`, `name`, `site_id`, `size_gb`, `state`, `volume_class`, `volume_kind`, `vm_type`, `replica_count`, `volume_name`, `attachments`, and `metadata.billing_catalog` containing the canonical SKU/currency. Mutations return `volume` plus an operation (`id`, `volume_id`, `status`, `error`); operation states are `in-progress`, `succeeded`, `failed`.

Standalone node attachments are implemented by `ibee_block_volume_attachment`: required volume ID/canonical node, optional VM association metadata, single/multi-writer mode, computed device path/compute compatibility, and explicit unmount acknowledgement. The upstream calls `driver.attach_volume` (`product_endpoints.py:797`) and returns `attachments[{node_name,vm_id,mode,device_path}]`; it does **not** hot-plug a guest VM. Import uses `VOLUME_ID/NODE_NAME`; existing attachments cannot be silently adopted. Detach uses `force=false`, sends the same idempotency key in JSON/header, polls operations and confirms this node is absent. Separate volume and attachment resources allow Terraform to detach before deleting storage.

The standalone `/v2/volumes` product router exposes create/list/get/delete, attach/detach, resize, and operation history. It does not expose standalone product-volume clone, snapshot policy, replica-count update, rename, class conversion, or migration in this reviewed router. Snapshot/backup services elsewhere in Persistent Block primarily use their own recovery/VM topology contracts; do not invent `/block-storage/volumes/{id}/clone` or replica-update endpoints. Current resource keeps replica count computed and replaces immutable name/class/site/SKU changes. Product metadata `backup_enabled` is not a full independently readable backup-policy lifecycle.

Remaining concrete work:

- Publish a usable public catalog endpoint with `sku_code`, selectable status, site choices, allowed sizes, currency, interval, and the authoritative price/performance schedule. Existing source returns `{items, currency, billing_interval}`; reuse its actual catalog shape.
- Fix currency selection in both block facade catalog reads and create canonicalization. `app/routes/blockstorage_proxy.py:116` and `:324` hardcode `MONTHLY`/`INR`. The provider consequently refuses purchases/expansions for non-INR organizations rather than applying INR minor units to another currency. Support for other currencies needs a server correction.
- Reconcile idempotency conventions: Persistent Block reads `idempotency_key` from create/resize JSON and delete query parameters. A header alone is insufficient (`app/models/product.py:81`, `:110`; `app/api/product_endpoints.py:1532`). The provider sends the same logical key in both the API-specific location and the header.
- Confirm server-side price/size/operation admission on resize. The public facade strips caller billing fields on resize; Auth's reviewed edge admission inventory explicitly lists create, not resize. Provider preflight is not a substitute for backend capacity-increase enforcement.

## P1: finish the CDN surface and document TLS ownership

The Object Storage API already has a usable tenant-scoped model. It takes the gateway's organization/workspace pair through `app/core/auth.py:99`; no browser session or raw Cloudflare credential is needed by the provider. The upstream router is `/cdn/v1` (`app/api/cdn/v1/router.py:10`).

| Existing upstream endpoints | Configuration and read projection | Public completion needed |
| --- | --- | --- |
| `POST, GET /cdn/v1/distributions`; `GET, PATCH, DELETE /cdn/v1/distributions/{id}` | Create: `name`, `origin_type`, `origin_id`, `cache_policy`. Update: `name`, `cache_policy`, `enabled`. Read also returns ID, status, default domain/URL and custom domains. | Distribution list is deployed publicly; publish the rest of the family and exact scope mappings. |
| `GET /cdn/v1/distributions/cache-policies` | `{policies:[{id,name,description,headers}]}` | Reconcile the public spelling: the portal wrapper uses `/cdn/cache-policies`, while the upstream path includes `/distributions`. |
| `POST, GET /cdn/v1/origins`; `GET, PATCH, DELETE /cdn/v1/origins/{id}` | `name`, `origin_url`; read ID, project namespace, default URL and timestamps. | `/v1/cdn/origins` returned gateway 404. Add mapping before claiming deployed support. |
| `GET, PUT, DELETE /cdn/v1/distributions/{id}/website-config` | `index_document`; read `distribution_id`, `enabled`, timestamps. | Publish route and verify that disable/delete has a stable readable response. |
| `POST, GET /cdn/v1/distributions/{id}/custom-domains`; `GET, DELETE /.../{domain}`; `POST /.../{domain}/verify` | Requested domain; read status, CNAME validation, TLS progress and creation time. | Confirm individual gateway routes; support pending DNS/TLS without losing the domain identity. |

Evidence: `object-storage/app/schemas/cdn.py:50`, `:91`, `:114`, `:183`, `:362`, `:398`, `:441`; handlers in `app/api/cdn/v1/{distributions,origins,website,custom_domains}.py`. Distribution states are `active`, `deploying`, `disabled`, `failed`, `deleted`. Custom-domain verification distinguishes DNS validation and TLS issuance.

Managed TLS for a CDN custom domain is a real feature, described and implemented by `app/api/cdn/v1/custom_domains.py:27`. It is not equivalent to the standalone portal SSL-certificate product. The CDN URL-generation facade (`portal-client-api/app/routes/cdn.py:150`) is also real, but generating a potentially expiring object URL does not provision a persistent CDN distribution.

## P1: expose the existing SSH-key lifecycle through public-token authentication

Portal SSH-key management is implemented and registered. The current routes are under `/v1/ssh-keys`:

- `POST /create`, `POST /import`, and `POST /generate`.
- `GET /organization/{organization_id}` and names discovery.
- `GET, PUT, DELETE /{key_id}`.

Sources: Portal `app/routes/__init__.py:95`; `app/routes/ssh_keys_enhanced.py:588`, `:622`, `:656`, `:783`, `:939`, `:966`, `:1041`.

Missing public work is **authentication, tenant semantics and routing**, not a public-key implementation. These routes require a current portal user and user-owned key metadata. The reviewed public API-key scope inventory contains no SSH-key owner family. Publish a workspace-scoped public facade with create/read/update/delete, enforce permissions from the token's subject, and define organization-shared versus user-owned key behavior explicitly.

For Terraform, expose public key import (`name`, `ssh_key`, optional description), canonical public-key/fingerprint/type/name readback, and stable ID. Avoid generated private keys in ordinary Terraform state. Creation writes both the Secret Store value and a typed key metadata index (`ssh_keys_enhanced.py:323`); writing an arbitrary `ibee_secret` is therefore not portal-equivalent key creation. The existing GET can perform legacy migration (`:939`), which should be completed separately or made explicit before promising a read with no mutations.

## P1: expose registry provisioning, plan selection and credentials

Registry lifecycle already exists in the portal and product service. Portal source `app/routes/container_registry.py` has:

- `GET, POST /v1/registries`; `GET, DELETE /v1/registries/{id}`.
- `GET /{id}/package`, `POST /{id}/upgrade`, `PATCH /{id}/visibility`.
- `GET, POST /{id}/access-tokens`, rotate and revoke token actions.
- Security settings read/update; repository/artifact discovery and explicit deletions.

Create validates `registry_name`, matching `confirm_registry_name`, visibility, explicit public-egress acknowledgement, and `billing_catalog.sku_code` (`:471`). Delete requires matching name confirmation (`:677`). The proxy resolves organization/workspace permissions and injects an internal service token (`:145`, `:201`); providers must never request or send that service token.

Missing public requirements:

1. Add registry read/write/credential scopes and route-owner mapping to API-key authorization; publish a token-authenticated facade or protected direct gateway translation.
2. Expose a public registry plan/catalog or quote with server SKU/site/capacity/currency; retain the existing server admission path.
3. Publish canonical GET fields for name, visibility, selected package/SKU, status, endpoint, retention/security settings, and terminal failures. Ensure every configured field can refresh and import.
4. Define one-time credential output/rotation/import semantics independently from registry infrastructure. A credential resource cannot reconstruct a secret from a masked list entry.

This is a facade/contract work package. There is no basis for treating portal session endpoints as already usable with the provider's `ibee_*` token.

## P1: expose the implemented email control plane

Portal `app/routes/email_service.py:23` registers `/v1/organizations/{organization_id}/workspaces/{workspace_id}/email-service`. It supports sending domains, DNS instructions, verification/retry/cancel/decommission, operation reads, sender addresses, sending API keys, webhooks, and suppressions. The operation allowlist at `:69` and individual routes at `:471` onward are the precise source inventory.

The proxy authenticates a portal user and sends a server-held `X-IBEE-Service-Token` upstream (`:291`). Public API-key Auth has no email control-plane route family in the inspected scope inventory. Email sending credentials and public platform API keys are distinct; a data-plane sending key must not become a control-plane provisioning credential.

Publish public-token workspace routes for:

- Sending-domain create/read, DNS validation instructions, verification and idempotent decommission with operation ID/status/error polling.
- Sender-address list/create/read-or-complete-list/update/delete.
- Webhook list/create/read-or-complete-list/update/delete, event sets/status, and deliberate secret rotation.
- Sending-key policy/list/create/revoke with one-time secrets and import limitations.

Do not model sending an email, testing a webhook, or retrying a past delivery as a persistent resource. Those are explicit actions. Required GET projections include every mutable user setting and stable identity; DNS/TLS/external verification remain asynchronous.

## P2: complete ISO ingestion and actual VM-media attachment

Source exists at Portal `app/routes/isos.py`, but registration was not found in the inspected `app/main.py` or aggregate router. The UI calls `/v1/isos` (`portal-client-ui/app/clientEndPoints.tsx:923`). Confirm registration/deployment rather than assuming this source module is reachable.

Existing module operations: `GET, POST /isos`, `DELETE /isos/{id}`, `POST /isos/{id}/attach`, `POST /isos/{id}/detach`. Ingestion takes source URL/name/account identifier, runs a background transfer and stores status `pending`, `available`, or `error` (`:583`). Missing pieces for a reliable Terraform contract:

- Public-token scope mapping and a canonical organization/workspace facade; replace caller-selected account ownership. Current list and ID lookups must be scoped before public exposure.
- A single-item GET or complete paginated list that exposes ID, tenant, name, source identity/checksum, stored-object identity, size, status and failure reason.
- Durable idempotent ingestion and operation recovery; avoid creating duplicates after an HTTP timeout.
- Define whether deletion removes the underlying stored ISO object and stops storage billing. The reviewed DELETE removes the metadata document only (`:644`).
- Implement actual VM media attachment. The reviewed attach/detach handlers only update `attached_to` and `attached_vm_name` in the ISO record; they do not call virtualization to mount/eject media (`:667`, `:702`). A provider must not claim that metadata-only changes mounted an ISO.

## P2: standalone SSL, DNS and bare-metal provisioning

These categories have more than a missing Terraform wrapper:

| Category | Existing evidence | Missing functional contract |
| --- | --- | --- |
| Standalone SSL certificates | Portal page has local React certificate state; its Create button has no submit handler (`app/routes/client/ssl-certificates.tsx:310`, `:325`). CDN custom-domain automatic TLS is separately implemented. | Real issue/import/read/delete APIs; certificate fingerprint/public chain/status; private-key write-only handling if imports require it; domain validation and renewal ownership. No standalone certificate lifecycle was found in the public contract or reviewed portal backend. |
| DNS | Workspace DNS points to `coming-soon.tsx` (`app/routes.ts:123`). Internal CDN DNS helpers manage platform infrastructure, not customer DNS zones. | Customer zone/record lifecycle, ownership/verification, record schemas, import identity and any billing/limits. Do not expose platform Cloudflare administration as customer DNS management. |
| Bare metal | Deployment UI waits 800 ms and reports success without a provisioning request (`app/routes/client/dedicated-servers/deploy.tsx:933`). Inventory/stock operations and legacy server power actions exist; these do not create a tenant-owned server (`portal-client-api/app/routes/inventory/baremetal.py`; `app/routes/servers.py:395`). | Inventory offers/capacity, order/create with canonical plan/site/image/access, stable server ID/read, asynchronous provisioning/errors, cancellation/termination and commitment billing. Hardware inventory CRUD is not an instance resource. |

These are product/backend delivery dependencies. A Terraform resource with a fake successful create would not complete them.

## Billing and Add Credits remain separate API work

The provider already exposes public billing eligibility and uses fresh purchase checks. Every new billable product must keep the authoritative backend admission, pricing, quota and tenant checks; the provider must not reproduce wallet arithmetic or grant itself credits.

Complete public account/currency/quote and scoped payment-attempt/status contracts before implementing automation for Add Credits. Preserve idempotency, checkout/required user authentication, webhook confirmation and ledger credit; never initiate a payment during plan/refresh. Existing portal/session payment routes are not evidence of a public platform-token payment contract. The earlier `PORTAL_PARITY_ASSESSMENT.md` contains the payment requirements and product-wide acceptance matrix.

## Implementation order and completion evidence

1. **Now:** finish provider/block/CDN code against the real implemented contracts; keep route-specific deployment gaps visible. Reconcile OpenAPI/gateway manifests with the already deployed route families.
2. **Next:** publish block catalog and correct multi-currency admission, then expose SSH keys, registry and email using their implemented domain services.
3. **Then:** repair and expose ISO lifecycle, implement standalone certificates/DNS/bare metal where product behavior is missing, and publish explicit funding APIs.
4. For each resource, require create → unchanged plan → portal drift → import → supported update/replacement → safe destroy, plus denied/malformed billing, wrong tenant, pending/failed operations and retry identity recovery. Use local mocks first; schedule authorized live mutations separately.

A category is complete only when the backend operation exists, the public token can reach the authorized route, canonical state can be refreshed/imported, billing and quotas are enforced server-side, and lifecycle validation passes. Source-ready code and confirmed deployment are tracked separately here.
