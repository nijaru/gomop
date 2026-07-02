package format

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dave/dst"
	"github.com/nijaru/gomop/internal/stdlib"
	"golang.org/x/tools/go/packages"
)

// fixImports adds missing imports, removes unused, and groups them.
// Uses tiered resolution: AST matching → stdlib manifest → sibling scan.
// Does NOT use packages.Load — that's goimports' job.
func (f *Formatter) fixImports(filename string, file *dst.File) {
	// 1. Collect existing imports
	importPaths := make(map[string]string) // name → path
	importNames := make(map[string]string) // path → name

	for _, decl := range file.Decls {
		if gen, ok := decl.(*dst.GenDecl); ok && gen.Tok == token.IMPORT {
			for _, spec := range gen.Specs {
				if imp, ok := spec.(*dst.ImportSpec); ok {
					path := strings.Trim(imp.Path.Value, `"`)
					name := ""
					if imp.Name != nil {
						name = imp.Name.Name
					} else {
						name = f.getAssumedName(path)
					}
					importPaths[name] = path
					importNames[path] = name
				}
			}
		}
	}

	// 2. Use references collected during the transform walk
	refs := f.packageRefs

	// 3. Determine used imports and potential missing ones
	usedImports := make(map[string]bool)
	unresolved := make(map[string][]string)

	for pkgName, symbols := range refs {
		if path, ok := importPaths[pkgName]; ok {
			usedImports[path] = true
		} else {
			unresolved[pkgName] = symbols
		}
	}

	// 4. Resolve missing imports from stdlib (O(1) manifest lookup)
	for pkgName, symbols := range unresolved {
		if path := f.resolveStdlib(pkgName, symbols); path != "" {
			if _, exists := f.imports[path]; !exists {
				spec := &dst.ImportSpec{
					Path: &dst.BasicLit{Kind: token.STRING, Value: fmt.Sprintf("%q", path)},
				}
				f.imports[path] = spec
			}
			usedImports[path] = true
			delete(unresolved, pkgName)
		}
	}

	// 5. Check siblings for unresolved symbols (may be local package globals).
	// Skip if Fast mode is enabled.
	if len(unresolved) > 0 && !f.opts.Fast {
		globals := f.collectSiblingGlobals(filename, file.Name.Name)
		for pkgName := range unresolved {
			if globals[pkgName] {
				delete(unresolved, pkgName)
			}
		}
	}

	// 6. Full type resolution (opt-in via --resolve). Uses packages.Load
	// for third-party import detection. Off by default to avoid the ~40ms overhead.
	if len(unresolved) > 0 && f.opts.Resolve {
		resolved := f.resolveTypeInfo(filename)
		for pkgName := range unresolved {
			if path, ok := resolved[pkgName]; ok {
				usedImports[path] = true
				delete(unresolved, pkgName)
			}
		}
	}

	// Remaining unresolved are third-party packages — don't auto-add them.
	// Existing imports matched by name are kept; unmatched ones are removed.

	// 7. Build new import list
	var newImports []*dst.ImportSpec
	for path, spec := range f.imports {
		if usedImports[path] {
			newImports = append(newImports, spec)
		}
	}

	newImports = f.groupImports(newImports)
	f.replaceImports(file, newImports)
}

// resolveTypeInfo uses packages.Load to resolve third-party package names
// to their import paths. Only called when --resolve is enabled.
func (f *Formatter) resolveTypeInfo(filename string) map[string]string {
	dir := filepath.Dir(filename)
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedImports |
			packages.NeedTypes | packages.NeedTypesInfo,
		Dir: dir,
	}
	pkgs, err := packages.Load(cfg, "file="+filename)
	if err != nil || len(pkgs) == 0 {
		return nil
	}
	if pkgs[0].TypesInfo == nil {
		return nil
	}

	resolved := make(map[string]string)
	for _, obj := range pkgs[0].TypesInfo.Uses {
		if pn, ok := obj.(*types.PkgName); ok {
			resolved[pn.Name()] = pn.Imported().Path()
		}
	}
	return resolved
}

// collectSiblingGlobals finds all globals declared in sibling files.
// Uses caching to avoid re-parsing the same directory multiple times.
func (f *Formatter) collectSiblingGlobals(filename string, packageName string) map[string]bool {
	dir := filepath.Dir(filename)
	if globals, ok := f.siblingCache[dir]; ok {
		return globals
	}

	files, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	globals := make(map[string]bool)
	fset := token.NewFileSet()

	for _, fi := range files {
		if fi.IsDir() || !strings.HasSuffix(fi.Name(), ".go") ||
			fi.Name() == filepath.Base(filename) ||
			strings.HasSuffix(fi.Name(), "_test.go") {
			continue
		}

		path := filepath.Join(dir, fi.Name())
		node, err := parser.ParseFile(fset, path, nil, parser.DeclarationErrors)
		if err != nil || node.Name.Name != packageName {
			continue
		}

		for _, decl := range node.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.ValueSpec:
						for _, name := range s.Names {
							globals[name.Name] = true
						}
					case *ast.TypeSpec:
						globals[s.Name.Name] = true
					}
				}
			case *ast.FuncDecl:
				globals[d.Name.Name] = true
			}
		}
	}

	f.siblingCache[dir] = globals
	return globals
}

// resolveStdlib tries to find a stdlib package for pkgName that has all symbols.
func (f *Formatter) resolveStdlib(pkgName string, symbols []string) string {
	candidates := stdlib.PackageByName[pkgName]
	if len(candidates) == 0 {
		return ""
	}
	for _, path := range candidates {
		pkgSymbols := stdlib.PackageSymbols[path]
		allFound := true
		for _, symName := range symbols {
			found := false
			for _, s := range pkgSymbols {
				if s.Name == symName {
					found = true
					break
				}
			}
			if !found {
				allFound = false
				break
			}
		}
		if allFound {
			return path
		}
	}
	return ""
}

func (f *Formatter) getAssumedName(path string) string {
	return stdlib.GetAssumedName(path)
}

// =============================================================================
// Import Grouping
// =============================================================================

func (f *Formatter) groupImports(specs []*dst.ImportSpec) []*dst.ImportSpec {
	var std, thirdParty, local []*dst.ImportSpec

	for _, spec := range specs {
		spec.Decorations().Before = dst.None
		spec.Decorations().After = dst.None
		path := strings.Trim(spec.Path.Value, `"`)
		if f.isStdLib(path) {
			std = append(std, spec)
		} else if f.isLocal(path) {
			local = append(local, spec)
		} else {
			thirdParty = append(thirdParty, spec)
		}
	}

	slices.SortFunc(std, f.cmpByPath)
	slices.SortFunc(thirdParty, f.cmpByPath)
	slices.SortFunc(local, f.cmpByPath)

	var result []*dst.ImportSpec
	result = append(result, std...)
	if len(std) > 0 && (len(thirdParty) > 0 || len(local) > 0) {
		std[len(std)-1].Decorations().After = dst.EmptyLine
	}
	result = append(result, thirdParty...)
	if len(thirdParty) > 0 && len(local) > 0 {
		thirdParty[len(thirdParty)-1].Decorations().After = dst.EmptyLine
	}
	result = append(result, local...)
	return result
}

func (f *Formatter) cmpByPath(a, b *dst.ImportSpec) int {
	return strings.Compare(
		strings.Trim(a.Path.Value, `"`),
		strings.Trim(b.Path.Value, `"`),
	)
}

func (f *Formatter) isStdLib(path string) bool {
	return stdlib.HasPackage(path)
}

func (f *Formatter) isLocal(path string) bool {
	for _, prefix := range f.opts.LocalPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	if f.opts.ModulePath != "" && strings.HasPrefix(path, f.opts.ModulePath) {
		return true
	}
	return false
}

func (f *Formatter) replaceImports(file *dst.File, specs []*dst.ImportSpec) {
	var decls []dst.Decl
	var importDecl *dst.GenDecl

	for _, decl := range file.Decls {
		if gen, ok := decl.(*dst.GenDecl); ok && gen.Tok == token.IMPORT {
			if importDecl == nil {
				importDecl = gen
				importDecl.Specs = nil
			}
		} else {
			decls = append(decls, decl)
		}
	}

	if len(specs) > 0 {
		if importDecl == nil {
			importDecl = &dst.GenDecl{Tok: token.IMPORT}
		}
		for _, spec := range specs {
			importDecl.Specs = append(importDecl.Specs, spec)
		}
		file.Decls = append([]dst.Decl{importDecl}, decls...)
	} else {
		file.Decls = decls
	}
}
