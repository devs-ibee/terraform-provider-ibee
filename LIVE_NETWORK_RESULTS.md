# Live networking validation — 2026-09-27

Target: authorized development workspace; only disposable resources named `codex-tf-live-*`. Networking spending ceiling: ₹500 incremental actual spend. Credentials and resource IDs stay outside the repository. This report distinguishes real live results from local fixtures and does not claim every feature is verified.

| Feature / operation | Live result | Evidence / limitation |
| --- | --- | --- |
| Networking site discovery | Passed | Public GET returned Amaravati as available; compute site availability was not substituted. |
| VPC explicit CIDR conflict | Expected rejection | First create returned HTTP 409 for account-wide CIDR overlap. Workspace inventory remained empty; switched to server-assigned CIDR. |
| VPC and automatic-prefix subnet creation | Passed | Terraform created two resources, VPC reached available, subnet returned CIDR, gateway and DNS. |
| Initial refresh/no-change plan | Passed | `terraform plan -detailed-exitcode` returned 0. |
| VPC/subnet imports | Passed | Both resources imported using their documented IDs; post-import plan returned 0. |
| VPC and subnet rename | Passed | Reviewed plan showed two in-place changes. Apply succeeded; independent API readback matched the updated VPC name. |
| Subnet DNS update | Passed | Changed to `9.9.9.9` and `1.1.1.1`; apply and no-change plan passed. |
| Subnet automatic-prefix replacement | Passed | Changed /24 to /25; plan/apply replaced only the subnet. Readback confirmed /25 and a subsequent no-change plan. |
| Portal comparison | Passed via parent agent | Portal showed the disposable updated VPC active in Amaravati with matching CIDR and zero attached nodes. |
| Reserved IPv4 price review | Passed | Parent agent read Amaravati pricing from the live portal: ₹202/month and ₹0.28/hour. No price was inferred from billing admission. |
| Reserved IPv4 creation | Blocked by capacity | Terraform POST returned HTTP 409: no public IPv4 addresses available in the selected site. Reconciliation GET returned HTTP 200 with zero reserved IPs. No successful reservation. |
| Reserved-IP import/rename/reverse DNS/release/attach/move | Blocked | No allocated IP; attachment/move also need a usable disposable VM. No customer IP was used. |
| VM/VPC node and firewall attachments | Blocked by compute backend | Compute agent's disposable cloud VM failed because the Ubuntu source storage definition was missing. No usable VM was shared for attachment or traffic tests. |
| L4/L7 load balancer create/TLS/routing | Not run: missing authoritative price | The public catalog equivalents returned 404 and the portal create form did not display a price. Account billing admission is not a price quote; no unpriced paid create was attempted. |
| NAT pricing | Verified in portal | Parent agent read managed-NAT price ₹2,029/month and ₹2.78/hour, billed while the gateway exists. |
| Standalone NAT create on legacy public VPC | Rejected; provider gap fixed | HTTP 409: NAT gateways can only be created for NAT-mode VPCs. Reconciliation showed zero gateways and IPs. Provider now supports compound `connectivity_type=nat_gateway` VPC creation, owns only the returned default NAT, and exposes its ID for forwarding rules. The example no longer creates a duplicate NAT. |
| Corrected compound NAT / forwarding lifecycle | Not live-applied | Compound creation was planned only; no apply was performed. Focused mocks cover canonical NAT identity, updates, failed-create candidate diagnostics without auto-adoption, imported ownership, existing-gateway rejection and cleanup guards against VM/rule disruption. Live compound provisioning and forwarding remain unverified. |
| Explicit private-only VPC create | Passed after provider change | Live API confirmed `private` in both create and read responses. Terraform no-change plan returned 0. This confirms the deployed portal contract supports private even though the reviewed older OpenAPI enum omitted it. |
| Private VPC import with connectivity omitted | Passed | Imported private VPC with omitted connectivity retained private mode and produced no changes. Explicit `public` produced a replacement plan (exit 2), which was deliberately not applied. New resources preserve the legacy public default only at creation. |
| Private VPC name update | Passed | In-place rename succeeded with connectivity omitted, computed NAT output resolved to null, and subsequent no-change plan returned 0. |
| Private VPC portal comparison | Passed | Parent agent saw the updated disposable VPC active, Private only, in Amaravati with zero nodes. |
| Final resource cleanup | Passed | Terraform destroyed the original subnet/VPC and the later private VPC. Final GETs for VPCs, reserved IPs and load balancers each returned HTTP 200 and zero entries. Parent agent refreshed the portal and confirmed “No VPC networks found.” No test-owned networking resource remains. |

Private-VPC cleanup initially hit one empty-body HTTP 403 on its refresh. No delete was issued on that failed plan. The same authorized client later returned 200; the reviewed Terraform destroy plan and apply then succeeded. No authentication/user-agent change or access-control bypass was used.

No successful paid networking allocation has occurred in this pass. This is not a billing-settlement audit. Local fixture coverage for the blocked operations is documented in `NETWORK_FEATURE_MATRIX.md`; it does not substitute for live provisioning or packet tests.
