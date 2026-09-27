package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestVpcCascadeGuardPreservesUnmanagedSubnets(t *testing.T) {
	for _, body := range []string{`[{"subnet_id":"owned"},{"subnet_id":"someone-elses-subnet"}]`, `null`, `[{}]`} {
		t.Run(body, func(t *testing.T) {
			mutations := 0
			r := NewVpcResource().(*vpcResource)
			r.client = networkTestClient(func(w http.ResponseWriter, req *http.Request) {
				if req.Method != "GET" {
					mutations++
				}
				fmt.Fprint(w, body)
			})
			model := vpcResourceModel{ID: types.StringValue("vpc"), OwnedDefaultSubnetID: types.StringValue("owned")}
			state := networkTestState(t, networkTestSchema(r), &model)
			resp := resource.DeleteResponse{}
			r.Delete(context.Background(), resource.DeleteRequest{State: state}, &resp)
			if !resp.Diagnostics.HasError() || mutations != 0 {
				t.Fatalf("unsafe parent cascade allowed: mutations=%d diagnostics=%v", mutations, resp.Diagnostics)
			}
		})
	}
}
func TestLoadBalancerTLSModesUseManagedPublicContract(t *testing.T) {
	for _, tc := range []struct{ layer, protocol, mode string }{{"l4", "tcp", ""}, {"l4", "tls_passthrough", "passthrough"}, {"l7", "http", ""}, {"l7", "https", "terminate"}} {
		t.Run(tc.protocol, func(t *testing.T) {
			r := newNetworkLB(tc.layer).(*networkResource)
			v := networkTestValues(t, r, map[string]any{"name": "app", "protocol": tc.protocol, "backends": []any{map[string]any{"type": "ip", "target": "192.0.2.1", "port": float64(443), "weight": float64(100), "tls": false}}})
			body, err := r.body(v, false)
			if err != nil {
				t.Fatal(err)
			}
			if tc.mode == "" {
				if _, ok := body["tls"]; ok {
					t.Fatal("unencrypted protocol sent TLS")
				}
			} else if !reflect.DeepEqual(body["tls"], map[string]any{"mode": tc.mode, "certificate_source": "managed"}) {
				t.Fatalf("wrong TLS body: %v", body["tls"])
			}
			updated, err := r.body(v, true)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := updated["tls"]; ok {
				t.Fatal("ordinary backend update unexpectedly changed certificate policy")
			}
		})
	}
}
func TestSubnetDNSAndPrefixRequestValidation(t *testing.T) {
	valid := vpcSubnetModel{Name: types.StringValue("private"), AutoCidr: types.BoolValue(true), PrefixLength: types.Int64Value(25), DNS: types.ListValueMust(types.StringType, []attr.Value{types.StringValue("9.9.9.9")})}
	body, err := subnetRequestBody(context.Background(), valid, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if body["prefix_length"] != int64(25) || body["cidr"] != nil {
		t.Fatalf("auto allocation body=%v", body)
	}
	for _, dns := range [][]attr.Value{nil, {types.StringValue("2001:db8::1")}, {types.StringValue("resolver.example.com")}} {
		bad := valid
		bad.DNS = types.ListValueMust(types.StringType, dns)
		if _, err := subnetRequestBody(context.Background(), bad, false, false); err == nil {
			t.Fatalf("invalid DNS accepted: %v", dns)
		}
	}
	bad := valid
	bad.PrefixLength = types.Int64Value(30)
	if _, err := subnetRequestBody(context.Background(), bad, false, false); err == nil {
		t.Fatal("prefix /30 accepted")
	}
	updated, err := subnetRequestBody(context.Background(), valid, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) != 2 || updated["prefix_length"] != nil || updated["cidr"] != nil {
		t.Fatalf("update tried to mutate allocation: %v", updated)
	}
}
func TestVpcCustomDefaultSubnetCIDR(t *testing.T) {
	ctx := context.Background()
	r := NewVpcResource().(*vpcResource)
	created := map[string]any{}
	r.client = networkTestClient(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == "POST" {
			json.NewDecoder(req.Body).Decode(&created)
		}
		fmt.Fprint(w, `{"vpc_id":"vpc","name":"app","site_id":"site","cidr":"10.144.0.0/22","description":"","status":"available","subnets":[{"subnet_id":"default","name":"default","cidr":"10.144.1.0/24"}]}`)
	})
	m := vpcResourceModel{Name: types.StringValue("app"), SiteID: types.StringValue("site"), Cidr: types.StringValue("10.144.0.0/22"), Description: types.StringValue(""), AutoCidr: types.BoolValue(false), CreateDefaultSubnet: types.BoolValue(true), DefaultSubnetCidr: types.StringValue("10.144.1.0/24")}
	sch := networkTestSchema(r)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: sch}}
	r.Create(ctx, resource.CreateRequest{Plan: networkTestPlan(t, sch, &m)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if created["default_subnet_cidr"] != "10.144.1.0/24" {
		t.Fatalf("default subnet not requested: %v", created)
	}
	var got vpcResourceModel
	if d := resp.State.Get(ctx, &got); d.HasError() {
		t.Fatal(d)
	}
	if got.OwnedDefaultSubnetID.ValueString() != "default" {
		t.Fatalf("default subnet ownership lost: %+v", got)
	}
}

func TestReservedIPAtomicMoveRetainsAddressOwnership(t *testing.T) {
	ctx := context.Background()
	vm := "old-vm"
	calls := []string{}
	r := NewReservedIPAttachmentResource().(*reservedIPAttachmentResource)
	r.client = networkTestClient(func(w http.ResponseWriter, req *http.Request) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		if req.Method == "POST" {
			if req.URL.Path != "/networking/reserved-ips/ip/move" {
				t.Fatalf("move used non-atomic mutation %s", req.URL.Path)
			}
			var body map[string]any
			json.NewDecoder(req.Body).Decode(&body)
			vm = body["vm_id"].(string)
		}
		json.NewEncoder(w).Encode(map[string]any{"public_ip_id": "ip", "attached_resource_id": vm, "attached_vpc_id": nil, "attached_subnet_id": nil})
	})
	sch := networkTestSchema(r)
	before := networkTestValues(t, r.networkResource, map[string]any{"id": "old-vm", "reserved_ip_id": "ip", "vm_id": "old-vm"})
	after := networkTestValues(t, r.networkResource, map[string]any{"id": "old-vm", "reserved_ip_id": "ip", "vm_id": "new-vm"})
	state := networkTestState(t, sch, types.ObjectValueMust(r.attributeTypes(), before))
	resp := resource.UpdateResponse{State: state}
	r.Update(ctx, resource.UpdateRequest{State: state, Plan: networkTestPlan(t, sch, types.ObjectValueMust(r.attributeTypes(), after))}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	want := []string{"GET /networking/reserved-ips/ip", "POST /networking/reserved-ips/ip/move", "GET /networking/reserved-ips/ip"}
	if !reflect.DeepEqual(calls, want) || vm != "new-vm" {
		t.Fatalf("calls=%v vm=%s", calls, vm)
	}
}
