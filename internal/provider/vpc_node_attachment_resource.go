package provider

import (
	"fmt"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
)

func NewVpcNodeAttachmentResource() resource.Resource {
	base := func(v networkValues) string { return networkVpcPath(v) + "/nodes" }
	return &networkResource{name: "vpc_node_attachment", description: "Attaches a VM to a VPC subnet and refreshes its assigned address. Import as vpc_id/vm_id. Connectivity changes require replacement. Public or NAT connectivity may incur charges; the server enforces admission.",
		idField: "vm_id", listIdentity: "vm_id", list: true, importFields: []string{"vpc_id", "vm_id"},
		attributes:    map[string]schema.Attribute{"id": networkIDAttribute(), "vpc_id": networkRequired(true), "subnet_id": networkRequired(true), "vm_id": networkRequired(true), "connectivity": networkOptionalString("private", true), "private_ip": schema.StringAttribute{Computed: true}, "gateway": schema.StringAttribute{Computed: true}, "reserved_public_ip_id": networkOptionalReference()},
		requestFields: networkIdentityFields("vm_id", "subnet_id", "connectivity", "reserved_public_ip_id"), responseFields: networkIdentityFields("vm_id", "subnet_id", "connectivity", "private_ip", "gateway"),
		createPath: base, readPath: base, deletePath: func(v networkValues) string { return base(v) + "/" + v.segment("vm_id") },
		createResultID: func(v networkValues, out map[string]any) (string, error) {
			if id, ok := out["allocation_id"].(string); !ok || id == "" {
				return "", fmt.Errorf("missing allocation_id")
			}
			return v.str("vm_id"), nil
		},
		validate: func(v networkValues) error {
			c := v.str("connectivity")
			if c != "private" && c != "public_ip" && c != "nat" {
				return fmt.Errorf("connectivity must be private, public_ip or nat")
			}
			return nil
		},
	}
}
