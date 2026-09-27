package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func connectivityVpcFixture(mode string) map[string]any {
	gateways := []any{}
	if mode == "nat_gateway" {
		gateways = append(gateways, map[string]any{"nat_gateway_id": "owned-nat", "status": "available"})
	}
	return map[string]any{"vpc_id": "vpc", "name": "test", "site_id": "site", "description": "unique-test-description", "cidr": "10.144.0.0/22", "status": "available", "connectivity_type": mode, "subnets": []any{}, "nat_gateways": gateways}
}
func connectivityVpcPlan(mode string) vpcResourceModel {
	return vpcResourceModel{Name: types.StringValue("test"), SiteID: types.StringValue("site"), Description: types.StringValue("unique-test-description"), Cidr: types.StringValue("10.144.0.0/22"), AutoCidr: types.BoolValue(false), CreateDefaultSubnet: types.BoolValue(false), ConnectivityType: types.StringValue(mode)}
}

func TestVpcConnectivityCanonicalCreateAndUpdate(t *testing.T) {
	for _, mode := range []string{"public", "private", "nat_gateway"} {
		t.Run(mode, func(t *testing.T) {
			r := NewVpcResource().(*vpcResource)
			out := connectivityVpcFixture(mode)
			r.client = networkTestClient(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == "/billing/resource-eligibility" {
					networkTestEligibility(w, true)
					return
				}
				if req.Method == "GET" && req.URL.Path == "/networking/vpcs" {
					fmt.Fprint(w, `[]`)
					return
				}
				if req.Method == "POST" || req.Method == "PATCH" {
					var body map[string]any
					json.NewDecoder(req.Body).Decode(&body)
					if req.Method == "POST" && body["connectivity_type"] != mode {
						t.Error("requested connectivity omitted")
					}
					if req.Method == "PATCH" {
						out["name"] = body["name"]
					}
				}
				json.NewEncoder(w).Encode(out)
			})
			s := networkTestSchema(r)
			m := connectivityVpcPlan(mode)
			created := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(context.Background(), resource.CreateRequest{Plan: networkTestPlan(t, s, &m)}, &created)
			if created.Diagnostics.HasError() {
				t.Fatal(created.Diagnostics)
			}
			var state vpcResourceModel
			created.State.Get(context.Background(), &state)
			if state.ConnectivityType.ValueString() != mode {
				t.Fatal("connectivity lost")
			}
			if mode == "nat_gateway" && state.OwnedDefaultNATID.ValueString() != "owned-nat" {
				t.Fatal("create did not record returned NAT ownership")
			}
			plan := state
			plan.Name = types.StringValue("updated")
			plan.DefaultNATGatewayID = types.StringUnknown()
			updated := resource.UpdateResponse{State: created.State}
			r.Update(context.Background(), resource.UpdateRequest{Plan: networkTestPlan(t, s, &plan), State: created.State}, &updated)
			if updated.Diagnostics.HasError() {
				t.Fatal(updated.Diagnostics)
			}
			updated.State.Get(context.Background(), &state)
			if state.DefaultNATGatewayID.IsUnknown() || state.Name.ValueString() != "updated" {
				t.Fatal("Update wrote unknown computed state")
			}
		})
	}
	for _, returned := range []any{nil, "public"} {
		r := NewVpcResource().(*vpcResource)
		out := connectivityVpcFixture("private")
		out["connectivity_type"] = returned
		r.client = networkTestClient(func(w http.ResponseWriter, req *http.Request) { json.NewEncoder(w).Encode(out) })
		s := networkTestSchema(r)
		m := connectivityVpcPlan("private")
		resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
		r.Create(context.Background(), resource.CreateRequest{Plan: networkTestPlan(t, s, &m)}, &resp)
		if !resp.Diagnostics.HasError() || resp.State.Raw.IsNull() {
			t.Fatal("unsupported private intent was accepted or cleanup identity lost")
		}
	}
}

func TestVpcFailedNATCreateNeverAdoptsObservedCandidate(t *testing.T) {
	for _, status := range []int{409, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r := NewVpcResource().(*vpcResource)
			posted := false
			mutations := 0
			out := connectivityVpcFixture("nat_gateway")
			r.client = networkTestClient(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == "/billing/resource-eligibility" {
					networkTestEligibility(w, true)
					return
				}
				if req.Method == "POST" {
					posted = true
					mutations++
					http.Error(w, "create rejected or failed", status)
					return
				}
				if req.Method != "GET" {
					mutations++
					t.Error("reconciliation mutated a resource")
				}
				if req.URL.Path == "/networking/vpcs" {
					if posted {
						json.NewEncoder(w).Encode([]any{out})
					} else {
						fmt.Fprint(w, `[]`)
					}
					return
				}
				json.NewEncoder(w).Encode(out)
			})
			s := networkTestSchema(r)
			m := connectivityVpcPlan("nat_gateway")
			resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
			r.Create(context.Background(), resource.CreateRequest{Plan: networkTestPlan(t, s, &m)}, &resp)
			if !resp.Diagnostics.HasError() || !resp.State.Raw.IsNull() || mutations != 1 {
				t.Fatalf("failed request adopted possible concurrent resources: %v mutations=%d", resp.Diagnostics, mutations)
			}
			if !strings.Contains(fmt.Sprint(resp.Diagnostics), "requires reconciliation") {
				t.Fatal("possible partial creation not reported")
			}
		})
	}
}

func TestVpcNATDeletionPreservesUnmanagedDependencies(t *testing.T) {
	for _, blocked := range []string{"", "unowned", "nodes", "rules", "missing-nodes"} {
		t.Run(blocked, func(t *testing.T) {
			r := NewVpcResource().(*vpcResource)
			deleted := []string{}
			natExists := true
			r.client = networkTestClient(func(w http.ResponseWriter, req *http.Request) {
				if req.Method == "DELETE" {
					deleted = append(deleted, req.URL.Path)
					if strings.HasSuffix(req.URL.Path, "/owned-nat") {
						natExists = false
					}
					w.WriteHeader(204)
					return
				}
				switch {
				case strings.HasSuffix(req.URL.Path, "/subnets"):
					fmt.Fprint(w, `[]`)
				case strings.HasSuffix(req.URL.Path, "/nat-gateways"):
					if natExists {
						fmt.Fprint(w, `[{"nat_gateway_id":"owned-nat"}]`)
					} else {
						fmt.Fprint(w, `[]`)
					}
				case strings.HasSuffix(req.URL.Path, "/nodes"):
					if blocked == "nodes" {
						fmt.Fprint(w, `[{"vm_id":"another-vm"}]`)
					} else if blocked == "missing-nodes" {
						fmt.Fprint(w, `null`)
					} else {
						fmt.Fprint(w, `[]`)
					}
				case strings.HasSuffix(req.URL.Path, "/port-forwarding-rules"):
					if blocked == "rules" {
						fmt.Fprint(w, `[{"port_forward_rule_id":"someone-elses-rule"}]`)
					} else {
						fmt.Fprint(w, `[]`)
					}
				default:
					t.Fatal("unexpected route", req.URL.Path)
				}
			})
			m := connectivityVpcPlan("nat_gateway")
			m.ID = types.StringValue("vpc")
			m.OwnedDefaultNATID = types.StringValue("owned-nat")
			if blocked == "unowned" {
				m.OwnedDefaultNATID = types.StringNull()
			}
			s := networkTestState(t, networkTestSchema(r), &m)
			resp := resource.DeleteResponse{State: s}
			r.Delete(context.Background(), resource.DeleteRequest{State: s}, &resp)
			if blocked != "" {
				if !resp.Diagnostics.HasError() || len(deleted) != 0 {
					t.Fatalf("unmanaged child affected: %v %v", deleted, resp.Diagnostics)
				}
			} else if resp.Diagnostics.HasError() || !reflect.DeepEqual(deleted, []string{"/networking/vpcs/vpc/nat-gateways/owned-nat", "/networking/vpcs/vpc"}) {
				t.Fatalf("owned cleanup=%v diagnostics=%v", deleted, resp.Diagnostics)
			}
		})
	}
	// The standalone resource must not adopt a default gateway through a POST
	// whose service implementation simply returns the already-existing gateway.
	r := NewNATGatewayResource().(*networkResource)
	posts := 0
	r.client = networkTestClient(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/billing/resource-eligibility" {
			networkTestEligibility(w, true)
			return
		}
		if req.Method != "GET" {
			posts++
		}
		fmt.Fprint(w, `[{"nat_gateway_id":"existing"}]`)
	})
	v := networkTestValues(t, r, map[string]any{"vpc_id": "vpc", "name": "NAT Gateway"})
	s := networkTestSchema(r)
	resp := resource.CreateResponse{State: tfsdk.State{Schema: s}}
	r.Create(context.Background(), resource.CreateRequest{Plan: networkTestPlan(t, s, types.ObjectValueMust(r.attributeTypes(), v))}, &resp)
	if !resp.Diagnostics.HasError() || posts != 0 || !resp.State.Raw.IsNull() {
		t.Fatal("standalone NAT create adopted existing gateway")
	}
}

func TestVpcImportDoesNotAdoptNATOwnership(t *testing.T) {
	r := NewVpcResource().(*vpcResource)
	out := connectivityVpcFixture("nat_gateway")
	r.client = networkTestClient(func(w http.ResponseWriter, req *http.Request) { json.NewEncoder(w).Encode(out) })
	empty := networkTestState(t, networkTestSchema(r), &vpcResourceModel{})
	imported := resource.ImportStateResponse{State: empty}
	r.ImportState(context.Background(), resource.ImportStateRequest{ID: "vpc"}, &imported)
	if imported.Diagnostics.HasError() {
		t.Fatal(imported.Diagnostics)
	}
	read := resource.ReadResponse{State: imported.State}
	r.Read(context.Background(), resource.ReadRequest{State: imported.State}, &read)
	if read.Diagnostics.HasError() {
		t.Fatal(read.Diagnostics)
	}
	var got vpcResourceModel
	read.State.Get(context.Background(), &got)
	if got.DefaultNATGatewayID.ValueString() != "owned-nat" || !got.OwnedDefaultNATID.IsNull() || !got.OwnedDefaultSubnetID.IsNull() {
		t.Fatal("import adopted destructive ownership")
	}
}
