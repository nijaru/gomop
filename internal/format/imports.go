package format

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"

	"github.com/dave/dst"
	"github.com/nijaru/gomop/internal/stdlib"
)

// fixImports adds missing imports, removes unused, and groups them.
func (f *Formatter) fixImports(filename string, file *dst.File) {
	// 1. Collect existing imports
	importPaths := make(map[string]string) // name -> path
	importNames := make(map[string]string) // path -> name

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

	// 2. Collect all references (pkg.Name)
	refs := f.collectPotentialPackageRefs(file)

	// 3. Determine used imports and potential missing ones
	usedImports := make(map[string]bool)
	unresolved := make(map[string][]string) // pkgName -> [symbols...]

	for pkgName, symbols := range refs {
		if path, ok := importPaths[pkgName]; ok {
			usedImports[path] = true
		} else {
			unresolved[pkgName] = symbols
		}
	}

	// 4. Resolve missing imports from stdlib
	for pkgName, symbols := range unresolved {
		if path := f.resolveStdlib(pkgName, symbols); path != "" {
			if _, exists := f.imports[path]; !exists {
				// Create new import spec
				spec := &dst.ImportSpec{
					Path: &dst.BasicLit{Kind: token.STRING, Value: fmt.Sprintf("%q", path)},
				}
				f.imports[path] = spec
			}
			usedImports[path] = true
			delete(unresolved, pkgName)
		}
	}

	// 5. Tier 2: Check siblings for unresolved symbols
	if len(unresolved) > 0 {
		globals := f.collectSiblingGlobals(filename, file.Name.Name)
		for pkgName := range unresolved {
			if globals[pkgName] {
				// It's a global from a sibling file, not a package call
				delete(unresolved, pkgName)
			}
		}
	}

	// 6. Tier 3: If we still have unresolved and haven't loaded type info, consider it
	if len(unresolved) > 0 && f.pkg == nil && !f.opts.SkipTypeInfo {
		// Fallback to packages.Load if we have unresolved imports that are not in stdlib
		// or if we want to be more accurate.
		// For now, let's trigger it.
		if err := f.loadTypeInfo(filename, nil); err == nil {
			// Second pass: use type info to resolve package names
			if f.pkg != nil && f.pkg.TypesInfo != nil {
				for _, obj := range f.pkg.TypesInfo.Uses {
					if pkgName, ok := obj.(*types.PkgName); ok {
						path := pkgName.Imported().Path()
						usedImports[path] = true
					}
				}
			}
		}
	}

	// 7. Build new import list
	var newImports []*dst.ImportSpec
	for path, spec := range f.imports {
		if usedImports[path] {
			newImports = append(newImports, spec)
		}
	}

	// Sort and group imports
	newImports = f.groupImports(newImports)

	// Replace imports in file
	f.replaceImports(file, newImports)
}

// collectPotentialPackageRefs finds all pkg.Name references.
func (f *Formatter) collectPotentialPackageRefs(file *dst.File) map[string][]string {
	refs := make(map[string][]string)
	dst.Inspect(file, func(node dst.Node) bool {
		if sel, ok := node.(*dst.SelectorExpr); ok {
			if ident, ok := sel.X.(*dst.Ident); ok {
				// Potential package reference
				refs[ident.Name] = append(refs[ident.Name], sel.Sel.Name)
			}
		}
		return true
	})
	return refs
}

// collectSiblingGlobals finds all globals declared in sibling files.
func (f *Formatter) collectSiblingGlobals(filename string, packageName string) map[string]bool {
	dir := filepath.Dir(filename)
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	globals := make(map[string]bool)
	fset := token.NewFileSet()

	for _, fi := range files {
		if fi.IsDir() || !strings.HasSuffix(fi.Name(), ".go") || fi.Name() == filepath.Base(filename) || strings.HasSuffix(fi.Name(), "_test.go") {
			continue
		}

		path := filepath.Join(dir, fi.Name())
		// Only parse top-level declarations
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
