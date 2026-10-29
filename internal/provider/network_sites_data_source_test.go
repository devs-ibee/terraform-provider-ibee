package provider

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
)

func TestNetworkSitesDiscoveryUsesNetworkingContract(t *testing.T) {
	d := NewNetworkSitesDataSource().(*networkSitesDataSource)
	d.client = networkTestClient(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/networking/sites" || r.URL.Query().Get("workspace_id") != "workspace" {
			t.Errorf("wrong networking discovery request %s %s", r.Method, r.URL)
		}
		fmt.Fprint(w, `[{"site_id":"network-site","site_name":"Available site","available":true,"message":null},{"site_id":"compute-only-site","site_name":"Compute-only site","available":false,"message":"Networking unavailable"}]`)
	})
	var sch datasource.SchemaResponse
	d.Schema(context.Background(), datasource.SchemaRequest{}, &sch)
	resp := datasource.ReadResponse{State: tfsdk.State{Schema: sch.Schema}}
	d.Read(context.Background(), datasource.ReadRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatal(resp.Diagnostics)
	}
	var actual networkSitesModel
	if diagnostics := resp.State.Get(context.Background(), &actual); diagnostics.HasError() {
		t.Fatal(diagnostics)
	}
	if len(actual.Sites) != 2 || actual.Sites[0].Name.ValueString() != "Available site" || !actual.Sites[0].Available.ValueBool() || !actual.Sites[0].Message.IsNull() || actual.Sites[1].Available.ValueBool() || actual.Sites[1].Message.ValueString() != "Networking unavailable" {
		t.Fatalf("incorrect networking catalog mapping: %+v", actual)
	}
}
