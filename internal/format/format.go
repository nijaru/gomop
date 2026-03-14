// Package format provides unified Go formatting in a single pass.
// It combines: import fixing, line shortening, and gofumpt-style rules.
package format

import (
	"bytes"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dave/dst"
	"github.com/dave/dst/decorator"
	"github.com/nijaru/gomop/internal/stdlib"
	"golang.org/x/tools/go/packages"
)

// Options configures the formatter.
type Options struct {
	// LineLength is the target maximum line length (default: 100)
	LineLength int

	// TabWidth is the width of a tab (default: 4)
	TabWidth int

	// GoVersion for version-specific rules (e.g., "go1.24")
	GoVersion string

	// ModulePath for import grouping
	ModulePath string

	// LocalPrefixes for local import grouping
	LocalPrefixes []string

	// ExtraRules enables stricter gofumpt rules
	ExtraRules bool

	// SkipTypeInfo disables type loading (faster but less accurate import detection)
	SkipTypeInfo bool
}

// DefaultOptions returns sensible defaults.
func DefaultOptions() Options {
	return Options{
		LineLength: 100,
		TabWidth:   4,
		GoVersion:  "go1.24",
	}
}

// Formatter performs unified Go formatting.
type Formatter struct {
	fset        *token.FileSet
	opts        Options
	pkg         *packages.Package
	imports     map[string]*dst.ImportSpec // path -> spec
	lineLengths []lineInfo                 // line length info from source
	indent      int                        // current indentation level
}

// New creates a new formatter.
func New(opts Options) *Formatter {
	if opts.LineLength <= 0 {
		opts.LineLength = 100
	}
	if opts.TabWidth <= 0 {
		opts.TabWidth = 4
	}
	return &Formatter{
		fset: token.NewFileSet(),
		opts: opts,
	}
}

// Format formats source in a single pass.
func (f *Formatter) Format(filename string, src []byte) ([]byte, error) { // Step 1: Parse into dst (preserves comments/decorations)
	file, err := decorator.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	// Step 2: Calculate line lengths from source
	f.lineLengths = f.calculateLineLengths(src)

	// Step 3: Single AST walk applying all transformations
	// Type info loading will be triggered in fixImports if needed.
	f.transform(filename, file)

	// Step 4: Print
	var buf bytes.Buffer
	if err := decorator.Fprint(&buf, file); err != nil {
		return nil, fmt.Errorf("print: %w", err)
	}

	return buf.Bytes(), nil
}

// lineLengths stores the length of each line in the source
type lineInfo struct {
	start int // byte offset
	end   int // byte offset
	len   int // visual length with tab expansion
}

// calculateLineLengths calculates visual line lengths from source
func (f *Formatter) calculateLineLengths(src []byte) []lineInfo {
	lines := bytes.Split(src, []byte("\n"))
	result := make([]lineInfo, len(lines))
	offset := 0

	for i, line := range lines {
		visualLen := 0
		for _, c := range line {
			if c == '\t' {
				visualLen += f.opts.TabWidth - (visualLen % f.opts.TabWidth)
			} else {
				visualLen++
			}
		}
		result[i] = lineInfo{
			start: offset,
			end:   offset + len(line),
			len:   visualLen,
		}
		offset += len(line) + 1 // +1 for newline
	}
	return result
}

// loadTypeInfo loads package type information for import resolution.
func (f *Formatter) loadTypeInfo(filename string, src []byte) error { // Create temp file for type checking
	if f.opts.SkipTypeInfo {
		return fmt.Errorf("type loading disabled")
	}

	dir := filepath.Dir(filename)
	if dir == "." {
		dir = "."
	}

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedImports |
			packages.NeedDeps | packages.NeedTypes | packages.NeedTypesInfo,
		Dir: dir,
	}
	if src != nil {
		cfg.Overlay = map[string][]byte{
			filename: src,
		}
	}

	pkgs, err := packages.Load(cfg, "file="+filename)
	if err != nil {
		return fmt.Errorf("packages.Load: %w", err)
	}
	if len(pkgs) == 0 {
		return fmt.Errorf("no package found")
	}
	if len(pkgs[0].Errors) > 0 {
		// Log errors but continue - we can still format
		for _, e := range pkgs[0].Errors {
			fmt.Fprintf(os.Stderr, "warning: %v\n", e)
		}
	}

	f.pkg = pkgs[0]
	return nil
}

// transform applies all formatting transforms in a single walk.
func (f *Formatter) transform(filename string, file *dst.File) { // Reset import tracking
	f.imports = make(map[string]*dst.ImportSpec)

	// Collect existing imports
	for _, decl := range file.Decls {
		if gen, ok := decl.(*dst.GenDecl); ok && gen.Tok == token.IMPORT {
			for _, spec := range gen.Specs {
				if imp, ok := spec.(*dst.ImportSpec); ok {
					path := strings.Trim(imp.Path.Value, `"`)
					f.imports[path] = imp
				}
			}
		}
	}

	// Walk and transform with indentation tracking
	f.indent = 0
	f.transformDecls(file.Decls)

	// Fix imports (add missing, remove unused, group)
	f.fixImports(filename, file)

	// Apply gofumpt-style rules
	f.applyGofumptRules(file)
}

// transformDecls processes declarations with indentation context
func (f *Formatter) transformDecls(decls []dst.Decl) {
	for _, decl := range decls {
		f.transformDecl(decl)
	}
}

// transformDecl processes a declaration with indentation context
func (f *Formatter) transformDecl(decl dst.Decl) {
	switch d := decl.(type) {
	case *dst.FuncDecl:
		// Check if function parameters need splitting
		f.shortenFuncParams(d)
		if d.Body != nil {
			f.indent = 1 // Inside function
			f.transformBlockStmt(d.Body)
			f.indent = 0
		}
		f.normalizeFuncDecl(d)
	case *dst.GenDecl:
		for _, spec := range d.Specs {
			f.transformSpec(spec)
		}
		f.normalizeGenDecl(d)
	}
}

// shortenFuncParams splits long function parameter lists
func (f *Formatter) shortenFuncParams(fn *dst.FuncDecl) {
	if fn.Type == nil || fn.Type.Params == nil || len(fn.Type.Params.List) <= 1 {
		return
	}

	// Calculate total width once
	width := (f.indent * f.opts.TabWidth) + 5 // "func "
	if fn.Name != nil {
		width += len(fn.Name.Name)
	}
	width += 2 // "()"

	for i, field := range fn.Type.Params.List {
		width += f.estimateNodeWidth(field)
		if i < len(fn.Type.Params.List)-1 {
			width += 2 // ", "
		}
	}

	if width > f.opts.LineLength {
		// Split parameters onto separate lines
		for _, field := range fn.Type.Params.List {
			field.Decorations().Before = dst.NewLine
			field.Decorations().After = dst.NewLine
		}
	}
}

// transformBlockStmt processes a block with increased indentation
func (f *Formatter) transformBlockStmt(block *dst.BlockStmt) {
	f.indent++
	for _, stmt := range block.List {
		f.transformStmt(stmt)
	}
	f.indent--
}

// transformStmt processes a statement with indentation context
func (f *Formatter) transformStmt(stmt dst.Stmt) {
	switch s := stmt.(type) {
	case *dst.AssignStmt:
		for _, expr := range s.Rhs {
			f.transformExpr(expr)
		}
	case *dst.BlockStmt:
		f.transformBlockStmt(s)
	case *dst.DeclStmt:
		f.transformDecl(s.Decl)
	case *dst.ExprStmt:
		f.transformExpr(s.X)
	case *dst.ForStmt:
		if s.Body != nil {
			f.transformBlockStmt(s.Body)
		}
	case *dst.IfStmt:
		f.transformExpr(s.Cond)
		if s.Body != nil {
			f.transformBlockStmt(s.Body)
		}
	case *dst.RangeStmt:
		if s.Body != nil {
			f.transformBlockStmt(s.Body)
		}
	case *dst.ReturnStmt:
		for _, expr := range s.Results {
			f.transformExpr(expr)
		}
	case *dst.SwitchStmt:
		if s.Body != nil {
			f.indent++
			for _, c := range s.Body.List {
				if clause, ok := c.(*dst.CaseClause); ok {
					for _, stmt := range clause.Body {
						f.transformStmt(stmt)
					}
				}
			}
			f.indent--
		}
	}
}

// transformExpr processes an expression with indentation context
func (f *Formatter) transformExpr(expr dst.Expr) {
	switch e := expr.(type) {
	case *dst.CallExpr:
		f.shortenCallExpr(e)
		for _, arg := range e.Args {
			f.transformExpr(arg)
		}
		f.transformExpr(e.Fun)
	case *dst.CompositeLit:
		f.shortenCompositeLit(e)
		for _, elt := range e.Elts {
			f.transformExpr(elt)
		}
	case *dst.BinaryExpr:
		f.transformExpr(e.X)
		f.transformExpr(e.Y)
	case *dst.KeyValueExpr:
		f.transformExpr(e.Value)
	case *dst.SelectorExpr:
		f.transformExpr(e.X)
	case *dst.UnaryExpr:
		f.transformExpr(e.X)
	case *dst.ParenExpr:
		f.transformExpr(e.X)
	case *dst.IndexExpr:
		f.transformExpr(e.X)
		f.transformExpr(e.Index)
	case *dst.SliceExpr:
		f.transformExpr(e.X)
		if e.Low != nil {
			f.transformExpr(e.Low)
		}
		if e.High != nil {
			f.transformExpr(e.High)
		}
	case *dst.FuncLit:
		if e.Body != nil {
			f.transformBlockStmt(e.Body)
		}
	}
}

// transformSpec processes a spec
func (f *Formatter) transformSpec(spec dst.Spec) {
	switch s := spec.(type) {
	case *dst.ValueSpec:
		for _, expr := range s.Values {
			f.transformExpr(expr)
		}
	case *dst.TypeSpec:
		f.transformExpr(s.Type)
	}
}

// =============================================================================
// Line Shortening (golines-style)
// =============================================================================

// shortenCallExpr breaks long function calls across multiple lines.
func (f *Formatter) shortenCallExpr(call *dst.CallExpr) {
	if len(call.Args) <= 1 {
		return
	}

	// Calculate width: indent + call expression
	callWidth := f.estimateNodeWidth(call) + (f.indent * f.opts.TabWidth)

	if callWidth > f.opts.LineLength {
		for i, arg := range call.Args {
			if i == 0 {
				arg.Decorations().Before = dst.NewLine
			}
			arg.Decorations().After = dst.NewLine
		}
	}
}

// shortenCompositeLit breaks long composite literals across lines.
func (f *Formatter) shortenCompositeLit(lit *dst.CompositeLit) {
	if len(lit.Elts) <= 1 {
		return
	}

	litWidth := f.estimateNodeWidth(lit) + (f.indent * f.opts.TabWidth)

	if litWidth > f.opts.LineLength {
		for i, elt := range lit.Elts {
			if i == 0 {
				elt.Decorations().Before = dst.NewLine
			}
			elt.Decorations().After = dst.NewLine
		}
	}
}

// estimateNodeWidth estimates formatted width of a node.
// It is non-recursive to avoid O(N^2) complexity.
func (f *Formatter) estimateNodeWidth(node dst.Node) int {
	if node == nil {
		return 0
	}
	width := 0
	dst.Inspect(node, func(n dst.Node) bool {
		if n == nil {
			return true
		}
		switch v := n.(type) {
		case *dst.Ident:
			width += len(v.Name)
		case *dst.BasicLit:
			width += len(v.Value)
		case *dst.BinaryExpr:
			width += 3 // " op "
		case *dst.CallExpr:
			width += 2                     // "()"
			width += (len(v.Args) - 1) * 2 // ", "
		case *dst.CompositeLit:
			width += 2                     // "{}"
			width += (len(v.Elts) - 1) * 2 // ", "
		case *dst.KeyValueExpr:
			width += 2 // ": "
		case *dst.SelectorExpr:
			width += 1 // "."
		}
		return true
	})
	return width
}

// =============================================================================
// Import Management (goimports-style)
// =============================================================================

// groupImports sorts and groups imports: std, third-party, local.
func (f *Formatter) groupImports(specs []*dst.ImportSpec) []*dst.ImportSpec {
	var std, thirdParty, local []*dst.ImportSpec

	for _, spec := range specs {
		// Clean up existing decorations to avoid carry-over newlines
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

	// Sort each group
	sort.Slice(std, f.byPath(std))
	sort.Slice(thirdParty, f.byPath(thirdParty))
	sort.Slice(local, f.byPath(local))

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

func (f *Formatter) byPath(specs []*dst.ImportSpec) func(i, j int) bool {
	return func(i, j int) bool {
		return strings.Trim(specs[i].Path.Value, `"`) < strings.Trim(specs[j].Path.Value, `"`)
	}
}

// isStdLib checks if a path is a standard library package.
func (f *Formatter) isStdLib(path string) bool {
	return stdlib.HasPackage(path)
}

// isLocal checks if a path matches local prefixes.
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

// replaceImports replaces the import declarations in the file.
func (f *Formatter) replaceImports(file *dst.File, specs []*dst.ImportSpec) { // Find and remove existing import declarations
	var decls []dst.Decl
	var importDecl *dst.GenDecl

	for _, decl := range file.Decls {
		if gen, ok := decl.(*dst.GenDecl); ok && gen.Tok == token.IMPORT {
			if importDecl == nil {
				importDecl = gen
				importDecl.Specs = nil // Clear existing
			}
			// Skip other import decls
		} else {
			decls = append(decls, decl)
		}
	}

	// Create new import decl if needed
	if len(specs) > 0 {
		if importDecl == nil {
			importDecl = &dst.GenDecl{
				Tok: token.IMPORT,
			}
		}
		for _, spec := range specs {
			importDecl.Specs = append(importDecl.Specs, spec)
		}

		// Prepend to declarations
		file.Decls = append([]dst.Decl{importDecl}, decls...)
	} else {
		file.Decls = decls
	}
}

// =============================================================================
// Gofumpt Rules
// =============================================================================

// applyGofumptRules applies gofumpt-style formatting rules.
func (f *Formatter) applyGofumptRules(file *dst.File) {
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *dst.FuncDecl:
			f.normalizeFuncDecl(d)
		case *dst.GenDecl:
			f.normalizeGenDecl(d)
		}
	}
}

// normalizeFuncDecl applies rules to function declarations.
func (f *Formatter) normalizeFuncDecl(fn *dst.FuncDecl) {
	if fn.Body == nil {
		return
	}

	// Remove empty lines at start/end of function body
	if len(fn.Body.List) > 0 {
		fn.Body.List[0].Decorations().Before = dst.None
		fn.Body.List[len(fn.Body.List)-1].Decorations().After = dst.None
	}

	// Add newline between ) and { for multi-line params
	if fn.Type.Params != nil && len(fn.Type.Params.List) > 0 {
		// Check if params span multiple lines
		// If so, ensure closing ) is on its own line
		if len(fn.Type.Params.List) > 2 {
			// Add newline before closing paren
		}
	}
}

// normalizeGenDecl applies rules to generic declarations.
func (f *Formatter) normalizeGenDecl(gen *dst.GenDecl) {
	switch gen.Tok {
	case token.VAR:
		// Single var declarations should not be grouped
		if len(gen.Specs) == 1 {
			if vs, ok := gen.Specs[0].(*dst.ValueSpec); ok {
				if len(vs.Names) == 1 && len(vs.Values) == 1 {
					// Could simplify to short declaration if in function
				}
			}
		}

		// Align consecutive var declarations
		// (handled by printer)

	case token.TYPE:
		// Empty interfaces/structs should be single line
		for _, spec := range gen.Specs {
			if ts, ok := spec.(*dst.TypeSpec); ok {
				f.normalizeTypeSpec(ts)
			}
		}
	}
}

// normalizeTypeSpec normalizes type specifications.
func (f *Formatter) normalizeTypeSpec(ts *dst.TypeSpec) {
	switch t := ts.Type.(type) {
	case *dst.StructType:
		if t.Fields == nil || len(t.Fields.List) == 0 {
			// Empty struct - already fine
		}
	case *dst.InterfaceType:
		if t.Methods == nil || len(t.Methods.List) == 0 {
			// Empty interface - already fine
		}
	}
}

// simplifyValueSpec simplifies variable specifications.
func (f *Formatter) simplifyValueSpec(vs *dst.ValueSpec) {
	// If single var with single value, could use :=
	// (context-dependent, handled elsewhere)
}
