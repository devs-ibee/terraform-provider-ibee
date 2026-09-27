# Networking portal parity and verification

Reviewed on 2026-09-27 against the public OpenAPI and route map in `platform-docs`, `network-orchestration-service`, and portal UI/API snapshots at `portal-client-ui and portal-client-api snapshots` (UI commit `151077f`). This is a feature inventory, not a claim that every portal action is available or has passed a live test.

`CLI mock` means a real Terraform CLI runs the provider against a local HTTP fixture through create, refresh/no-op, import, supported update, drift, and destroy. It does not test provisioning, packets, guest networking, billing settlement, certificate issuance, or backend enforcement. `Focused mock` tests exercise validation or failure/ownership paths directly. Live results belong in the live-test report; network provisioning remains site- and quota-dependent.

| Portal feature | Terraform mapping / current result | Contract and test scope |
| --- | --- | --- |
| Discover networking-enabled sites | `ibee_network_sites` data source: ID, name, available, message | `/networking/sites`; distinct from compute `ibee_sites`. Strict typed-array and workspace query tests. Compute site availability does not imply VPC availability. |
| Create/read/import/delete VPC | `ibee_vpc` | Public lifecycle; CLI mock. Delete inventories subnets before mutation and refuses unmanaged children. Focused tests cover malformed inventory and unmanaged preservation. Backend must add an atomic empty-child check to eliminate a concurrent-create race. |
| VPC name and description | Mutable `name` and `description` | Public PATCH; name update covered in CLI mock. |
| Automatic or explicit VPC CIDR | `cidr`, `auto_cidr` | Immutable allocation; replacing CIDR replaces VPC. CLI mock uses explicit CIDR; automatic allocation validation is covered separately. |
| Create default subnet / custom default CIDR | `create_default_subnet`, `default_subnet_cidr` | Create-only. Provider owns only a default child recorded from its own successful create. Imports do not take ownership. Focused custom-CIDR test. |
| VPC connectivity selection | `connectivity_type=public/private/nat_gateway`, preserving legacy public default. Compound NAT is owned by the VPC; forwarding references `default_nat_gateway_id`. | Portal source sends private unchanged, while the reviewed older public spec only lists public/nat_gateway. Live private compatibility is recorded in LIVE_NETWORK_RESULTS.md. Focused tests verify exact connectivity confirmation, computed fields on update, and ownership. Failed creates report candidates without adopting them. |
| VPC default designation/region override | Not exposed | Public create includes `is_default` and `region`; no matching portal creation control observed. Provider uses networking site. |
| Subnet create/read/import/delete | `ibee_vpc_subnet` | CLI mock plus strict ID/state tests. Destroy is scoped to the managed subnet. |
| Subnet name, DNS | Mutable `name`, `dns` | Public PUT/PATCH contract; DNS IPv4 validation; CLI mock changes DNS to `9.9.9.9`. |
| Subnet explicit CIDR / automatic prefix | `cidr` or `prefix_length`, computed `gateway` | Mutually exclusive; `/22`–`/29` automatic prefix. CLI mock verifies prefix replacement allocates a fresh CIDR and reaches no-op. |
| Attach VM to VPC subnet / detach | `ibee_vpc_node_attachment`: VM, subnet, connectivity, optional reserved IP; computed network details | Public node contract; CLI mock covers private attachment, import, detach. Other connectivity choices need environment-specific live coverage. |
| Primary/secondary interfaces, promote interface, requested private address, default route recovery and guest configuration | Not mapped to low-level node resource | **Public-contract gap:** portal VM attachment orchestration accepts modes and fields absent from reviewed public node schema/routes. No claim of full interface topology parity. |
| NAT gateway create/read/import/delete | New managed NAT: use `ibee_vpc.connectivity_type=nat_gateway` and its computed default ID. `ibee_nat_gateway` supports separately imported/recreated gateways. | Live standalone create on legacy public VPC returned409: NAT requires a NAT-mode VPC. The corrected example uses compound creation and avoids duplicate ownership. Standalone create refuses existing NATs. Both deletion paths block attached nodes/forwarding rules; imported VPC never adopts NAT ownership. Local mocks cover these guards; live capacity result is in LIVE_NETWORK_RESULTS.md. |
| NAT public-IP swap, reserve/release choice on deletion | Not exposed | **Public-contract gap:** portal dedicated swap action and delete `public_ip_action` are absent from published lifecycle contract. Provider follows public delete semantics. |
| NAT port forwarding | `ibee_nat_port_forwarding_rule`: name, TCP/UDP, external/internal ports, internal IPv4, note, enabled | Public CRUD; CLI mock uses TCP and update. Focused validation covers invalid addresses/ports. UDP needs its own live packet test. |
| Forward to private VIP / MetalLB announcers | Not exposed | **Public-contract gap:** portal `target_type`, `target_vm_ids`, reserved private VIP creation, announcers, and VIP public-IP attach/detach have no reviewed public routes. |
| Reserve/read/import/release public IPv4 | `ibee_reserved_ip`: site and label, computed address/status | Public lifecycle; CLI mock. Allocation/release and billing need live coverage. |
| Rename reserved IP / reverse DNS | Mutable label and `reverse_dns` | Public PATCH; basic lifecycle mock covers label. DNS propagation is not tested. |
| Attach/detach reserved IP without releasing it | `ibee_reserved_ip_attachment`: reserved IP, VM, optional VPC/subnet | Public `/attach` and `/detach`; CLI mock. Ownership guard refuses detaching an address moved to another VM. Missing attachment metadata is an error, not a detached assumption. |
| Move reserved IP between VMs | In-place VM update uses atomic public `/move` | CLI mock and focused test verify old ownership before POST, retained address identity, and updated target. |
| Direct provider-network VM attachment / conversion from platform allocation | Not exposed as equivalent to VPC attachment | **Public-contract gap:** source direct-provider/convert routes are not in reviewed public map. Do not imply `/attach` provides the portal's complete primary provider-interface flow. |
| Firewall groups | `ibee_firewall_group`: name, description, rule count; create/read/import/delete | CLI mock and root live group create/reconcile/import succeeded. Name/description edits replace because group update is absent from public API. |
| Firewall rules | `ibee_firewall_rule`: ingress/egress, TCP/UDP/ICMP/any, ports, targets, allow/drop, priority, description, enabled | CLI mock + deterministic identity, malformed response, disabled-create tests. Live disabled TCP rule create/no-op/port update passed. Every protocol/action combination and packet enforcement is not individually live-tested. |
| Disabled rule creation | `enabled=false` | API requires POST then PATCH; not an atomic disabled create. Make attachments depend on rule resources to avoid a transient active rule on attached VMs. |
| Portal firewall presentation | No provider workaround needed | UI `toUiRule` hides disabled and egress rules while group count can use total API count. A group count of one with only default deny displayed is expected for a disabled ingress rule. |
| Attach/detach firewall group | `ibee_firewall_attachment` | Paginated association reads, ownership, CRUD/import CLI mock. Packet enforcement not tested. |
| L4 TCP load balancer | `ibee_load_balancer_l4` | CLI mock; name, backends, endpoints, readiness, import, delete. |
| L4 TLS passthrough | `protocol=tls_passthrough` | Derives portal-equivalent `tls.mode=passthrough`, `certificate_source=managed` on create. Focused payload tests and CLI replacement passed; encrypted traffic not tested. |
| L7 HTTP / HTTPS | `ibee_load_balancer_l7` | HTTPS derives `tls.mode=terminate`, `certificate_source=managed` as portal does. CLI replacement and focused tests; certificate issuance/HTTPS request not tested. |
| LB backends | Type, target, port, weight, backend TLS | Typed schema for IP, hostname, service. CLI fixture uses IP; hostname/service routing need focused environment coverage. |
| L7 path/header routing rules | Priority, path prefix, headers, per-rule backends | Typed list defaults/state conversion + CLI fixture. Real HTTP routing not tested. |
| LB custom domain / DNS validation | Not exposed | **Read-contract alignment gap:** request/source supports custom domain, but published response omits it. Source response includes hostname/CNAME; publish/verify full read contract before managing drift and DNS validation. |
| LB balancing algorithm / sticky header | Not exposed | Request accepts routing but GET response model omits it; cannot safely refresh/import/drift-detect. **Read-contract gap.** |
| LB timeouts, proxy protocol, retry, active/passive health checks, observability logs | Not exposed | Portal/source creation fields exceed reviewed public request schema; GET response omits these settings. **Public request/read-contract gaps.** |
| Custom TLS certificate | Not exposed | Gateway implementation explicitly rejects custom certificate source on the shared HTTPS listener. **Backend limitation.** |
| List/filter/search tables, detail status and endpoints | Resource state/import + networking sites data source | UI-only filtering is not a managed resource. Dedicated VPC/LB/IP discovery data sources and full condition/event telemetry are not implemented. |
| Quotas, account admission, product pricing | Billable create preflights use shared billing eligibility | Public networking create lacks trusted SKU/quote. Empty-SKU checks cover account status only; product service remains authoritative. Portal billing catalog payload and full cost/credit parity are not claimed. Read/delete do not perform creation admission. |

## Source pointers

- `platform-docs/fern/openapi/ibee-cloud.yaml`: public networking paths and VPC/subnet/NAT/public-IP/firewall/LB schemas. `platform-docs/gateway/source/public-api.route-map.yaml`: external route inventory.
- `network-orchestration-service/app/vpc/service.py`: supported site gating, VPC creation, implicit NAT creation, and cascading deletion (around lines 114–153, 321–398, 481–502). `app/vpc/models.py`: connectivity enum and subnet schema.
- Portal UI `app/routes/client/vpcs.create.tsx` and `vpcs.$vpcId.tsx`: connectivity options, VM interface operations, NAT IP operations, port forwarding/VIP fields.
- Portal UI `app/routes/client/reserved-ips*.tsx`: reserved-IP management and direct attachment workflows.
- Portal UI `app/routes/client/firewalls.tsx`: `toUiRule`/`mapGroup` and hidden disabled/egress behavior.
- Portal UI `app/routes/client/load-balancer/create.tsx` around 1187–1201: exact managed TLS payload. Networking `app/models/load_balancer.py` around 283–363 and 477–518: create validation versus read response; `app/integrations/k8s/gateway.py` around 668–672: managed-only HTTPS listener.

## Verification artifacts

- `internal/provider/network_resource_test.go`: ownership, identity, malformed responses, waits, safe deletion and billing failure cases.
- `internal/provider/network_feature_parity_test.go`: VPC cascade guard, custom default CIDR, subnet DNS/prefix validation, managed TLS payloads, atomic IP move.
- `internal/provider/network_sites_data_source_test.go`: network-specific site discovery and workspace scoping.
- `internal/provider/terraform_network_lifecycle_test.go`: `TestTerraformLifecycleNetworking` runs all twelve resources with real Terraform against local mocks; includes imports, updates, protocol/prefix replacement, drift correction, denied-billing cleanup and no-op plans.

Latest networking-only verification: focused Go tests passed; `IBEE_TF_TEST=1 go test ./internal/provider -run '^TestTerraformLifecycleNetworking$' -count=1 -timeout=5m` passed. This matrix must remain explicit about untested protocol variants and data-plane/backend gaps until those checks are performed.
