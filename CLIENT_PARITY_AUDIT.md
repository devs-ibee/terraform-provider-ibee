# SDK and CLI parity audit

Date: 2026-09-28. Source and test audit of the current local `ibee-python`, `ibee-typescript`, and `ibee-cli` workspaces against the Terraform provider and reviewed portal/native contracts. Pre-existing local changes were preserved. Live production results are in [CROSS_CLIENT_RESULTS.md](CROSS_CLIENT_RESULTS.md).

## Release assessment

The focused CLI reliability fix changes **8 files**, with **40 local tests passing** and production verification of VM listing and operation completion. It is applied locally but not yet committed or published from the CLI repository.

The SDKs and CLI are **not ready to advertise full portal/Terraform feature parity**. Of the provider's **34 registered resource types, at least 16 have no dedicated SDK/CLI surface**: 4 storage configuration types (CORS, lifecycle, notifications, retention); 4 CDN types; and 8 compute extras (cloud/GPU snapshots, cloud/GPU backup policies, cloud/GPU volume attachments, standalone block volumes and their attachments). CDN purge/domain verification actions are also absent. Existing basic methods cover the other products, but that count does not establish equivalent billing, safety, waiting, import or live lifecycle behavior. Typed compute term/deletion policy, credential lifecycle/validation and admission orchestration still need correction before that parity claim.

Production verification after applying this patch:

- Actual CLI source has the 8-file patch; all 40 tests pass on Python 3.10.12.
- `vms list --json` produces JSON objects and includes the disposable test VM.
- `stop --wait` now completes immediately for live `status=succeeded`, returns exit 0 and emits parseable JSON.
- The old process demonstrably waited 300 seconds, printed “Stop still succeeded”, and returned exit 0. The patch fixes both terminal recognition and incomplete-operation exit semantics.

## Local verification

| Client | Existing checks | Result |
| --- | --- | --- |
| Python SDK | Python 3.12, `PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=src python -m pytest -p no:cacheprovider tests src/ibee/tests -q` | 19 passed, 3 optional transport tests skipped. System Python 3.9 was unsuitable; used the existing billing-service Python 3.12 environment without installing into it. |
| TypeScript SDK | `npm run build` then `npm test` in `an isolated temporary snapshot`, with source copied and existing dependencies linked | Build passed; 20 mocked tests passed. |
| CLI | Python 3.10, `PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=src:../ibee-python/src python -m pytest -p no:cacheprovider tests -q` | Original 13 tests passed. Used `python3.10`. |
| Proposed CLI patch | Same CLI command in isolated `after` snapshot | 40 passed: 13 existing tests plus 27 regression cases. New regression suite against unchanged `before` snapshot: 21 failed, 6 passed. |

Inherited `IBEE_TOKEN`, `IBEE_API_TOKEN`, `IBEE_WORKSPACE_ID`, `IBEE_ENV` and `IBEE_BASE_URL` were removed for tests. Tests use mocked transports/clients only. Passing existing tests does not establish lifecycle or deployed API parity: several tests explicitly encode omitted bucket region and DELETE-based credential revocation.

## Product coverage

| Product | Python SDK | TypeScript SDK | CLI | Difference from current provider/portal |
| --- | --- | --- | --- | --- |
| Buckets | Create/list/get/visibility update/delete; Object Lock, default retention and tags accepted at create | Same convenience methods and generated contract | CRUD; create only exposes region/public access | No clients expose CORS, lifecycle, notifications, or standalone retention read/update. The provider implements those configurations. Native region is required; clients/documentation assume optional automatic placement. Use a region valid for the selected environment. |
| S3 credentials | Create/list/get and method called revoke | Same | Credential subcommands | Revoke currently sends DELETE, which permanently deletes metadata; native explicit revoke is POST `/{id}/revoke`. Permission is optional/free-form despite native required enum. CLI omitted name becomes null. SDK scope defaults can be expansive. Python response metadata lacks declared ACL/tenant fields (extras survive at runtime); TS convenience revoke type incorrectly declares `access_key_id/revoked/status`, while actual response is `success/message`. |
| CDN | No resource client | No resource client | No group | Missing distribution, origin, website, domain, purge and validation APIs already implemented in the provider. |
| Secret manager | Store list/create/get/update/archive; secret list/create/get/delete/value-read/value-update with CAS | Same, but convenience values are typed `Record<string,string>` instead of the API's JSON object | Same commands; output bug patched | Basic lifecycle available. Recovery, historical versions, rollback, batch ingest and native identity/scope operations are not represented. Identity public routing gaps documented in `STORAGE_FEATURE_MATRIX.md` still apply; these are not all Terraform-resource omissions. |
| Networking | 47 public operations across VPC/subnet/node/NAT/port forwarding, reserved IP, firewall, L4/L7 LB | Generated clients exposed through convenience getters | Generic allowlisted bridge for all 47 operations, plus discovery shortcuts | Broad original public contract coverage. No client composes canonical pricing/admission/operation completion like Terraform; API accepted states must be polled/read by callers. Method surface does not prove deployed route behavior. |
| Compute | Cloud/GPU create/list/get/delete/start/stop/reboot/metrics/operation reads; catalog discovery | Same convenience and generated APIs | Same, optional wait | Missing typed `billing_catalog`/selected billing term and delete `public_ip_action`; missing snapshot, backup-policy, volume and attachment APIs present in provider. CLI sizes are defaults/inputs, not resolved from selected plan. |
| Billing | Explicit eligibility method | Explicit eligibility method | Explicit `billing eligibility SKU [--cost-minor ...]` | No automatic create preflight in these clients. CLI requires a SKU and cannot issue the SKU-free account check used by provider storage. No client implements credit checkout/top-up. Eligibility is a decision, not funding or a reservation. |

## Proven defects and the bounded patch

1. Cloud/GPU SDK lists return bare arrays. CLI table code looked only for `.items`/`.vms`, so nonempty responses displayed “none found”. JSON rendering converted models inside lists into strings. Fixed with array handling and recursive model serialization.
2. `secrets list` read `Secret.name`; the actual model exposes `secret_name`. Fixed and tested with the real SDK model.
3. A timed-out `--wait` returned exit 0; `ops get --wait` returned exit 0 even on failure. Fixed to return nonzero unless the terminal operation succeeded. The remote operation is not cancelled.
4. Python's open operation-status union does not normalize `succeeded`. Portal source aliases `succeeded/success/completed`, and the provider accepts those. The original CLI polled successful `succeeded` operations until timeout. The patch shares terminal success/failure sets, includes cancelled/canceled, and tests immediate return without a sleep.
5. Successful `--json --wait` appended prose on stdout. The patch sends operation prose/diagnostics to stderr, preserving a single JSON document on stdout.

The reviewed 8-file patch was applied to the actual CLI repository and all 40 tests passed. README documents the unchanged typed-compute and credential-name gaps.

Remaining concrete issues outside this patch:

- CLI credential creation sends `name:null` when `--name` is omitted, even when permission/bucket are explicitly supplied. Native `CredentialCreateRequest.name` rejects null. Omitted permission and allowed-buckets also serialize null. Supply all scoped-credential inputs until fixed; change the CLI to omit absent optional fields and validate permission/scope rather than broadening access.
- CLI secret-store update similarly passes unspecified options as null instead of omitting them. This differs from the SDK's OMIT sentinel and should receive explicit patch/null contract tests.
- CLI labels every 403 “missing required scope”, misclassifying billing denials. Error output includes raw API bodies; secret input parsing can echo a malformed value fragment. Redaction/error classification should be reviewed before expanding secret operations.
- Billing model validation is weaker than Terraform's gate: Python coerces `allowed="true"` to bool and makes metadata optional; TypeScript convenience returns unchecked JSON. CLI's explicit denied eligibility query exits 0, so `billing eligibility ... && create ...` is not a safe admission script.
- TypeScript has two error families: convenience methods throw exported `ApiError`; generated networking throws `IbeeApiError` subclasses. A handler checking only `instanceof ApiError` will miss networking failures.

## Exact invocation/configuration notes

Use the selected environment's endpoint explicitly. Production is `https://api.ibee.ai/v1`; development is `https://api.ibee.co.in/v1`. The production validation has separately established production bucket region `in-south-1`; development results must not be treated as production placement guarantees.

Python constructors are `Ibee(token=token, base_url=endpoint, max_retries=0)` and `AsyncIbee(...)`. `base_url` overrides `environment`; SDKs do not automatically load Terraform config or CLI environment variables. Scope is per request:

```python
client.object_storage.create_bucket(workspace_id=ws, name=name, region=region)
client.object_storage.get_bucket(name, workspace_id=ws)
client.object_storage.update_bucket(name, workspace_id=ws, is_public=False)
client.object_storage.delete_bucket(name, workspace_id=ws)

client.vpcs.create_vpc(workspace_id=ws, name=name, site_id=site_id,
                      cidr=cidr, auto_cidr=False, create_default_subnet=False)
client.vpcs.get_vpc(vpc_id, workspace_id=ws)
client.vpcs.update_vpc(vpc_id, workspace_id=ws, name=new_name)
client.vpcs.delete_vpc(vpc_id, workspace_id=ws)
```

Explicit `create_default_subnet=False` prevents an incidental subnet in a disposable VPC test; applications may deliberately opt into one and own its cleanup. Site IDs come from networking discovery, not region names.

Python compute create requires `workspace_id`, `idempotency_key`, `name`, `os_distro`, `os_type`, `template_id`, `cpu`, `ram_mb`, and `plan_id`; GPU also requires `gpu_count` and `gpu_model`. Its verified escape hatch is `request_options={"additional_body_parameters":{"billing_catalog": selected_canonical_term}}`. Delete supports the same request-options mechanism for `{"public_ip_action":"release"}`. These escape hatches do not validate price selection; use the already resolved canonical catalog/term, never an invented price.

TypeScript configuration is `new Ibee({token, baseUrl:endpoint})`; `baseUrl` overrides `environment`. Convenience storage uses camelCase:

```typescript
await client.objectStorage.createBucket({workspaceId:ws, name, region});
await client.objectStorage.getBucket({workspaceId:ws, bucketName:name});
await client.objectStorage.updateBucket({workspaceId:ws, bucketName:name, isPublic:false});
await client.objectStorage.deleteBucket({workspaceId:ws, bucketName:name});
await client.vpcs.createVpc({workspace_id:ws, name, site_id:siteId,
  cidr, auto_cidr:false, create_default_subnet:false}, {maxRetries:0});
await client.vpcs.getVpc({workspace_id:ws, vpc_id:vpcId});
await client.vpcs.updateVpc({workspace_id:ws, vpc_id:vpcId, name:newName}, {maxRetries:0});
await client.vpcs.deleteVpc({workspace_id:ws, vpc_id:vpcId}, {maxRetries:0});
```

Networking getters expose generated snake_case request fields. VM convenience create takes `{workspaceId,idempotencyKey?,...snake_case_body}`. It forwards extra fields from a prepared variable at runtime, but the request type lacks billing_catalog. VM delete convenience forwards no body. `client.api.fetch(relativePathWithWorkspaceQuery, init, {maxRetries:0})` is the supported authenticated passthrough if an unmodeled body/route is needed; callers must inspect its standard Response.

CLI global flags precede the group; provide token through `IBEE_TOKEN` or `IBEE_API_TOKEN`, not shell arguments. `--base-url` / `IBEE_BASE_URL` override `--dev` / `IBEE_ENV`; `--workspace` overrides `IBEE_WORKSPACE_ID`. The CLI does not read `IBEE_ENDPOINT`.

```sh
ibee --base-url https://api.ibee.ai/v1 --workspace WORKSPACE --json buckets get NAME
ibee --base-url https://api.ibee.ai/v1 --workspace WORKSPACE buckets update NAME --private
ibee --base-url https://api.ibee.ai/v1 --workspace WORKSPACE buckets delete NAME --yes
ibee --workspace WORKSPACE networking call create_vpc --data '{"name":"test","site_id":"SITE","cidr":"10.92.0.0/24","auto_cidr":false,"create_default_subnet":false}'
ibee --workspace WORKSPACE networking call delete_vpc --data '{"vpc_id":"VPC"}' --yes
```

Python defaults to two retries. TypeScript convenience resources do not retry, but generated networking defaults to two. For tests/non-idempotent creates, disable automatic retry and reconcile ambiguous responses before resubmitting. Compute calls should retain a stable idempotency key for retry of the same intended operation.

## Next bounded work

The validation owner has applied and live-checked the CLI patch. Next update the shared OpenAPI contract and regenerate SDKs for actual region/credential semantics, compute billing/delete policy and new resource APIs. Add create/read/update/delete route fixtures matching canonical native responses and cross-client contract tests. Keep provisioning orchestration/admission separate from thin HTTP methods, but expose a documented safe path that resolves trusted catalog sizing/term, checks eligibility, submits once, waits and confirms canonical state. Payment checkout and secret recovery remain explicit operations with their own authorization and contracts.
