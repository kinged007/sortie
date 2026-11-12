package agentcore

import (
	"cmp"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const (
	mcpContractRegistryImportPath  = "github.com/sortie-ai/sortie/internal/registry"
	mcpContractAgentcoreImportPath = "github.com/sortie-ai/sortie/internal/agent/agentcore"
	mcpToolDeliveryFunc            = "ResolveToolDelivery"
	mcpToolDeliveryDialectArg      = 3
	mcpContractUndeclaredSelector  = "MCPDialectUndeclared"
	mcpContractNoneSelector        = "MCPDialectNone"
)

var mcpContractAllowlist = map[string]string{
	"agentcore":       "the shared decision package itself; registers no agent kind",
	"agenttest":       "the conformance-assertion package itself; registers no agent kind",
	"dispositiontest": "test-only fixture support package; registers no agent kind",
	"procutil":        "process-launch helper package; registers no agent kind",
	"sshutil":         "SSH helper package; registers no agent kind",
}

type mcpContractViolation struct {
	pos  token.Position
	text string
}

type mcpContractPackage struct {
	dirName string
	files   []*ast.File
}

type mcpDeliveryCall struct {
	pos     token.Position
	dialect string
}

func mcpCompositeLitKeyValue(expr ast.Expr, key string) ast.Expr {
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil
	}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if ident, ok := kv.Key.(*ast.Ident); ok && ident.Name == key {
			return kv.Value
		}
	}
	return nil
}

func mcpRegistrySelector(expr ast.Expr, registryIdent string) string {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == registryIdent {
		return sel.Sel.Name
	}
	return ""
}

func mcpToolDeliveryCalls(fset *token.FileSet, files []*ast.File) []mcpDeliveryCall {
	var calls []mcpDeliveryCall
	for _, file := range files {
		agentcoreIdent := resolveImportName(file, mcpContractAgentcoreImportPath)
		if agentcoreIdent == "" {
			continue
		}
		registryIdent := resolveImportName(file, mcpContractRegistryImportPath)

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != mcpToolDeliveryFunc {
				return true
			}
			if ident, ok := sel.X.(*ast.Ident); !ok || ident.Name != agentcoreIdent {
				return true
			}

			found := mcpDeliveryCall{pos: fset.Position(call.Pos())}
			if registryIdent != "" && len(call.Args) > mcpToolDeliveryDialectArg {
				found.dialect = mcpRegistrySelector(call.Args[mcpToolDeliveryDialectArg], registryIdent)
			}
			calls = append(calls, found)
			return true
		})
	}
	return calls
}

func mcpRegistrationFacts(fset *token.FileSet, files []*ast.File) (registers bool, declared string, pos token.Position) {
	for _, file := range files {
		registryIdent := resolveImportName(file, mcpContractRegistryImportPath)
		if registryIdent == "" {
			continue
		}

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			outer, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			inner, ok := outer.X.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := inner.X.(*ast.Ident)
			if !ok || ident.Name != registryIdent || inner.Sel.Name != "Agents" {
				return true
			}

			switch outer.Sel.Name {
			case "Register":
				registers = true
				pos = fset.Position(call.Pos())
			case "RegisterWithMeta":
				registers = true
				pos = fset.Position(call.Pos())
				if len(call.Args) < 3 {
					return true
				}
				declared = mcpRegistrySelector(mcpCompositeLitKeyValue(call.Args[2], "MCPDialect"), registryIdent)
			}
			return true
		})
	}
	return registers, declared, pos
}

func checkMCPContractPackage(fset *token.FileSet, pkg mcpContractPackage) []mcpContractViolation {
	if _, exempt := mcpContractAllowlist[pkg.dirName]; exempt {
		return nil
	}

	registers, declared, pos := mcpRegistrationFacts(fset, pkg.files)
	if !registers {
		return nil
	}

	if declared == "" || declared == mcpContractUndeclaredSelector {
		return []mcpContractViolation{{pos: pos, text: "registers an agent kind but declares no non-empty MCPDialect"}}
	}

	calls := mcpToolDeliveryCalls(fset, pkg.files)

	var violations []mcpContractViolation
	if declared == mcpContractNoneSelector {
		for _, call := range calls {
			violations = append(violations, mcpContractViolation{
				pos:  call.pos,
				text: "declares " + declared + " but calls agentcore." + mcpToolDeliveryFunc,
			})
		}
		return violations
	}

	if len(calls) == 0 {
		violations = append(violations, mcpContractViolation{
			pos:  pos,
			text: "declares " + declared + " but never calls agentcore." + mcpToolDeliveryFunc,
		})
	}
	for _, call := range calls {
		if call.dialect != declared {
			violations = append(violations, mcpContractViolation{
				pos:  call.pos,
				text: "declares " + declared + " but agentcore." + mcpToolDeliveryFunc + " does not receive that registry constant as its dialect",
			})
		}
	}
	return violations
}

func TestMCPInjectionContractInvariant(t *testing.T) {
	root := ".."

	fset := token.NewFileSet()
	packages := map[string]*mcpContractPackage{}
	var dirOrder []string
	parsed := 0

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			// Pruning an allowlisted directory would also hide packages nested below it.
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		dir := filepath.Dir(path)
		pkg, seen := packages[dir]
		if !seen {
			pkg = &mcpContractPackage{dirName: filepath.Base(dir)}
			packages[dir] = pkg
			dirOrder = append(dirOrder, dir)
		}

		file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			t.Errorf("parse %s: %v", path, parseErr)
			return nil
		}
		pkg.files = append(pkg.files, file)
		parsed++
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if parsed == 0 {
		t.Fatalf("root %s yielded no parsed Go files, want at least one", root)
	}

	sort.Strings(dirOrder)
	for _, dir := range dirOrder {
		for _, v := range checkMCPContractPackage(fset, *packages[dir]) {
			t.Errorf("%s: %s", v.pos, v.text)
		}
	}
}

func TestCheckMCPContractPackage_DetectsViolations(t *testing.T) {
	t.Parallel()

	const header = `package fixture

import (
	"github.com/sortie-ai/sortie/internal/agent/agentcore"
	"github.com/sortie-ai/sortie/internal/registry"
)
`
	register := func(meta string) string {
		return `
func init() {
	registry.Agents.RegisterWithMeta("fixture", newFixtureAdapter, registry.AgentMeta{` + meta + `})
}
`
	}
	start := func(body string) string {
		return "\nfunc start() {\n\t" + body + "\n}\n"
	}
	const (
		deliveringMeta = "MCPDialect: registry.MCPDialectMCPServersTOML,"
		none           = "MCPDialect: registry.MCPDialectNone,"
		deliverTOML    = "agentcore.ResolveToolDelivery(ctx, params, target, registry.MCPDialectMCPServersTOML, logger)"
		deliverJSON    = "agentcore.ResolveToolDelivery(ctx, params, target, registry.MCPDialectMCPServersJSON, logger)"
	)

	tests := []struct {
		name      string
		dirName   string
		src       string
		wantCount int
	}{
		{
			name:      "a RegisterWithMeta literal omitting MCPDialect declares nothing",
			src:       header + register("RequiresCommand: true,"),
			wantCount: 1,
		},
		{
			name:      "a delivering dialect with no ResolveToolDelivery call",
			src:       header + register(deliveringMeta),
			wantCount: 1,
		},
		{
			name:      "a delivering dialect passing its own constant",
			src:       header + register(deliveringMeta) + start(deliverTOML),
			wantCount: 0,
		},
		{
			name:      "a call passing a different dialect constant",
			src:       header + register(deliveringMeta) + start(deliverJSON),
			wantCount: 1,
		},
		{
			name:      "a call passing a variable instead of a registry constant",
			src:       header + register(deliveringMeta) + start("agentcore.ResolveToolDelivery(ctx, params, target, dialect, logger)"),
			wantCount: 1,
		},
		{
			name:      "an allowlisted package registering without a dialect",
			dirName:   "procutil",
			src:       header + "\nfunc init() {\n\tregistry.Agents.Register(\"fixture\", newFixtureAdapter)\n}\n",
			wantCount: 0,
		},
		{
			name:      "none without a call",
			src:       header + register(none),
			wantCount: 0,
		},
		{
			name:      "none calling ResolveToolDelivery",
			src:       header + register(none) + start(deliverTOML),
			wantCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "fixture.go", tt.src, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parser.ParseFile: %v", err)
			}

			got := checkMCPContractPackage(fset, mcpContractPackage{dirName: cmp.Or(tt.dirName, "fixture"), files: []*ast.File{file}})
			if len(got) != tt.wantCount {
				t.Errorf("checkMCPContractPackage() returned %d violations, want %d: %+v", len(got), tt.wantCount, got)
			}
		})
	}
}
