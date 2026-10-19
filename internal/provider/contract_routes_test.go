package provider

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// The contract route check. Every API call site in the provider must send a
// method and path that is an operation in contract/ibee-cloud.yaml. A call
// site's route is taken from the requests it sends during the test suite;
// when no test exercises it, from its source when the path is built only
// from literals and escaped segments; and for the shared networking
// resource's path functions, from TestNetworkResourceRoutesInContract. A
// full run fails if a call site's route can be found none of these ways.

const contractPath = "../../contract/ibee-cloud.yaml"

// offContract lists routes the provider still calls although the contract has
// no such operation. Each one is deprecated and listed in the CHANGELOG.
var offContract = []string{
	"GET /object-storage/buckets/{bucket_name}/cors",
	"PUT /object-storage/buckets/{bucket_name}/cors",
	"DELETE /object-storage/buckets/{bucket_name}/cors",
	"GET /object-storage/buckets/{bucket_name}/lifecycle",
	"PUT /object-storage/buckets/{bucket_name}/lifecycle",
	"DELETE /object-storage/buckets/{bucket_name}/lifecycle",
	"GET /object-storage/buckets/{bucket_name}/notifications",
	"PUT /object-storage/buckets/{bucket_name}/notifications",
	"DELETE /object-storage/buckets/{bucket_name}/notifications",
	"GET /object-storage/buckets/{bucket_name}/object-lock-configuration",
	"PUT /object-storage/buckets/{bucket_name}/object-lock-configuration",
	"POST /cdn/origins",
	"GET /cdn/origins/{origin_id}",
	"PATCH /cdn/origins/{origin_id}",
	"DELETE /cdn/origins/{origin_id}",
}

type routeCall struct{ method, path string }

var routes = struct {
	sync.Mutex
	calls map[routeCall]string        // request -> first call site
	lines map[string]map[int]struct{} // file -> lines that sent a request
}{calls: map[routeCall]string{}, lines: map[string]map[int]struct{}{}}

// recordRoute keeps the request and the provider line that sent it. Requests a
// test sends through the client directly are not provider routes.
func recordRoute(method, path string) {
	pcs := make([]uintptr, 32)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs)])
	for {
		frame, more := frames.Next()
		if strings.HasSuffix(frame.Function, ".(*Client).do") || strings.HasSuffix(frame.Function, ".(*Client).doH") {
			if !more {
				return
			}
			continue
		}
		if strings.HasSuffix(frame.File, "_test.go") {
			return
		}
		file := filepath.Base(frame.File)
		routes.Lock()
		defer routes.Unlock()
		key := routeCall{method, path}
		if _, seen := routes.calls[key]; !seen {
			routes.calls[key] = fmt.Sprintf("%s:%d", file, frame.Line)
		}
		if routes.lines[file] == nil {
			routes.lines[file] = map[int]struct{}{}
		}
		routes.lines[file][frame.Line] = struct{}{}
		return
	}
}

func TestMain(m *testing.M) {
	requestObserver = recordRoute
	code := m.Run()
	if code == 0 {
		problems, err := contractRouteProblems()
		if err != nil {
			problems = append(problems, err.Error())
		}
		if len(problems) > 0 {
			fmt.Fprintf(os.Stderr, "FAIL: contract route check against %s:\n  %s\n", contractPath, strings.Join(problems, "\n  "))
			code = 1
		}
	}
	os.Exit(code)
}

func contractRouteProblems() ([]string, error) {
	contract, err := contractOperations(contractPath)
	if err != nil {
		return nil, err
	}
	allowed := contractAllows(contract)
	var problems []string
	routes.Lock()
	defer routes.Unlock()
	for call, site := range routes.calls {
		if !allowed(call.method, call.path) {
			problems = append(problems, fmt.Sprintf("%s: %s %s is not an operation in the contract", site, call.method, call.path))
		}
	}
	sites, err := apiCallSites(".")
	if err != nil {
		return nil, err
	}
	fullRun := flag.Lookup("test.run") == nil || flag.Lookup("test.run").Value.String() == ""
	for _, site := range sites {
		if site.exercised(routes.lines[site.file]) {
			continue
		}
		switch {
		case site.pathFunction:
			// Checked for every resource by TestNetworkResourceRoutesInContract.
		case site.method != "" && site.path != "":
			if !allowed(site.method, site.path) {
				problems = append(problems, fmt.Sprintf("%s:%d: %s %s is not an operation in the contract", site.file, site.start, site.method, site.path))
			}
		case fullRun:
			problems = append(problems, fmt.Sprintf("%s:%d: no test sends this request and its path is built at run time, so its route is unchecked", site.file, site.start))
		}
	}
	sort.Strings(problems)
	return problems, nil
}

// contractAllows reports whether a route is in the contract or offContract.
func contractAllows(contract contractRoutes) func(method, path string) bool {
	known := contractRoutes{}
	for _, route := range offContract {
		method, path, _ := strings.Cut(route, " ")
		known.add(method, path)
	}
	return func(method, path string) bool { return contract.match(method, path) || known.match(method, path) }
}

type contractRoutes map[string][]*regexp.Regexp

var pathParameter = regexp.MustCompile(`\{[^/{}]+\}`)

// add records a contract path; each {parameter} matches one path segment.
func (c contractRoutes) add(method, path string) {
	literal := pathParameter.Split(path, -1)
	for i := range literal {
		literal[i] = regexp.QuoteMeta(literal[i])
	}
	c[method] = append(c[method], regexp.MustCompile("^"+strings.Join(literal, "[^/]+")+"$"))
}

func (c contractRoutes) match(method, path string) bool {
	for _, pattern := range c[method] {
		if pattern.MatchString(path) {
			return true
		}
	}
	return false
}

// contractOperations reads the method and path of every operation under the
// contract's top-level paths key.
func contractOperations(file string) (contractRoutes, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	operations := contractRoutes{}
	inPaths, current := false, ""
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), " ")
		switch {
		case line == "paths:":
			inPaths = true
		case !inPaths || strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#"):
		case !strings.HasPrefix(line, " "):
			inPaths = false
		case strings.HasPrefix(line, "  /") || strings.HasPrefix(line, "  '/") || strings.HasPrefix(line, `  "/`):
			current = strings.Trim(strings.TrimSuffix(strings.TrimSpace(line), ":"), `'"`)
		case strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "     ") && current != "":
			switch method := strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(line), ":")); method {
			case "GET", "PUT", "POST", "PATCH", "DELETE", "HEAD", "OPTIONS":
				operations.add(method, current)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(operations) == 0 {
		return nil, fmt.Errorf("no operations found in %s", file)
	}
	return operations, nil
}

type callSite struct {
	file         string
	start, end   int
	method, path string // set when the source alone gives the route
	pathFunction bool   // the path is a networkResource path function
}

func (s callSite) exercised(lines map[int]struct{}) bool {
	for line := range lines {
		if line >= s.start && line <= s.end {
			return true
		}
	}
	return false
}

// apiCallSites finds every Client.do and Client.doH call in the provider's
// non-test files, except do's own call to doH.
func apiCallSites(dir string) ([]callSite, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	var parsed []*ast.File
	helpers := map[string]*ast.FuncDecl{}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			return nil, err
		}
		parsed = append(parsed, f)
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && len(fn.Body.List) == 1 {
				if ret, ok := fn.Body.List[0].(*ast.ReturnStmt); ok && len(ret.Results) == 1 {
					helpers[fn.Name.Name] = fn
				}
			}
		}
	}
	var sites []callSite
	for _, f := range parsed {
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv != nil && (fn.Name.Name == "do" || fn.Name.Name == "doH") {
				continue
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || (sel.Sel.Name != "do" && sel.Sel.Name != "doH") || len(call.Args) < 3 {
					return true
				}
				site := callSite{file: filepath.Base(fset.Position(call.Pos()).Filename), start: fset.Position(call.Pos()).Line, end: fset.Position(call.End()).Line}
				if fn, ok := call.Args[2].(*ast.CallExpr); ok {
					if sel, ok := fn.Fun.(*ast.SelectorExpr); ok {
						switch sel.Sel.Name {
						case "createPath", "readPath", "updatePath", "deletePath":
							site.pathFunction = true
						}
					}
				}
				if method, ok := staticMethod(call.Args[1]); ok {
					if path, ok := staticPath(call.Args[2], helpers, nil); ok {
						path, _, _ = strings.Cut(path, "?")
						site.method, site.path = method, path
					}
				}
				sites = append(sites, site)
				return true
			})
		}
	}
	return sites, nil
}

func staticMethod(expr ast.Expr) (string, bool) {
	if sel, ok := expr.(*ast.SelectorExpr); ok {
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "http" && strings.HasPrefix(sel.Sel.Name, "Method") {
			return strings.ToUpper(strings.TrimPrefix(sel.Sel.Name, "Method")), true
		}
	}
	if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
		value, err := strconv.Unquote(lit.Value)
		return value, err == nil
	}
	return "", false
}

// staticPath evaluates a path built from string literals, url.PathEscape,
// url.QueryEscape and networkValues.segment (one segment each) and one-line
// helper functions such as secretStorePath. Anything else is left to the
// run-time check.
func staticPath(expr ast.Expr, helpers map[string]*ast.FuncDecl, env map[string]string) (string, bool) {
	switch x := expr.(type) {
	case *ast.BasicLit:
		if x.Kind == token.STRING {
			value, err := strconv.Unquote(x.Value)
			return value, err == nil
		}
	case *ast.ParenExpr:
		return staticPath(x.X, helpers, env)
	case *ast.BinaryExpr:
		if x.Op == token.ADD {
			left, lok := staticPath(x.X, helpers, env)
			right, rok := staticPath(x.Y, helpers, env)
			return left + right, lok && rok
		}
	case *ast.Ident:
		value, ok := env[x.Name]
		return value, ok
	case *ast.CallExpr:
		if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "url" && (sel.Sel.Name == "PathEscape" || sel.Sel.Name == "QueryEscape") {
				return "segment", true
			}
			if sel.Sel.Name == "segment" {
				return "segment", true
			}
		}
		if name, ok := x.Fun.(*ast.Ident); ok && helpers[name.Name] != nil {
			fn := helpers[name.Name]
			bound := map[string]string{}
			i := 0
			for _, field := range fn.Type.Params.List {
				for _, param := range field.Names {
					if i < len(x.Args) {
						if value, ok := staticPath(x.Args[i], helpers, env); ok {
							bound[param.Name] = value
						}
					}
					i++
				}
			}
			return staticPath(fn.Body.List[0].(*ast.ReturnStmt).Results[0], helpers, bound)
		}
	}
	return "", false
}

func (r *networkResource) routeSpec() *networkResource { return r }

// Every resource built on networkResource sends its path functions' routes.
func TestNetworkResourceRoutesInContract(t *testing.T) {
	contract, err := contractOperations(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	allowed := contractAllows(contract)
	or := func(method, fallback string) string {
		if method == "" {
			return fallback
		}
		return method
	}
	checked := 0
	for _, newResource := range (&ibeeProvider{}).Resources(context.Background()) {
		spec, ok := newResource().(interface{ routeSpec() *networkResource })
		if !ok {
			continue
		}
		r := spec.routeSpec()
		v := networkValues{}
		for name := range r.attributes {
			v[name] = types.StringValue("sample-" + name)
		}
		for _, route := range []struct {
			method string
			path   func(networkValues) string
		}{{or(r.createMethod, http.MethodPost), r.createPath}, {http.MethodGet, r.readPath}, {or(r.updateMethod, http.MethodPatch), r.updatePath}, {or(r.deleteMethod, http.MethodDelete), r.deletePath}} {
			if route.path == nil {
				continue
			}
			path, _, _ := strings.Cut(route.path(v), "?")
			checked++
			if !allowed(route.method, path) {
				t.Errorf("ibee_%s: %s %s is not an operation in the contract", r.name, route.method, path)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no networkResource routes found")
	}
}

func TestContractRouteMatching(t *testing.T) {
	contract, err := contractOperations(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, patterns := range contract {
		count += len(patterns)
	}
	if count < 150 {
		t.Fatalf("read only %d operations from the contract", count)
	}
	allowed := contractAllows(contract)
	for route, want := range map[string]bool{
		"GET /compute/cloud-vms/vm-1":                           true,
		"PATCH /compute/gpu-vms/vm-1/actions/access":            true,
		"DELETE /object-storage/credentials/AKIA":               true,
		"POST /object-storage/credentials/AKIA/revoke":          false,
		"GET /compute/cloud-vm-snapshots/":                      false,
		"PUT /compute/cloud-vms/vm-1":                           false,
		"GET /object-storage/buckets/assets/cors":               true, // listed in offContract
		"POST /secret-store/stores/store-1/secrets:batchIngest": true,
	} {
		method, path, _ := strings.Cut(route, " ")
		if allowed(method, path) != want {
			t.Errorf("%s: allowed = %v, want %v", route, !want, want)
		}
	}
}
