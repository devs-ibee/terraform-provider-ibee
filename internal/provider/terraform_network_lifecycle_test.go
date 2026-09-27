package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// This exercises real Terraform planning and framework value conversion against
// loopback fixtures only. No credentials or infrastructure are used.
func TestTerraformLifecycleNetworking(t *testing.T) {
	if os.Getenv("IBEE_TF_TEST") != "1" {
		t.Skip("set IBEE_TF_TEST=1 for local Terraform networking lifecycle fixtures")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err = os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-buildvcs=false", "-o", filepath.Join(bin, "terraform-provider-ibee"), ".")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cli := filepath.Join(dir, "dev.tfrc")
	writeTestFile(t, cli, fmt.Sprintf("provider_installation {\n dev_overrides { \"devs-ibee/ibee\" = %q }\n direct {}\n}\n", bin))
	fixture := &networkTerraformFixture{objects: map[string]map[string]any{}, allowed: true}
	server := httptest.NewServer(fixture)
	defer server.Close()
	env := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "IBEE_") || strings.HasPrefix(key, "TF_") {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "TF_CLI_CONFIG_FILE="+cli, "TF_IN_AUTOMATION=1", "CHECKPOINT_DISABLE=1")
	run := func(want int, args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "terraform", args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		code := 0
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		if code != want {
			t.Fatalf("terraform %v exited %d want %d\n%s", args, code, want, out)
		}
		return string(out)
	}
	config := fmt.Sprintf(`terraform {
 required_version = ">= 1.11"
 required_providers { ibee = { source = "devs-ibee/ibee" } }
}
provider "ibee" {
 endpoint = %q
 token = "fixture-only"
 workspace_id = "workspace"
 operation_timeout = "3s"
}
resource "ibee_vpc" "test" {
 name = "fixture"
 site_id = "site"
 cidr = "10.144.0.0/22"
 create_default_subnet = false
}
resource "ibee_vpc_subnet" "test" {
 vpc_id = ibee_vpc.test.id
 name = "app"
 prefix_length = 24
 dns = ["1.1.1.1", "8.8.8.8"]
}
resource "ibee_nat_gateway" "test" {
 vpc_id = ibee_vpc.test.id
 subnet_id = ibee_vpc_subnet.test.id
}
resource "ibee_vpc_node_attachment" "test" {
 vpc_id = ibee_vpc.test.id
 subnet_id = ibee_vpc_subnet.test.id
 vm_id = "vm-private"
 connectivity = "nat"
 depends_on = [ibee_nat_gateway.test]
}
resource "ibee_nat_port_forwarding_rule" "test" {
 vpc_id = ibee_vpc.test.id
 nat_gateway_id = ibee_nat_gateway.test.id
 name = "https"
 external_port = 443
 internal_ip = ibee_vpc_node_attachment.test.private_ip
 internal_port = 443
}
resource "ibee_firewall_group" "test" { name = "app" }
resource "ibee_firewall_rule" "test" {
 firewall_group_id = ibee_firewall_group.test.id
 port_start = 443
 enabled = false
}
resource "ibee_firewall_attachment" "test" {
 firewall_group_id = ibee_firewall_group.test.id
 vm_id = "vm-private"
 depends_on = [ibee_firewall_rule.test]
}
resource "ibee_reserved_ip" "test" {
 site_id = "site"
 label = "public"
 reverse_dns = "app.example.com"
}
resource "ibee_reserved_ip_attachment" "test" {
 reserved_ip_id = ibee_reserved_ip.test.id
 vm_id = "vm-public"
}
resource "ibee_load_balancer_l4" "test" {
 name = "tcp"
 protocol = "tcp"
 backends = [{ type = "ip", target = "192.0.2.1", port = 443 }]
}
resource "ibee_load_balancer_l7" "test" {
 name = "http"
 protocol = "http"
 backends = [{ type = "ip", target = "192.0.2.1", port = 8080 }]
 rules = [{ path_prefix = "/api", backends = [{ type = "ip", target = "192.0.2.1", port = 8081 }] }]
}
`, server.URL)
	writeTestFile(t, filepath.Join(dir, "main.tf"), config)
	run(0, "validate", "-no-color")
	run(0, "apply", "-auto-approve", "-input=false", "-no-color")
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	for _, item := range []struct{ name, id string }{{"vpc", "vpc-1"}, {"vpc_subnet", "vpc-1/subnet-1"}, {"nat_gateway", "vpc-1/nat-1"}, {"vpc_node_attachment", "vpc-1/vm-private"}, {"nat_port_forwarding_rule", "vpc-1/nat-1/pf-1"}, {"firewall_group", "group-1"}, {"firewall_rule", "group-1/rule-1"}, {"firewall_attachment", "group-1/vm-private"}, {"reserved_ip", "ip-1"}, {"reserved_ip_attachment", "ip-1/vm-public"}, {"load_balancer_l4", "lb-l4"}, {"load_balancer_l7", "lb-l7"}} {
		address := "ibee_" + item.name + ".test"
		run(0, "state", "rm", address)
		run(0, "import", "-input=false", "-no-color", address, item.id)
	}
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	// Exercise mutable fields and nullable optional L7 rules removal.
	config = strings.Replace(config, "port = 8080", "port = 8082", 1)
	config = strings.Replace(config, " rules = [{ path_prefix = \"/api\", backends = [{ type = \"ip\", target = \"192.0.2.1\", port = 8081 }] }]", " rules = []", 1)
	config = strings.Replace(config, "internal_port = 443", "internal_port = 444", 1)
	config = strings.Replace(config, `name = "fixture"`, `name = "renamed-vpc"`, 1)
	config = strings.Replace(config, `dns = ["1.1.1.1", "8.8.8.8"]`, `dns = ["9.9.9.9"]`, 1)
	config = strings.Replace(config, `vm_id = "vm-public"`, `vm_id = "vm-public-2"`, 1)
	config = strings.Replace(config, `protocol = "tcp"`, `protocol = "tls_passthrough"`, 1)
	config = strings.Replace(config, `protocol = "http"`, `protocol = "https"`, 1)
	writeTestFile(t, filepath.Join(dir, "main.tf"), config)
	run(0, "apply", "-auto-approve", "-input=false", "-no-color")
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	// Changing automatic prefix must allocate a fresh CIDR, not reuse the prior
	// computed CIDR as though it were explicitly configured.
	config = strings.Replace(config, "prefix_length = 24", "prefix_length = 25", 1)
	writeTestFile(t, filepath.Join(dir, "main.tf"), config)
	run(0, "apply", "-auto-approve", "-input=false", "-no-color")
	run(0, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	fixture.mu.Lock()
	fixture.objects["/networking/load-balancers/lb-l4"]["name"] = "portal-drift"
	fixture.mu.Unlock()
	run(2, "plan", "-detailed-exitcode", "-input=false", "-no-color")
	run(0, "apply", "-auto-approve", "-input=false", "-no-color")
	fixture.mu.Lock()
	fixture.allowed = false
	fixture.mu.Unlock()
	run(0, "destroy", "-auto-approve", "-input=false", "-no-color")
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.objects) != 0 {
		t.Fatalf("resources remain after destroy: %v", fixture.objects)
	}
}

type networkTerraformFixture struct {
	mu      sync.Mutex
	objects map[string]map[string]any
	allowed bool
}

func (f *networkTerraformFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	send := func(v any) { json.NewEncoder(w).Encode(v) }
	p := r.URL.Path
	if p == "/billing/resource-eligibility" {
		networkTestEligibility(w, f.allowed)
		return
	}
	body := map[string]any{}
	if r.Body != nil && (r.Method == "POST" || r.Method == "PATCH") {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	groupPath := "/networking/firewall-groups/group-1"
	if p == groupPath+"/rules" && r.Method == "POST" {
		body["rule_id"] = "rule-1"
		body["enabled"] = true
		body["system_managed"] = false
		if body["port_end"] == nil {
			body["port_end"] = body["port_start"]
		}
		f.objects[groupPath]["rules"] = []any{body}
		send(f.objects[groupPath])
		return
	}
	if p == groupPath+"/rules/rule-1" {
		rules := f.objects[groupPath]["rules"].([]any)
		if r.Method == "DELETE" {
			f.objects[groupPath]["rules"] = []any{}
			w.WriteHeader(204)
			return
		}
		for k, v := range body {
			rules[0].(map[string]any)[k] = v
		}
		send(f.objects[groupPath])
		return
	}
	if (p == "/networking/reserved-ips/ip-1/attach" || p == "/networking/reserved-ips/ip-1/detach" || p == "/networking/reserved-ips/ip-1/move") && r.Method == "POST" {
		ip := f.objects["/networking/reserved-ips/ip-1"]
		ip["attached_resource_id"] = body["vm_id"]
		ip["attached_vpc_id"] = body["vpc_id"]
		ip["attached_subnet_id"] = body["subnet_id"]
		send(ip)
		return
	}
	if r.Method == "POST" {
		var id, idField, collection string
		switch p {
		case "/networking/vpcs":
			id, idField, collection = "vpc-1", "vpc_id", p
			body["status"] = "available"
			body["subnets"] = []any{}
			if body["description"] == nil {
				body["description"] = ""
			}
		case "/networking/vpcs/vpc-1/subnets":
			id, idField, collection = "subnet-1", "subnet_id", p
			if body["cidr"] == nil {
				prefix := 24
				if n, ok := body["prefix_length"].(float64); ok {
					prefix = int(n)
				}
				body["cidr"] = fmt.Sprintf("10.144.0.0/%d", prefix)
			}
			body["gateway"] = "10.144.0.1"
			body["status"] = "available"
		case "/networking/vpcs/vpc-1/nat-gateways":
			if body["reserved_public_ip_id"] != nil {
				http.Error(w, "computed platform IP must not be reused as a reservation", 400)
				return
			}
			id, idField, collection = "nat-1", "nat_gateway_id", p
			body["public_ip_id"] = "nat-ip"
			body["public_ip"] = "203.0.113.2"
			body["site_id"] = "site"
			body["status"] = "available"
		case "/networking/vpcs/vpc-1/nodes":
			id, idField, collection = "vm-private", "vm_id", p
			body["allocation_id"] = "allocation-1"
			body["private_ip"] = "10.144.0.10"
			body["gateway"] = "10.144.0.1"
		case "/networking/vpcs/vpc-1/nat-gateways/nat-1/port-forwarding-rules":
			id, idField, collection = "pf-1", "port_forward_rule_id", p
			body["status"] = "available"
		case "/networking/firewall-groups":
			id, idField, collection = "group-1", "firewall_group_id", p
			body["status"] = "active"
			body["rules"] = []any{}
		case groupPath + "/attachments":
			id, idField, collection = "vm-private", "vm_id", p
			body["network_id"] = "network-1"
		case "/networking/reserved-ips":
			id, idField, collection = "ip-1", "public_ip_id", p
			body["address"] = "203.0.113.1"
			body["status"] = "reserved"
			body["reverse_dns"] = ""
		case "/networking/load-balancers/l4", "/networking/load-balancers/l7":
			if protocol := body["protocol"]; protocol == "https" || protocol == "tls_passthrough" {
				tls, ok := body["tls"].(map[string]any)
				mode := "terminate"
				if protocol == "tls_passthrough" {
					mode = "passthrough"
				}
				if !ok || tls["mode"] != mode || tls["certificate_source"] != "managed" {
					http.Error(w, "TLS request missing required managed mode", 400)
					return
				}
			}
			layer := strings.TrimPrefix(p, "/networking/load-balancers/")
			id, idField, collection = "lb-"+layer, "lb_id", "/networking/load-balancers"
			body["layer"] = layer
			body["status"] = "active"
			body["endpoint"] = map[string]any{"host": id + ".test", "port": 80}
			body["url"] = "http://" + id + ".test"
			body["endpoint_url"] = body["url"]
		default:
			http.Error(w, "unexpected POST "+p, 400)
			return
		}
		body[idField] = id
		f.objects[collection+"/"+id] = body
		send(body)
		return
	}
	if r.Method == "PATCH" && (strings.HasPrefix(p, "/networking/load-balancers/l4/") || strings.HasPrefix(p, "/networking/load-balancers/l7/")) {
		parts := strings.Split(p, "/")
		p = "/networking/load-balancers/" + parts[len(parts)-1]
	}
	if obj, ok := f.objects[p]; ok {
		switch r.Method {
		case "PATCH":
			for k, v := range body {
				obj[k] = v
			}
		case "DELETE":
			delete(f.objects, p)
			w.WriteHeader(204)
			return
		}
		send(obj)
		return
	}
	if r.Method == "GET" && (strings.HasSuffix(p, "/nat-gateways") || strings.HasSuffix(p, "/nodes") || strings.HasSuffix(p, "/port-forwarding-rules") || strings.HasSuffix(p, "/attachments") || strings.HasSuffix(p, "/subnets")) {
		items := []any{}
		for key, obj := range f.objects {
			if strings.HasPrefix(key, p+"/") && !strings.Contains(strings.TrimPrefix(key, p+"/"), "/") {
				items = append(items, obj)
			}
		}
		send(items)
		return
	}
	http.Error(w, "missing fixture object "+p, 404)
}
