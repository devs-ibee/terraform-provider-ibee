# IBEE Terraform: full portal and billing parity

> Baseline assessment before implementation. For current code coverage and remaining dependencies, see [COVERAGE.md](COVERAGE.md).

Assessment date: 2026-09-27. Scope: every product/tool in the current portal navigation, plus the account, billing, and payment behavior needed to operate it consistently through Terraform.

## Outcome

The current six-resource provider is a prototype. Full portal parity needs coordinated work in the provider, public API/gateway, SDK contracts, and product services. Adding Terraform resource wrappers alone cannot provide that parity.

The existing Billing service should remain authoritative for credits, affordability, suspension, and resource allowances. Terraform must use public, API-token-authenticated endpoints and report those decisions, while each product service repeats the relevant checks before committing a mutation. Payment processing and ledger updates remain in Billing.

All 19 portal product/tool categories are included below. Their presence in navigation is not proof of production availability: navigation is feature-flagged per organization, DNS currently routes to a coming-soon page, and the reviewed bare-metal submit handler does not call a provisioning API.

## Evidence and limits

- Inventoried 17 IBEE repositories accessible through the connected GitHub accounts. They include `portal-client-ui`, `portal-client-api`, `billing-service`, `auth-service`, `virtualization-engine`, `persistentblock-service`, `object-storage`, `network-orchestration-service`, `cloudimage-vault`, `secret-store`, `container-registry`, `platform-docs`, and supporting administration/notification/support/website repositories.
- Reviewed current remote default-branch files for portal navigation, feature visibility, billing checks and top-ups, Billing admission/enforcement/payment flows, public API definitions/routing, and registry/secret-store admission clients. Supplemented with local provider and product-service source inspection. This is not a line-by-line audit of all repositories.
- Most application repositories default to `dev`; `platform-docs` defaults to `main`. Local branches differ from those defaults. Findings are source-level, not certification of a deployed environment.
- Working recommendation: target production behavior and run acceptance tests in development. Confirm the production deployment revisions and enabled product set before declaring parity.
- The reviewed public OpenAPI defines 145 operations. It covers only part of the portal's surface.
- No resources were provisioned, payments initiated, credits added, infrastructure destroyed, or releases published during this review.

## Complete product matrix

Proposed names are design candidates, not implemented resources. “Contract exists” means operations were found in the reviewed public OpenAPI, not that live behavior is verified. Existing provider implementations still need the hardening described in `TERRAFORM_ROADMAP.md`.

| Portal category | Current Terraform | Public contract and proposed work | Required product behavior |
| --- | --- | --- | --- |
| Cloud VMs | `ibee_cloud_vm` prototype | CRUD subset, catalog, resize/access/lifecycle operations exist. Add SSH inputs, import, refresh, supported updates, and relevant attachments. | Resolve trusted plan, site, image, interval and billable add-ons; check creation/capacity changes; track asynchronous operations and partial failures. |
| GPU VMs | Missing | VM and lifecycle contracts exist; add `ibee_gpu_vm`. | GPU model/count/availability, trusted pricing, quotas, billing eligibility, and operation recovery. |
| Bare Metal Servers | Missing | No public lifecycle contract found. Candidate `ibee_bare_metal_server` requires a real order/provision/read/delete API first. | Inventory reservation, billing/commitment policy, provisioning status and cancellation. The reviewed portal submit handler waits 800 ms and shows a success message; it does not submit a provisioning request. |
| Object Storage | Missing | Bucket create/list/get/update/delete and credential operations exist. Add `ibee_bucket`; scope additional configuration separately. | Usage-based admission, bucket policy/region/defaults, nonempty deletion policy, and deliberate credential-state handling. |
| Block Storage | Missing | VM attach/detach operations exist; standalone volume CRUD is absent from this public spec. Publish volume/catalog lifecycle before adding `ibee_volume`. | Size/performance pricing, capacity-increase admission, attachment ownership, resize restrictions, and deletion behavior. |
| VPC | VPC, subnet, VM attachment prototypes | Contracts include VPCs, subnets, nodes, NAT gateways and forwarding rules. Complete coverage with explicit ownership. | VPC/subnet/NAT quotas, compatible placement, billable network components, and safe child deletion. |
| Reserved IPs | Missing | Reserve/read/update/release/attach/detach/move contracts exist. Add an IP resource and attachment resource. | Site/type/catalog selection, reserve/release charges, and VM deletion retain/release policy. |
| Load Balancer | Missing | L4/L7 create/read/update/delete/status contracts exist. Add L4/L7 resources or a carefully validated combined schema. | Trusted size/protocol pricing, backend dependencies, readiness polling, and TLS/certificate ownership. |
| DNS | Missing | Portal workspace DNS route points to `coming-soon.tsx`; no public zone/record API found. Future zone/record resources depend on product delivery. | Zone ownership, supported record types, verification and any plan limits. Do not label current DNS Terraform support as available. |
| CDN | Missing | Portal routes exist; no distribution/custom-domain public contract found. Publish it before adding CDN resources. | Origin ownership, domain verification, usage admission, certificate readiness, and cleanup of partially created origins. |
| Firewalls | Group/rule prototypes | Group/rule/attachment contracts exist. Add firewall attachment and fix rule identity/refresh. | Resource permissions, rule limits, attachment ownership, safe concurrent rule creation, and full drift detection. |
| SSL Certificates | Missing | Portal route exists; public certificate lifecycle is absent. Determine issue/import/read/renew/delete behavior. | Domain validation, renewal ownership, sensitive key material, and any certificate charges. |
| Backups | Missing | Cloud/GPU backup policy, runs, and restore operations exist. Prefer backup-policy resources; treat one-off run/restore deliberately as actions. | Storage pricing, retention, schedule/time zone, recovery readiness, and policy disable versus backup deletion. |
| Snapshots | Missing | Cloud/GPU snapshot create/list/get/delete/restore contracts exist. Add snapshot resources where stable lifecycle permits. | Snapshot billing/size, consistency, protection rules, async completion, and explicit restore actions. |
| ISOs | Missing | Image discovery exists; ISO upload/management is absent from the public contract. Add ISO management only after publication. | Upload limits, checksum validation, image readiness, storage charges and safe detach/delete. |
| SSH Keys | Missing; VM key IDs also missing from provider schema | VM create accepts `ssh_key_ids`; no public standalone key CRUD found. Add VM references first, then `ibee_ssh_key` after API publication. | Scope keys to the correct tenant and store public keys only; keep account verification/permissions authoritative. |
| Email Service | Missing | Portal control-plane pages and local service exist; no email control-plane routes in this public spec. Candidate domain/webhook resources need a published contract. | Domain/DNS verification, sending limits, product entitlement/billing enforcement, async readiness, and distinct secret handling. Message sending is a data-plane operation, not a persistent infrastructure resource. |
| Secret Manager | Missing | Store/secret CRUD/value/archive operations exist. Add store resources and deliberately designed secret lifecycle support. | Admission and quotas, archive semantics, version/rotation behavior, and Terraform state confidentiality. Verify deployment of the service's eligibility feature flag. |
| Container Registry | Missing | Portal and registry service exist; registry provisioning/plan/credential APIs are absent from this public spec. Publish them first. | Registry service already validates canonical product/SKU/site/quota and eligibility. Preserve server-derived capacity/pricing, upgrade rules, reconciliation and credential revocation. |

The matrix is grounded in the portal's [19 feature entries](https://github.com/IBEE/portal-client-ui/blob/dev/app/lib/featureVisibility.ts), [route definitions](https://github.com/IBEE/portal-client-ui/blob/dev/app/routes.ts), and [public API specification](https://github.com/IBEE/platform-docs/blob/main/fern/openapi/ibee-cloud.yaml). The bare-metal dependency is visible in [its current submit handler](https://github.com/IBEE/portal-client-ui/blob/dev/app/routes/client/dedicated-servers/deploy.tsx#L914).

## Account and organization parity

These are cross-product requirements, not additional billable resource categories:

| Area | Terraform/API requirement |
| --- | --- |
| Identity and permissions | API token scopes, organization membership, workspace access, verified-identity requirements, product write permissions, and revoked-token behavior must be enforced at the public API boundary. A portal button being disabled is not API enforcement. |
| Organization and workspaces | Resolve resource ownership from the authenticated API token plus workspace selection. Wallet and billing profile are organization-scoped; multiple workspaces consume the same organization's allowance. Publish workspace management if it is to become a Terraform resource. |
| Product availability | Read effective entitlements/availability from a supported API. Portal navigation visibility is useful evidence of product categories, but it is not a substitute for a server-side entitlement check. |
| Quotas | Product services enforce authoritative counts/capacity against the organization's allowance. Terraform may expose read-only limits for planning; it must not invent limits or silently change a tier. |
| Billing profile, wallet, usage, invoices | Add read-only public summaries first. Avoid reads that initialize billing accounts, create wallets, or otherwise mutate state: the local portal account-summary implementation includes provisioning fallbacks and should not simply be wrapped as a Terraform read. |
| Team/API tokens | Add narrowly scoped membership/token resources only after their public contracts, privilege checks, import behavior, and secret lifecycle are defined. |
| Verification, postpaid applications, support | Preserve portal workflows for user identity/document submission, credit approval, support requests, and interactive recovery. Terraform should return the required next action rather than grant itself approval. |

## Shared billing behavior

The public endpoint already exists in the contract:

```text
POST /v1/billing/resource-eligibility?workspace_id=...
Authorization: Bearer <IBEE API token>
Required scope: billing.read
```

The current remote route map sends it directly to Billing's public admission endpoint. Billing calls the same `ResourceEligibilityService` used internally. The portal-client API also contains an eligibility proxy; do not use browser session endpoints or internal service headers from the Terraform provider. [Gateway route](https://github.com/IBEE/platform-docs/blob/main/gateway/source/public-api.route-map.yaml#L87), [Billing public handler](https://github.com/IBEE/billing-service/blob/dev/app/api/v1/public_billing.py).

The Terraform implementation should follow this sequence:

1. Resolve token scope and workspace ownership; preserve the organization's identity returned by the trusted boundary.
2. Resolve actual product/SKU/site/quantity/interval and add-ons from an authoritative catalog or quote. Respect organization-specific pricing and currency. A base VM SKU alone may not describe public IP, OS license, disk, or backup cost.
3. Perform a fresh eligibility check immediately before each billable create or expansion. Continue only when `allowed` is a JSON boolean `true`. A saved plan or an earlier successful check is not approval for a later request.
4. Call the product API with one stable idempotency key for retries of that logical mutation. The product service must recheck eligibility, entitlement and quota, and use authoritative admission/reservation where concurrency requires it.
5. Preserve returned IDs, poll the operation, surface billing/authorization/quota errors distinctly, and keep recoverable state on partial failure.
6. Read back canonical state and verify an unchanged second plan.

Admission is a point-in-time decision; it does **not** reserve funds. With parallel Terraform resources, multiple preflights can pass against the same balance. Provider-side serialization is not a substitute for backend concurrency control across the portal, SDK, and other Terraform runs.

The current Billing implementation considers prepaid initial-top-up history/promo balance, paid and promotional spendable balances, accrued uncollected usage, postpaid headroom, SKU validity, dunning and suspension. Reuse this implementation rather than copying its arithmetic or thresholds into Go. [Eligibility service](https://github.com/IBEE/billing-service/blob/dev/app/domain/enforcement/eligibility.py).

### Operation-specific restrictions

The remote internal billing contract now distinguishes create resource, create credential, increase capacity, mutate, read, delete, revoke credential, and recovery operations. The enforcement rules allow some cleanup/security operations even when new purchases are blocked. [Enforcement rules](https://github.com/IBEE/billing-service/blob/dev/app/domain/enforcement/service.py#L38).

Consequences for Terraform:

- Do not place a universal creation gate in provider `Configure`, refresh, import, or destroy.
- Do not interpret a denied read/HTTP 403 or billing lock as a missing resource and remove it from state.
- Use the authoritative operation-specific policy for updates, credential rotation, and destruction. Preserve state and actionable errors when restrictions prevent refresh or cleanup; do not bypass enforcement.
- The published OpenAPI request currently documents only `sku_code` and `estimated_cost_minor`. The internal request also accepts `operation`, and the internal response includes resource limits/enforcement details that the public response intentionally omits. Align the public schema, route behavior, permissions, generated clients, and tests before relying on these fields externally. The public handler sharing the internal model does not itself make undocumented fields a supported contract. [Schemas](https://github.com/IBEE/billing-service/blob/dev/app/domain/enforcement/schemas.py#L40).
- Confirm server enforcement for each product. The reviewed registry and secret-store code have explicit operation-specific admission clients; this review did not establish identical enforcement for every service or deployment.

### User-facing errors

Return the reason and recovery path without treating all failures as insufficient credit:

| Decision | Expected response |
| --- | --- |
| Initial top-up required / insufficient balance | Explain the block and direct the authorized user to the organization's Add Credits flow. |
| Credit limit exceeded / overdue billing | Explain the limit or settlement requirement; do not automatically buy credits or increase a limit. |
| Unknown/inactive SKU | Refresh catalog or select a valid plan; do not provision with invented pricing. |
| Quota/entitlement/permission denied | Report the actual restriction and authorized recovery route. |
| Manual restriction / suspension | Preserve the service decision; payment is not necessarily the permitted remedy. |
| Eligibility unavailable or malformed | Fail closed for the dependent purchase, with a retryable diagnostic where appropriate. |

## Add Credits and payment parity

The portal's Add Credits flow creates a payment attempt, completes a Stripe/Razorpay flow as selected by the backend, and waits for confirmation/ledger visibility. Billing checks top-up eligibility and currency-specific minimums, reuses idempotent attempts, and deduplicates successful gateway payments before crediting the account. Top-up success can trigger billing recovery. [Portal flow](https://github.com/IBEE/portal-client-ui/blob/dev/app/routes/client/billing/billing.tsx#L890), [Payment service](https://github.com/IBEE/billing-service/blob/dev/app/domain/payments/service.py#L139).

Recommended Terraform experience:

- Display a billing denial and an organization billing link when the API provides a permitted recovery action. Allow the user to complete Add Credits, then rerun apply; the provider performs a new eligibility check.
- Add read-only billing/account information through a supported public endpoint when available. A wallet balance is observed state, not a desired balance to continuously restore.
- If funding from automation is required, first publish a scoped checkout/payment-attempt/status contract. Model funding as an explicit one-time workflow or supported action, with a durable idempotency key, explicit amount/currency, backend payment confirmation and retry reconciliation. Its invocation must be deliberate; plan/refresh/repeated apply must not charge again.
- Keep card entry and any interactive authentication in the payment provider's approved checkout flow. Store payment references, not raw card details or reusable payment secrets, in Terraform state.
- Promo-code redemption also needs an explicit idempotent contract and redemption-status semantics. Administrative/manual credit grants, credit-limit overrides, tier overrides, and suspension overrides are not ordinary customer funding operations.
- Do not hardcode the portal's INR message as a universal minimum. Billing has distinct first-top-up and subsequent-top-up rules keyed by currency.
- The reviewed public OpenAPI exposes eligibility only under `/billing`; it does not yet expose wallet read, top-up initiation/confirmation/status, invoice or promo-code APIs. Portal billing URLs cannot simply be relabeled as public token APIs.

## Implementation work packages

| Order | Deliverable | Repositories involved | Completion evidence |
| --- | --- | --- | --- |
| 1 | Freeze the intended production product set and reconcile public billing/entitlement/catalog contracts. Define per-product operation, pricing and quota requirements. | `platform-docs`, `billing-service`, `auth-service`, `portal-client-api`, product owners | Versioned OpenAPI and route mapping, deployment revision inventory, permission and negative-case fixtures. |
| 2 | Shared Go billing admission/error/operation layer, first wired into cloud-VM create; fix existing lifecycle issues and add import. | This provider | A denied/unavailable check prevents purchase; successful checks do not become cached authorization; cleanup is not blocked by a creation-only check. |
| 3 | Complete existing-public-contract coverage: cloud/GPU VMs, networking/NAT/firewall/IP/LB, buckets, secrets, snapshots and backup policies. | Provider plus public API owners | Per-resource create/read/update-or-replace/import/destroy, no-op second plan, portal drift, partial-failure recovery, and product-specific billing tests. |
| 4 | Publish missing product contracts and implement their providers: volumes, registry, CDN/domains/certificates, ISO/SSH keys, email; bare metal and DNS after real lifecycle delivery. | Respective product services, API/gateway, docs/SDKs, provider | Identical operation/authorization behavior across portal/API/SDK/Terraform on the chosen deployed versions. |
| 5 | Public billing summaries and deliberate funding workflow, docs and signed Registry release. | Billing/API, SDK/CLI, provider/docs | Payment retry/confirmation tests, account isolation, clean Registry install, and documented feature support. |

Work packages can be released incrementally; every product stays on the scope list. Missing or unverified backend behavior is an explicit dependency, not an implemented Terraform feature.

## Release acceptance matrix

For each resource, exercise the same requests through the public API and portal-backed product service, then verify Terraform outcomes:

- Prepaid: new account, successful paid top-up, eligible promo balance, insufficient funds, accrued outstanding usage and held funds.
- Postpaid: within headroom, limit exceeded, overdue/dunning, and approved recovery.
- Permissions: correct/wrong workspace, missing product scope, missing billing read permission, revoked token, unverified identity, and product not enabled.
- Enforcement: create versus increase versus delete/revoke; manual and billing restrictions; blocked refresh must not become state deletion.
- Quotas/capacity: at-limit and parallel create/resize across different workspaces in the same organization.
- Lifecycle: import, canonical refresh, changes from portal/SDK, replacement, async failure, retries, cancellation, and unmanaged child preservation.
- Payments: duplicate initiation, pending/failed/cancelled payment, delayed/duplicate webhook, actual ledger credit, currency/minimum checks, and no duplicate charge on rerun.
- Costs: base product plus add-ons, interval, site, organization pricing, usage-based SKUs, and retain/release choices on destroy.

The immediate engineering step is to establish the shared billing/public-contract foundation, then use it in every relevant resource. This review expands the roadmap; the implementation of full product parity is still outstanding.
