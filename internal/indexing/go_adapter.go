package indexing

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/rvbernucci/forja-guide/internal/contracts"
)

type GoAdapter struct {
	descriptor contracts.AdapterDescriptor
}

//go:embed go_adapter.go
var goAdapterSource []byte

func NewGoAdapter() *GoAdapter {
	return &GoAdapter{descriptor: contracts.AdapterDescriptor{
		Name: "go", Version: runtime.Version(),
		ConfigurationHash: hashText(
			"go/packages:tests=true;network=off;cgo=off;mod=readonly;source=" + hashText(string(goAdapterSource)),
		),
		CapabilityHash: hashText("declarations;imports;references;calls;tests;types"),
	}}
}

func (a *GoAdapter) Descriptor() contracts.AdapterDescriptor { return a.descriptor }
func (a *GoAdapter) Languages() []string                     { return []string{"go"} }

func (a *GoAdapter) Extract(ctx context.Context, root string, documents []SourceDocument) (RawAdapterResult, error) {
	allowed := make(map[string]SourceDocument)
	for _, document := range documents {
		if document.Language == "go" {
			allowed[document.Path] = document
		}
	}
	result := RawAdapterResult{Descriptor: a.descriptor, Symbols: []RawSymbol{}, Relations: []RawRelation{}, Diagnostics: []RawDiagnostic{}}
	if len(allowed) == 0 {
		return result, nil
	}
	config := &packages.Config{
		Context: ctx,
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedModule,
		Dir: root, Env: restrictedGoEnvironment(), Tests: true,
	}
	loaded, err := packages.Load(config, "./...")
	if err != nil {
		return result, fmt.Errorf("load Go packages: %w", err)
	}
	collector := goCollector{
		root: root, allowed: allowed, descriptor: a.descriptor,
		symbols: make(map[string]RawSymbol), objectKeys: make(map[types.Object]string),
		packageKeys: make(map[string]string), relations: make(map[string]RawRelation),
		diagnostics: make(map[string]RawDiagnostic),
	}
	for _, pkg := range loaded {
		collector.collectPackageSymbols(pkg)
	}
	for _, pkg := range loaded {
		collector.collectPackageRelations(pkg)
	}
	result.Symbols = mapSymbols(collector.symbols)
	result.Relations = mapRelations(collector.relations)
	result.Diagnostics = mapDiagnostics(collector.diagnostics)
	return result, nil
}

type goCollector struct {
	root        string
	allowed     map[string]SourceDocument
	descriptor  contracts.AdapterDescriptor
	symbols     map[string]RawSymbol
	objectKeys  map[types.Object]string
	packageKeys map[string]string
	relations   map[string]RawRelation
	diagnostics map[string]RawDiagnostic
}

func (c *goCollector) collectPackageSymbols(pkg *packages.Package) {
	if pkg == nil || pkg.Fset == nil {
		return
	}
	for _, packageError := range pkg.Errors {
		path := c.pathFromError(packageError.Pos)
		if path != "" {
			c.addDiagnostic(RawDiagnostic{Path: path, Severity: "error", Code: "go/packages"})
		}
	}
	packagePath := pkg.PkgPath
	if packagePath == "" {
		packagePath = pkg.ID
	}
	var packageFile string
	for _, syntax := range pkg.Syntax {
		path, ok := c.sourcePath(pkg.Fset, syntax.Pos())
		if ok && (packageFile == "" || path < packageFile) {
			packageFile = path
		}
	}
	if packageFile == "" {
		return
	}
	packageKey := "go:package:" + packagePath
	c.packageKeys[packagePath] = packageKey
	c.addSymbol(RawSymbol{
		Key: packageKey, Path: packageFile, Language: "go", Kind: "package",
		Name: pkg.Name, QualifiedName: packagePath, Signature: "package " + pkg.Name,
		Declaration: zeroRange(), Exported: true,
	})
	if pkg.TypesInfo == nil {
		return
	}
	for _, syntax := range pkg.Syntax {
		path, ok := c.sourcePath(pkg.Fset, syntax.Pos())
		if !ok {
			continue
		}
		for _, declaration := range syntax.Decls {
			c.collectDeclaration(pkg, path, declaration)
		}
	}
}

func (c *goCollector) collectPackageRelations(pkg *packages.Package) {
	if pkg == nil || pkg.Fset == nil || pkg.TypesInfo == nil {
		return
	}
	for _, syntax := range pkg.Syntax {
		path, ok := c.sourcePath(pkg.Fset, syntax.Pos())
		if !ok {
			continue
		}
		c.collectImports(pkg, path, syntax)
		for _, declaration := range syntax.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			object := pkg.TypesInfo.Defs[function.Name]
			sourceKey := c.objectKeys[object]
			if sourceKey != "" {
				c.collectFunctionRelations(pkg, path, sourceKey, function.Body)
			}
		}
	}
}

func (c *goCollector) collectDeclaration(pkg *packages.Package, path string, declaration ast.Decl) {
	switch value := declaration.(type) {
	case *ast.FuncDecl:
		object := pkg.TypesInfo.Defs[value.Name]
		kind := "function"
		if value.Recv != nil {
			kind = "method"
		}
		c.addObjectSymbol(pkg, path, value.Name.Name, kind, value, object, isGoTest(path, value.Name.Name))
	case *ast.GenDecl:
		for _, specification := range value.Specs {
			switch spec := specification.(type) {
			case *ast.TypeSpec:
				kind := "type"
				switch spec.Type.(type) {
				case *ast.StructType:
					kind = "struct"
				case *ast.InterfaceType:
					kind = "interface"
				}
				c.addObjectSymbol(pkg, path, spec.Name.Name, kind, spec, pkg.TypesInfo.Defs[spec.Name], false)
			case *ast.ValueSpec:
				kind := "variable"
				if value.Tok == token.CONST {
					kind = "constant"
				}
				for _, name := range spec.Names {
					c.addObjectSymbol(pkg, path, name.Name, kind, spec, pkg.TypesInfo.Defs[name], false)
				}
			}
		}
	}
}

func (c *goCollector) addObjectSymbol(pkg *packages.Package, path, name, kind string, node ast.Node, object types.Object, test bool) {
	if object == nil {
		c.addDiagnostic(RawDiagnostic{Path: path, Severity: "warning", Code: "go/untyped-declaration"})
		return
	}
	qualified := objectQualifiedName(object)
	key := objectKey(pkg.Fset, object)
	signature := types.TypeString(object.Type(), func(value *types.Package) string { return value.Path() })
	raw := RawSymbol{
		Key: key, Path: path, Language: "go", Kind: kind, Name: name,
		QualifiedName: qualified, Signature: signature,
		Declaration: c.sourceRange(pkg.Fset, node), Exported: ast.IsExported(name), Test: test,
	}
	c.objectKeys[object] = key
	c.addSymbol(raw)
}

func (c *goCollector) collectImports(pkg *packages.Package, path string, file *ast.File) {
	for _, specification := range file.Imports {
		importPath, err := strconv.Unquote(specification.Path.Value)
		if err != nil || importPath == "" {
			c.addDiagnostic(RawDiagnostic{Path: path, Severity: "error", Code: "go/invalid-import"})
			continue
		}
		relation := RawRelation{SourcePath: path, Kind: "imports", EvidenceClass: "confirmed_static", Locator: c.sourceRange(pkg.Fset, specification)}
		if key := c.packageKeys[importPath]; key != "" {
			relation.TargetKey = &key
		} else {
			relation.ExternalName = &importPath
		}
		c.addRelation(relation)
	}
}

func (c *goCollector) collectFunctionRelations(pkg *packages.Package, path, sourceKey string, body *ast.BlockStmt) {
	ast.Inspect(body, func(node ast.Node) bool {
		identifier, ok := node.(*ast.Ident)
		if ok {
			object := pkg.TypesInfo.Uses[identifier]
			if object == nil {
				return true
			}
			relation := RawRelation{SourceKey: &sourceKey, SourcePath: path, Kind: "references", EvidenceClass: "confirmed_static", Locator: c.sourceRange(pkg.Fset, identifier)}
			c.bindGoTarget(&relation, object)
			c.addRelation(relation)
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		object := calledObject(pkg.TypesInfo, call.Fun)
		if object == nil {
			return true
		}
		relation := RawRelation{SourceKey: &sourceKey, SourcePath: path, Kind: "calls", EvidenceClass: "confirmed_static", Locator: c.sourceRange(pkg.Fset, call.Fun)}
		c.bindGoTarget(&relation, object)
		c.addRelation(relation)
		return true
	})
}

func (c *goCollector) bindGoTarget(relation *RawRelation, object types.Object) {
	if key := c.objectKeys[object]; key != "" {
		relation.TargetKey = &key
		return
	}
	name := objectQualifiedName(object)
	relation.ExternalName = &name
}

func (c *goCollector) addSymbol(value RawSymbol) {
	if previous, exists := c.symbols[value.Key]; exists {
		if previous != value {
			c.addDiagnostic(RawDiagnostic{Path: value.Path, Severity: "error", Code: "go/ambiguous-symbol"})
		}
		return
	}
	c.symbols[value.Key] = value
}

func (c *goCollector) addRelation(value RawRelation) {
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	c.relations[hex.EncodeToString(digest[:])] = value
}

func (c *goCollector) addDiagnostic(value RawDiagnostic) {
	c.diagnostics[value.Path+"\x00"+value.Severity+"\x00"+value.Code] = value
}

func (c *goCollector) sourcePath(fileSet *token.FileSet, position token.Pos) (string, bool) {
	absolute := fileSet.PositionFor(position, false).Filename
	if absolute == "" {
		return "", false
	}
	relative, err := filepath.Rel(c.root, absolute)
	if err != nil {
		return "", false
	}
	relative = filepath.ToSlash(relative)
	_, allowed := c.allowed[relative]
	return relative, allowed
}

func (c *goCollector) sourceRange(fileSet *token.FileSet, node ast.Node) contracts.SourceRange {
	start := fileSet.PositionFor(node.Pos(), false)
	end := fileSet.PositionFor(node.End(), false)
	return contracts.SourceRange{
		Start: contracts.SourcePosition{Line: start.Line, Column: start.Column, Offset: start.Offset},
		End:   contracts.SourcePosition{Line: end.Line, Column: end.Column, Offset: end.Offset},
	}
}

func (c *goCollector) pathFromError(value string) string {
	if value == "" {
		return ""
	}
	file := strings.SplitN(value, ":", 2)[0]
	relative, err := filepath.Rel(c.root, file)
	if err != nil {
		return ""
	}
	relative = filepath.ToSlash(relative)
	if _, ok := c.allowed[relative]; !ok {
		return ""
	}
	return relative
}

func calledObject(info *types.Info, expression ast.Expr) types.Object {
	switch value := expression.(type) {
	case *ast.Ident:
		return info.Uses[value]
	case *ast.SelectorExpr:
		if selection := info.Selections[value]; selection != nil {
			return selection.Obj()
		}
		return info.Uses[value.Sel]
	default:
		return nil
	}
}

func objectKey(fileSet *token.FileSet, object types.Object) string {
	position := fileSet.PositionFor(object.Pos(), false)
	return fmt.Sprintf("go:object:%s:%s:%d:%s", packagePath(object), object.Name(), position.Offset, types.TypeString(object.Type(), func(value *types.Package) string { return value.Path() }))
}

func objectQualifiedName(object types.Object) string {
	prefix := packagePath(object)
	if function, ok := object.(*types.Func); ok {
		if signature, ok := function.Type().(*types.Signature); ok && signature.Recv() != nil {
			return prefix + ".(" + types.TypeString(signature.Recv().Type(), func(value *types.Package) string { return value.Path() }) + ")." + object.Name()
		}
	}
	if prefix == "" {
		return "builtin." + object.Name()
	}
	return prefix + "." + object.Name()
}

func packagePath(object types.Object) string {
	if object != nil && object.Pkg() != nil {
		return object.Pkg().Path()
	}
	return ""
}

func isGoTest(path, name string) bool {
	base := filepath.Base(path)
	return strings.HasSuffix(base, "_test.go") || strings.HasPrefix(name, "Test") ||
		strings.HasPrefix(name, "Benchmark") || strings.HasPrefix(name, "Fuzz") || strings.HasPrefix(name, "Example")
}

func zeroRange() contracts.SourceRange {
	position := contracts.SourcePosition{Line: 1, Column: 1, Offset: 0}
	return contracts.SourceRange{Start: position, End: position}
}

func restrictedGoEnvironment() []string {
	keys := []string{"PATH", "HOME", "TMPDIR", "GOROOT", "GOPATH", "GOCACHE", "GOMODCACHE"}
	environment := make([]string, 0, len(keys)+7)
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			environment = append(environment, key+"="+value)
		}
	}
	return append(environment,
		"CGO_ENABLED=0", "GONOSUMDB=*", "GOPROXY=off", "GOSUMDB=off",
		"GOTOOLCHAIN=local", "GOWORK=off", "GOPACKAGESDRIVER=off", "GOFLAGS=-mod=readonly",
	)
}

func hashText(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func mapSymbols(values map[string]RawSymbol) []RawSymbol {
	result := make([]RawSymbol, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result
}

func mapRelations(values map[string]RawRelation) []RawRelation {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]RawRelation, 0, len(keys))
	for _, key := range keys {
		result = append(result, values[key])
	}
	return result
}

func mapDiagnostics(values map[string]RawDiagnostic) []RawDiagnostic {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]RawDiagnostic, 0, len(keys))
	for _, key := range keys {
		result = append(result, values[key])
	}
	return result
}
