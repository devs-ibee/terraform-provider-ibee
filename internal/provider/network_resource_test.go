package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type networkTestTransport struct{ handler http.Handler }

func (t networkTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	t.handler.ServeHTTP(w, req)
	return w.Result(), nil
}
func networkTestClient(handler http.HandlerFunc) *Client {
	c := NewClient("https://network-fixture.test", "fixture", "workspace")
	c.http = &http.Client{Transport: networkTestTransport{handler: handler}}
	c.operationTimeout = time.Second
	c.pollInterval = time.Millisecond
	return c
}
func networkTestSchema(r resource.Resource) schema.Schema {
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	return resp.Schema
}
func networkTestState(t *testing.T, s schema.Schema, value any) tfsdk.State {
	t.Helper()
	state := tfsdk.State{Schema: s}
	if d := state.Set(context.Background(), value); d.HasError() {
		t.Fatal(d)
	}
	return state
}
func networkTestPlan(t *testing.T, s schema.Schema, value any) tfsdk.Plan {
	t.Helper()
	state := networkTestState(t, s, value)
	return tfsdk.Plan{Schema: s, Raw: state.Raw}
}
func networkTestValues(t *testing.T, r *networkResource, raw map[string]any) networkValues {
	t.Helper()
	v := networkValues{}
	for k, a := range r.attributes {
		value, err := networkValue(a.GetType(), raw[k])
		if err != nil {
			t.Fatal(err)
		}
		v[k] = value
	}
	return v
}
func networkTestEligibility(w http.ResponseWriter, allowed bool) {
	reason := "eligible"
	if !allowed {
		reason = "initial_topup_required"
	}
	json.NewEncoder(w).Encode(map[string]any{"organization_id": "org", "allowed": allowed, "reason": reason, "billing_mode": "PREPAID", "billing_state": "CURRENT", "currency": "INR", "evaluated_at": "2026-09-27T00:00:00Z"})
}

func TestNetworkMalformedListPreservesState(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `[{"status":"active"}]`} {
		t.Run(body, func(t *testing.T) {
			r := NewNATGatewayResource().(*networkResource)
			r.client = networkTestClient(func(w http.ResponseWriter, req *http.Request) { fmt.Fprint(w, body) })
			v := networkTestValues(t, r, map[string]any{"id": "nat-1", "vpc_id": "vpc-1", "name": "NAT Gateway", "status": "available"})
			s := networkTestState(t, networkTestSchema(r), types.ObjectValueMust(r.attributeTypes(), v))
			resp := resource.ReadResponse{State: s}
			r.Read(context.Background(), resource.ReadRequest{State: s}, &resp)
			if !resp.Diagnostics.HasError() {
				t.Fatal("malformed response accepted")
			}
			if resp.State.Raw.IsNull() {
				t.Fatal("malformed response removed state")
			}
		})
	}
}
func TestNetworkAttachmentPagination(t *testing.T) {
	pages := 0
	r := NewFirewallAttachmentResource().(*networkResource)
	r.client = networkTestClient(func(w http.ResponseWriter, req *http.Request) {
		pages++
		items := []map[string]any{}
		if req.URL.Query().Get("skip") == "0" {
			for i := 0; i < 100; i++ {
				items = append(items, map[string]any{"vm_id": fmt.Sprintf("other-%d", i), "network_id": "n"})
			}
		} else {
			items = append(items, map[string]any{"vm_id": "target", "network_id": "net-target"})
		}
		json.NewEncoder(w).Encode(items)
	})
	v := networkValues{"id": types.StringValue("target"), "firewall_group_id": types.StringValue("g"), "vm_id": types.StringValue("target")}
	out, err := r.fetch(context.Background(), v)
	if err != nil || out["network_id"] != "net-target" || pages != 2 {
		t.Fatalf("out=%v pages=%d err=%v", out, pages, err)
	}
}
func TestNATBillingDenialPreventsCreate(t *testing.T) {
	creates := 0
	r := NewNATGatewayResource().(*networkResource)
	r.client = networkTestClient(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/billing/resource-eligibility" {
			networkTestEligibility(w, false)
			return
		}
		creates++
		t.Error("billing-denied operation reached product API")
	})
	v := networkTestValues(t, r, map[string]any{"vpc_id": "vpc", "name": "NAT Gateway"})
	s := networkTestSchema(r)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: networkTestPlan(t, s, types.ObjectValueMust(r.attributeTypes(), v))}, &resp)
	if !resp.Diagnostics.HasError() || creates != 0 {
		t.Fatalf("create count=%d diagnostics=%v", creates, resp.Diagnostics)
	}
}
func TestNATFailedReadinessRetainsID(t *testing.T) {
	r := NewNATGatewayResource().(*networkResource)
	r.client = networkTestClient(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/billing/resource-eligibility":
			networkTestEligibility(w, true)
		case req.Method == "POST":
			fmt.Fprint(w, `{"nat_gateway_id":"nat-1"}`)
		default:
			fmt.Fprint(w, `[{"nat_gateway_id":"nat-1","name":"NAT Gateway","status":"error","error_message":"allocation failed","public_ip_id":"ip-1","public_ip":"203.0.113.5","site_id":"site","subnet_id":null}]`)
		}
	})
	v := networkTestValues(t, r, map[string]any{"vpc_id": "vpc", "name": "NAT Gateway"})
	s := networkTestSchema(r)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: networkTestPlan(t, s, types.ObjectValueMust(r.attributeTypes(), v))}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("error readiness accepted")
	}
	var id string
	resp.State.GetAttribute(context.Background(), path.Root("id"), &id)
	if id != "nat-1" {
		t.Fatalf("lost created identity: %q", id)
	}
}
func TestNetworkLBStateConversion(t *testing.T) {
	for _, layer := range []string{"l4", "l7"} {
		t.Run(layer, func(t *testing.T) {
			r := newNetworkLB(layer).(*networkResource)
			protocol := "tcp"
			if layer == "l7" {
				protocol = "http"
			}
			raw := map[string]any{"id": "lb-1", "name": "app", "protocol": protocol, "backends": []any{map[string]any{"target": "192.0.2.1", "port": float64(8080), "type": "ip", "weight": float64(100), "tls": false}}, "rules": nil}
			v := networkTestValues(t, r, raw)
			response := map[string]any{"lb_id": "lb-1", "name": "app", "protocol": protocol, "status": "active", "layer": layer, "backends": raw["backends"], "endpoint": map[string]any{"host": "lb.test", "port": float64(80)}, "url": "http://lb.test", "endpoint_url": "http://lb.test", "rules": nil}
			if err := r.merge(v, response); err != nil {
				t.Fatal(err)
			}
			s := networkTestState(t, networkTestSchema(r), types.ObjectValueMust(r.attributeTypes(), v))
			var output types.Object
			if d := s.Get(context.Background(), &output); d.HasError() {
				t.Fatal(d)
			}
			body, err := r.body(networkObject(output), false)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := body["rules"]; ok {
				t.Fatal("null optional rules sent during create")
			}
			if !reflect.DeepEqual(body["backends"], []any{map[string]any{"target": "192.0.2.1", "port": int64(8080), "type": "ip", "weight": int64(100), "tls": false}}) {
				t.Fatalf("backend conversion: %#v", body["backends"])
			}
		})
	}
}
func TestReservedIPAttachmentDoesNotDetachMovedIP(t *testing.T) {
	mutations := 0
	r := NewReservedIPAttachmentResource().(*networkResource)
	r.client = networkTestClient(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != "GET" {
			mutations++
		}
		fmt.Fprint(w, `{"public_ip_id":"ip","attached_resource_id":"another-vm"}`)
	})
	v := networkTestValues(t, r, map[string]any{"id": "original-vm", "reserved_ip_id": "ip", "vm_id": "original-vm"})
	s := networkTestState(t, networkTestSchema(r), types.ObjectValueMust(r.attributeTypes(), v))
	resp := resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: s}, &resp)
	if !resp.Diagnostics.HasError() || mutations != 0 {
		t.Fatalf("diagnostics=%v mutations=%d", resp.Diagnostics, mutations)
	}
}
func TestVpcDeletionOwnsOnlyRecordedDefaultSubnet(t *testing.T) {
	for _, owned := range []string{"", "owned"} {
		t.Run(owned, func(t *testing.T) {
			paths := []string{}
			r := NewVpcResource().(*vpcResource)
			r.client = networkTestClient(func(w http.ResponseWriter, req *http.Request) {
				paths = append(paths, req.Method+" "+req.URL.Path)
				if strings.Contains(req.URL.Path, "unmanaged") {
					t.Fatal("unmanaged subnet touched")
				}
				w.WriteHeader(204)
			})
			model := vpcResourceModel{ID: types.StringValue("vpc"), Name: types.StringValue("test"), SiteID: types.StringValue("site"), Cidr: types.StringValue("10.0.0.0/24"), AutoCidr: types.BoolValue(true), CreateDefaultSubnet: types.BoolValue(false), DefaultSubnetID: types.StringValue("unmanaged"), OwnedDefaultSubnetID: types.StringNull(), Description: types.StringValue(""), Status: types.StringValue("available")}
			if owned != "" {
				model.OwnedDefaultSubnetID = types.StringValue(owned)
			}
			s := networkTestState(t, networkTestSchema(r), &model)
			resp := resource.DeleteResponse{}
			r.Delete(context.Background(), resource.DeleteRequest{State: s}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatal(resp.Diagnostics)
			}
			want := []string{"DELETE /networking/vpcs/vpc"}
			if owned != "" {
				want = append([]string{"DELETE /networking/vpcs/vpc/subnets/owned"}, want...)
			}
			if !reflect.DeepEqual(paths, want) {
				t.Fatalf("got %v want %v", paths, want)
			}
		})
	}
}
func networkTestFirewallPlan() firewallRuleModel {
	return firewallRuleModel{GroupID: types.StringValue("group"), Direction: types.StringValue("ingress"), Protocol: types.StringValue("tcp"), PortStart: types.Int64Value(443), PortEnd: types.Int64Unknown(), RemoteTargets: types.ListValueMust(types.StringType, []attr.Value{types.StringValue("0.0.0.0/0")}), Action: types.StringValue("allow"), Description: types.StringNull(), Priority: types.Int64Unknown(), Enabled: types.BoolValue(false), ID: types.StringUnknown()}
}
func TestFirewallIdentityUsesCompleteDelta(t *testing.T) {
	m := networkTestFirewallPlan()
	port := int64(443)
	rule := firewallRuleAPI{RuleID: "new", Direction: "ingress", Protocol: "tcp", PortStart: &port, PortEnd: &port, RemoteTargets: []string{"0.0.0.0/0"}, Action: "allow", Enabled: true}
	old := rule
	old.RuleID = "old"
	other := rule
	other.RuleID = "other"
	other.Action = "drop"
	got, err := identifyNewFirewallRule(context.Background(), []firewallRuleAPI{old}, []firewallRuleAPI{other, old, rule}, &m)
	if err != nil || got.RuleID != "new" {
		t.Fatalf("got=%v err=%v", got, err)
	}
	duplicate := rule
	duplicate.RuleID = "duplicate"
	if _, err = identifyNewFirewallRule(context.Background(), nil, []firewallRuleAPI{rule, duplicate}, &m); err == nil {
		t.Fatal("ambiguous creation accepted")
	}
}
func TestFirewallCreateDisabledAndRefreshAllFields(t *testing.T) {
	patch := false
	r := NewFirewallRuleResource().(*firewallRuleResource)
	created := false
	rule := map[string]any{"rule_id": "rule", "direction": "ingress", "protocol": "tcp", "port_start": 443, "port_end": 443, "remote_targets": []string{"0.0.0.0/0"}, "action": "allow", "enabled": true, "description": nil, "priority": nil}
	r.client = networkTestClient(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == "PATCH" {
			var b map[string]any
			json.NewDecoder(req.Body).Decode(&b)
			if b["enabled"] != false {
				t.Error("disabled setting omitted")
			}
			patch = true
			rule["enabled"] = false
		}
		if req.Method == "POST" {
			created = true
		}
		rules := []map[string]any{}
		if created {
			rules = append(rules, rule)
		}
		json.NewEncoder(w).Encode(map[string]any{"rules": rules})
	})
	s := networkTestSchema(r)
	m := networkTestFirewallPlan()
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: networkTestPlan(t, s, &m)}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	if !patch {
		t.Fatal("create ignored enabled=false")
	}
	rule["port_start"], rule["port_end"], rule["action"], rule["description"] = 80, 81, "drop", "portal edit"
	read := resource.ReadResponse{State: resp.State}
	r.Read(context.Background(), resource.ReadRequest{State: resp.State}, &read)
	if read.Diagnostics.HasError() {
		t.Fatal(read.Diagnostics)
	}
	var actual firewallRuleModel
	read.State.Get(context.Background(), &actual)
	if actual.PortStart.ValueInt64() != 80 || actual.PortEnd.ValueInt64() != 81 || actual.Action.ValueString() != "drop" || actual.Description.ValueString() != "portal edit" {
		t.Fatalf("drift not refreshed: %+v", actual)
	}
}

func TestNetworkRejectsWrongResponseIdentity(t *testing.T) {
	cases := []struct {
		name   string
		r      *networkResource
		values map[string]any
		body   string
	}{
		{"load-balancer", NewL4LoadBalancerResource().(*networkResource), map[string]any{"id": "wanted"}, `{"lb_id":"other","layer":"l4"}`},
		{"reserved-ip", NewReservedIPResource().(*reservedIPResource).networkResource, map[string]any{"id": "wanted"}, `{"public_ip_id":"other"}`},
		{"attachment", NewReservedIPAttachmentResource().(*networkResource), map[string]any{"id": "vm", "vm_id": "vm", "reserved_ip_id": "wanted"}, `{"public_ip_id":"other","attached_resource_id":"vm"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.r.client = networkTestClient(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, tc.body) })
			v := networkTestValues(t, tc.r, tc.values)
			if _, err := tc.r.fetch(context.Background(), v); err == nil || IsNotFound(err) {
				t.Fatalf("wrong identity should preserve state with an error: %v", err)
			}
		})
	}
}
func TestReservedIPAttachmentMissingOwnershipPreservesState(t *testing.T) {
	mutations := 0
	r := NewReservedIPAttachmentResource().(*networkResource)
	r.client = networkTestClient(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != "GET" {
			mutations++
		}
		fmt.Fprint(w, `{"public_ip_id":"ip"}`)
	})
	v := networkTestValues(t, r, map[string]any{"id": "vm", "vm_id": "vm", "reserved_ip_id": "ip"})
	s := networkTestState(t, networkTestSchema(r), types.ObjectValueMust(r.attributeTypes(), v))
	read := resource.ReadResponse{State: s}
	r.Read(context.Background(), resource.ReadRequest{State: s}, &read)
	if !read.Diagnostics.HasError() || read.State.Raw.IsNull() {
		t.Fatalf("missing ownership must not remove state: %v", read.Diagnostics)
	}
	del := resource.DeleteResponse{}
	r.Delete(context.Background(), resource.DeleteRequest{State: s}, &del)
	if !del.Diagnostics.HasError() || mutations != 0 {
		t.Fatalf("missing ownership allowed detach: %v", del.Diagnostics)
	}
}
func TestFirewallGroupRejectsWrongIdentity(t *testing.T) {
	r := NewFirewallGroupResource().(*firewallGroupResource)
	r.client = networkTestClient(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"firewall_group_id":"other","name":"other","status":"active"}`)
	})
	m := firewallGroupModel{ID: types.StringValue("wanted"), Name: types.StringValue("wanted"), Description: types.StringNull(), Status: types.StringValue("active")}
	s := networkTestState(t, networkTestSchema(r), &m)
	resp := resource.ReadResponse{State: s}
	r.Read(context.Background(), resource.ReadRequest{State: s}, &resp)
	if !resp.Diagnostics.HasError() || resp.State.Raw.IsNull() {
		t.Fatalf("wrong group identity accepted: %v", resp.Diagnostics)
	}
}
