// Package format provides unified Go formatting in a single pass.
// It combines: import fixing, line shortening, and gofumpt-style rules.
package format

import (
	"bytes"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"slices"
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

	// siblingCache caches globals from sibling files (directory -> globals)
	siblingCache map[string]map[string]bool
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
		fset:         token.NewFileSet(),
		opts:         opts,
		siblingCache: make(map[string]map[string]bool),
	}
}

// Format formats source in a single pass.
func (f *Formatter) Format(filename string, src []byte) ([]byte, error) {
	// Step 1: Parse into dst (preserves comments/decorations)
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

	// Convert interface{} to any
	f.convertEmptyInterfacesToAny(file)
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
	// Process statements and simplify var declarations
	for i, stmt := range block.List {
		// Check for var x = value that can be simplified to x := value
		if simplified := f.simplifyVarDecl(stmt); simplified != nil {
			block.List[i] = simplified
			continue
		}
		f.transformStmt(stmt)
	}
	f.indent--
}

// simplifyVarDecl converts var x = value to x := value inside functions.
// Returns nil if the statement cannot be simplified.
func (f *Formatter) simplifyVarDecl(stmt dst.Stmt) dst.Stmt {
	// Must be a DeclStmt
	declStmt, ok := stmt.(*dst.DeclStmt)
	if !ok {
		return nil
	}

	// Must be a GenDecl with VAR token
	genDecl, ok := declStmt.Decl.(*dst.GenDecl)
	if !ok || genDecl.Tok != token.VAR {
		return nil
	}

	// Must have exactly one spec
	if len(genDecl.Specs) != 1 {
		return nil
	}

	// Must be a ValueSpec
	valueSpec, ok := genDecl.Specs[0].(*dst.ValueSpec)
	if !ok {
		return nil
	}

	// Must have exactly one name and one value, and no type
	if len(valueSpec.Names) != 1 || len(valueSpec.Values) != 1 || valueSpec.Type != nil {
		return nil
	}

	// Create an AssignStmt with DEFINE
	assign := &dst.AssignStmt{
		Lhs: []dst.Expr{valueSpec.Names[0]},
		Tok: token.DEFINE,
		Rhs: []dst.Expr{valueSpec.Values[0]},
	}
	// Copy before/after decorations from the original statement
	assign.Decs.Before = declStmt.Decs.Before
	assign.Decs.After = declStmt.Decs.After

	return assign
}

// transformStmt processes a statement with indentation context
func (f *Formatter) transformStmt(stmt dst.Stmt) {
	switch s := stmt.(type) {
	case *dst.AssignStmt:
		for i, expr := range s.Rhs {
			// Check for method chain first
			if f.isMethodChain(expr) {
				s.Rhs[i] = f.shortenMethodChain(expr)
			} else if split := f.splitLongString(expr, s.Lhs, s.Tok); split != nil {
				// Check if this is a long string that needs splitting
				s.Rhs[i] = split
			} else {
				f.transformExpr(expr)
			}
		}
	case *dst.BlockStmt:
		f.transformBlockStmt(s)
	case *dst.CaseClause:
		f.shortenCaseClause(s)
		for _, stmt := range s.Body {
			f.transformStmt(stmt)
		}
	case *dst.CommClause:
		for _, stmt := range s.Body {
			f.transformStmt(stmt)
		}
	case *dst.DeclStmt:
		f.transformDecl(s.Decl)
	case *dst.ExprStmt:
		// Check for method chain in expression statements
		if call, ok := s.X.(*dst.CallExpr); ok {
			f.shortenMethodChain(call)
		}
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
		for i, expr := range s.Results {
			if f.isMethodChain(expr) {
				s.Results[i] = f.shortenMethodChain(expr)
			} else if split := f.splitLongString(expr, nil, 0); split != nil {
				s.Results[i] = split
			} else {
				f.transformExpr(expr)
			}
		}
	case *dst.SelectStmt:
		if s.Body != nil {
			f.indent++
			for _, c := range s.Body.List {
				if clause, ok := c.(*dst.CommClause); ok {
					for _, stmt := range clause.Body {
						f.transformStmt(stmt)
					}
				}
			}
			f.indent--
		}
	case *dst.SwitchStmt:
		if s.Body != nil {
			f.indent++
			for _, c := range s.Body.List {
				if clause, ok := c.(*dst.CaseClause); ok {
					f.shortenCaseClause(clause)
					for _, stmt := range clause.Body {
						f.transformStmt(stmt)
					}
				}
			}
			f.indent--
		}
	case *dst.TypeSwitchStmt:
		if s.Body != nil {
			f.indent++
			for _, c := range s.Body.List {
				if clause, ok := c.(*dst.CaseClause); ok {
					f.shortenCaseClause(clause)
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
	case *dst.BasicLit:
		f.transformBasicLit(e)
	case *dst.CallExpr:
		f.shortenCallExpr(e)
		for _, arg := range e.Args {
			f.transformExpr(arg)
		}
		f.transformExpr(e.Fun)
	case *dst.CompositeLit:
		f.shortenCompositeLit(e)
		// Check if the type is an anonymous struct
		if structType, ok := e.Type.(*dst.StructType); ok {
			f.shortenAnonymousStruct(structType)
		}
		for _, elt := range e.Elts {
			f.transformExpr(elt)
		}
	case *dst.BinaryExpr:
		f.shortenBinaryExpr(e)
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
	case *dst.StructType:
		f.shortenAnonymousStruct(e)
	case *dst.InterfaceType:
		// Empty interfaces are converted to 'any' in convertEmptyInterfacesToAny
	}
}

// convertEmptyInterfacesToAny transforms interface{} to any throughout the file.
func (f *Formatter) convertEmptyInterfacesToAny(file *dst.File) {
	dst.Inspect(file, func(n dst.Node) bool {
		switch node := n.(type) {
		// Function parameters and results
		case *dst.FuncDecl:
			if node.Type != nil {
				if node.Type.Params != nil {
					for _, field := range node.Type.Params.List {
						field.Type = f.toAnyIfEmptyInterface(field.Type)
					}
				}
				if node.Type.Results != nil {
					for _, field := range node.Type.Results.List {
						field.Type = f.toAnyIfEmptyInterface(field.Type)
					}
				}
			}
		// Field lists (struct fields, interface methods)
		case *dst.StructType:
			if node.Fields != nil {
				for _, field := range node.Fields.List {
					field.Type = f.toAnyIfEmptyInterface(field.Type)
				}
			}
		// Variable declarations
		case *dst.ValueSpec:
			if node.Type != nil {
				node.Type = f.toAnyIfEmptyInterface(node.Type)
			}
		// Type declarations
		case *dst.TypeSpec:
			node.Type = f.toAnyIfEmptyInterface(node.Type)
		// Type assertions
		case *dst.TypeAssertExpr:
			if node.Type != nil {
				node.Type = f.toAnyIfEmptyInterface(node.Type)
			}
		// Map value types
		case *dst.MapType:
			node.Value = f.toAnyIfEmptyInterface(node.Value)
		// Array/Slice element types
		case *dst.ArrayType:
			node.Elt = f.toAnyIfEmptyInterface(node.Elt)
		// Chan element types
		case *dst.ChanType:
			node.Value = f.toAnyIfEmptyInterface(node.Value)
		}
		return true
	})
}

// toAnyIfEmptyInterface returns an 'any' ident if the type is an empty interface.
func (f *Formatter) toAnyIfEmptyInterface(expr dst.Expr) dst.Expr {
	if iface, ok := expr.(*dst.InterfaceType); ok {
		if iface.Methods == nil || len(iface.Methods.List) == 0 {
			return &dst.Ident{Name: "any"}
		}
	}
	return expr
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

// transformBasicLit transforms basic literals (octal, etc.)
func (f *Formatter) transformBasicLit(lit *dst.BasicLit) {
	if lit.Kind != token.INT {
		return
	}

	// Convert old-style octal literals (0644) to new style (0o644)
	// Old style: starts with 0 and has only digits 0-7, length > 1
	// New style: starts with 0o
	value := lit.Value
	if len(value) > 1 && value[0] == '0' {
		// Check if it's already new style (0o, 0x, 0b)
		if len(value) > 2 && (value[1] == 'o' || value[1] == 'O' || value[1] == 'x' || value[1] == 'X' || value[1] == 'b' || value[1] == 'B') {
			return
		}
		// Check if it's an old-style octal (only digits 0-7)
		isOctal := true
		for i := 1; i < len(value); i++ {
			c := value[i]
			if c < '0' || c > '7' {
				isOctal = false
				break
			}
		}
		if isOctal {
			// Convert to new style: 0644 -> 0o644
			lit.Value = "0o" + value[1:]
		}
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

// shortenMethodChain splits long method chains across multiple lines.
func (f *Formatter) shortenMethodChain(expr dst.Expr) dst.Expr {
	// Check if this is a method chain and collect selectors
	selectors := f.collectMethodChainSelectors(expr)
	if len(selectors) <= 1 {
		return expr
	}

	// Calculate total width
	width := f.estimateNodeWidth(expr) + (f.indent * f.opts.TabWidth)
	if width <= f.opts.LineLength {
		return expr
	}

	// Split the chain by adding newlines before each selector's X
	for _, sel := range selectors {
		// Add newline before the dot
		sel.Decs.X.Prepend("\n")
	}

	return expr
}

// isMethodChain checks if an expression is a method chain (2+ chained calls).
func (f *Formatter) isMethodChain(expr dst.Expr) bool {
	selectors := f.collectMethodChainSelectors(expr)
	return len(selectors) >= 2
}

// collectMethodChainSelectors collects all SelectorExpr nodes in a method chain.
func (f *Formatter) collectMethodChainSelectors(expr dst.Expr) []*dst.SelectorExpr {
	var selectors []*dst.SelectorExpr
	current := expr

	for {
		call, ok := current.(*dst.CallExpr)
		if !ok {
			break
		}

		sel, ok := call.Fun.(*dst.SelectorExpr)
		if !ok {
			break
		}

		selectors = append(selectors, sel)

		// Move to the receiver (X) of the selector
		current = sel.X
	}

	return selectors
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

// shortenBinaryExpr splits long boolean expressions at && / || operators.
func (f *Formatter) shortenBinaryExpr(expr *dst.BinaryExpr) {
	// Only split on logical operators
	if expr.Op != token.LAND && expr.Op != token.LOR {
		return
	}

	// Check if the expression is too long
	width := f.estimateNodeWidth(expr) + (f.indent * f.opts.TabWidth)
	if width <= f.opts.LineLength {
		return
	}

	// Split at every &&/|| in the chain by recursively marking right operands
	f.splitLogicalChain(expr, expr.Op)
}

// splitLogicalChain marks newline splits at each logical operator matching op.
func (f *Formatter) splitLogicalChain(expr dst.Expr, op token.Token) {
	bin, ok := expr.(*dst.BinaryExpr)
	if !ok || bin.Op != op {
		return
	}
	// Recurse into the left side
	f.splitLogicalChain(bin.X, op)
	// Add newline before the right operand
	bin.Y.Decorations().Before = dst.NewLine
}

// shortenCaseClause collapses short case clauses onto a single line.
// This implements gofumpt's "Short case clauses should take a single line" rule.
func (f *Formatter) shortenCaseClause(clause *dst.CaseClause) {
	if len(clause.List) == 0 {
		return // default case
	}

	// Estimate width of case expressions: "case " + exprs + ":"
	width := 6 // "case "
	for i, expr := range clause.List {
		width += f.estimateNodeWidth(expr)
		if i < len(clause.List)-1 {
			width += 2 // ", "
		}
	}
	width += 1 // ":"

	// Add indentation
	width += f.indent * f.opts.TabWidth

	// If it fits comfortably (with room for at least one body statement), collapse
	if width < f.opts.LineLength-20 {
		// Remove newlines between case expressions
		for _, expr := range clause.List {
			expr.Decorations().Before = dst.None
			expr.Decorations().After = dst.None
		}
	}
}

// splitLongString splits a long string literal into concatenated parts.
// Returns nil if the string doesn't need splitting.
func (f *Formatter) splitLongString(expr dst.Expr, lhs []dst.Expr, tok token.Token) dst.Expr {
	lit, ok := expr.(*dst.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return nil
	}

	// Calculate the line width including assignment
	prefix := f.indent * f.opts.TabWidth
	if len(lhs) > 0 {
		for _, l := range lhs {
			prefix += f.estimateNodeWidth(l)
		}
		if tok == token.DEFINE {
			prefix += 2 // ":="
		} else {
			prefix += 1 // "="
		}
		prefix += 1 // space
	}

	// Check if the string is too long
	totalWidth := prefix + len(lit.Value)
	if totalWidth <= f.opts.LineLength {
		return nil
	}

	// Extract string content (without quotes)
	raw := lit.Value
	isRaw := strings.HasPrefix(raw, "`")
	var content string
	if isRaw {
		content = strings.Trim(raw, "`")
	} else {
		content = strings.Trim(raw, `"`)
		// Unescape basic sequences for length calculation
		content = strings.ReplaceAll(content, `\n`, "\n")
		content = strings.ReplaceAll(content, `\t`, "\t")
	}

	// Don't split raw strings or strings with newlines
	if isRaw || strings.Contains(content, "\n") {
		return nil
	}

	// Calculate how much space we have for each line
	availableWidth := f.opts.LineLength - prefix - 2 // 2 for quotes

	// Don't split if the available width is too small
	if availableWidth < 20 {
		return nil
	}

	// Split into chunks at word boundaries
	chunks := f.splitStringAtWords(content, availableWidth)
	if len(chunks) <= 1 {
		return nil
	}

	// Build a binary expression tree of concatenated strings
	var result dst.Expr
	for i, chunk := range chunks {
		part := &dst.BasicLit{
			Kind:  token.STRING,
			Value: `"` + chunk + `"`,
		}
		if i == 0 {
			result = part
		} else {
			result = &dst.BinaryExpr{
				Op: token.ADD,
				X:  result,
				Y:  part,
			}
			// Add newline before continuation
			part.Decorations().Before = dst.NewLine
		}
	}

	return result
}

// splitStringAtWords splits a string into chunks at word boundaries.
func (f *Formatter) splitStringAtWords(s string, maxWidth int) []string {
	if len(s) <= maxWidth {
		return []string{s}
	}

	var chunks []string
	words := strings.Fields(s)
	if len(words) == 0 {
		// No spaces, just split at max width
		for len(s) > maxWidth {
			chunks = append(chunks, s[:maxWidth])
			s = s[maxWidth:]
		}
		if len(s) > 0 {
			chunks = append(chunks, s)
		}
		return chunks
	}

	var current strings.Builder
	for _, word := range words {
		if current.Len() == 0 {
			current.WriteString(word)
		} else if current.Len()+1+len(word) <= maxWidth {
			current.WriteString(" ")
			current.WriteString(word)
		} else {
			chunks = append(chunks, current.String())
			current.Reset()
			current.WriteString(word)
		}
	}
	if current.Len() > 0 {
		chunks = append(chunks, current.String())
	}

	return chunks
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
			if len(v.Args) > 1 {
				width += (len(v.Args) - 1) * 2 // ", "
			}
		case *dst.CompositeLit:
			width += 2                     // "{}"
			if len(v.Elts) > 1 {
				width += (len(v.Elts) - 1) * 2 // ", "
			}
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
	return strings.Compare(strings.Trim(a.Path.Value, `"`), strings.Trim(b.Path.Value, `"`))
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

	// Enforce empty lines between multiline top-level declarations
	f.separateMultilineDecls(file.Decls)

	// Enforce comment whitespace
	f.enforceCommentWhitespace(file)
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

	// Group adjacent params with the same type
	f.groupFuncParams(fn)
}

// separateMultilineDecls ensures top-level declarations that span multiple lines
// have empty lines between them.
func (f *Formatter) separateMultilineDecls(decls []dst.Decl) {
	if len(decls) < 2 {
		return
	}

	for i := 1; i < len(decls); i++ {
		prev := decls[i-1]
		curr := decls[i]

		isPrevMulti := f.isMultilineDecl(prev)
		isCurrMulti := f.isMultilineDecl(curr)

		if isPrevMulti || isCurrMulti {
			// Ensure empty line between them
			// If previous is import, usually grouped, but wait: imports are already separated by fixImports
			if !f.isImportDecl(prev) && !f.isImportDecl(curr) {
				curr.Decorations().Before = dst.EmptyLine
			}
		}
	}
}

func (f *Formatter) isImportDecl(decl dst.Decl) bool {
	if gen, ok := decl.(*dst.GenDecl); ok && gen.Tok == token.IMPORT {
		return true
	}
	return false
}

func (f *Formatter) hasNewlineSpace(space dst.SpaceType) bool {
	return space == dst.NewLine || space == dst.EmptyLine
}

// isMultilineDecl determines if a declaration spans multiple lines.
func (f *Formatter) isMultilineDecl(decl dst.Decl) bool {
	switch d := decl.(type) {
	case *dst.FuncDecl:
		if d.Body != nil {
			if len(d.Body.List) > 1 {
				return true
			}
			if len(d.Body.List) == 1 {
				stmt := d.Body.List[0]
				if f.hasNewlineSpace(stmt.Decorations().Before) || f.hasNewlineSpace(stmt.Decorations().After) {
					return true
				}
			}
		}
		// Check if params are multiline
		if d.Type != nil && d.Type.Params != nil {
			for _, p := range d.Type.Params.List {
				if f.hasNewlineSpace(p.Decs.Before) || f.hasNewlineSpace(p.Decs.After) {
					return true
				}
			}
		}
		return false
	case *dst.GenDecl:
		if d.Lparen {
			// var (...) or type (...) or const (...) block
			return true
		}
		
		for _, spec := range d.Specs {
			switch s := spec.(type) {
			case *dst.TypeSpec:
				switch t := s.Type.(type) {
				case *dst.StructType:
					if t.Fields != nil && len(t.Fields.List) > 0 {
						return true
					}
				case *dst.InterfaceType:
					if t.Methods != nil && len(t.Methods.List) > 0 {
						return true
					}
				}
			case *dst.ValueSpec:
				for _, val := range s.Values {
					if cl, ok := val.(*dst.CompositeLit); ok && len(cl.Elts) > 0 {
						return true
					}
					if f.hasNewlineSpace(val.Decorations().Before) || f.hasNewlineSpace(val.Decorations().After) {
						return true
					}
				}
			}
			if f.hasNewlineSpace(spec.Decorations().Before) || f.hasNewlineSpace(spec.Decorations().After) {
				return true
			}
		}
	}
	
	if f.hasNewlineSpace(decl.Decorations().Before) || f.hasNewlineSpace(decl.Decorations().After) {
		return true
	}
	
	return false
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
			return
		}
		f.shortenStructDef(ts, t)
		f.alignStructTags(t)
	case *dst.InterfaceType:
		if t.Methods == nil || len(t.Methods.List) == 0 {
			// Empty interface - already fine
		}
	}
}

// alignStructTags aligns struct tags by adding padding before each tag.
func (f *Formatter) alignStructTags(structType *dst.StructType) {
	if structType.Fields == nil || len(structType.Fields.List) == 0 {
		return
	}

	// Only align if there are at least 2 fields with tags
	fieldsWithTags := 0
	for _, field := range structType.Fields.List {
		if field.Tag != nil {
			fieldsWithTags++
		}
	}
	if fieldsWithTags < 2 {
		return
	}

	// Calculate the max width of field name + type (before the tag)
	maxWidth := 0
	for _, field := range structType.Fields.List {
		if field.Tag == nil {
			continue
		}
		width := f.estimateFieldWidth(field)
		if width > maxWidth {
			maxWidth = width
		}
	}

	// Add padding before each tag to align
	for _, field := range structType.Fields.List {
		if field.Tag == nil {
			continue
		}
		width := f.estimateFieldWidth(field)
		padding := maxWidth - width
		if padding > 0 {
			// Add space before the tag by prepending to Start decorations
			field.Tag.Decs.Start = append(dst.Decorations{strings.Repeat(" ", padding+1)}, field.Tag.Decs.Start...)
		}
	}
}

// estimateFieldWidth estimates the visual width of a field (names + type).
// Includes the space between field name and type.
func (f *Formatter) estimateFieldWidth(field *dst.Field) int {
	width := 0
	for j, name := range field.Names {
		width += len(name.Name)
		if j < len(field.Names)-1 {
			width += 2 // ", "
		}
	}
	// Add 1 for the space between name and type
	if len(field.Names) > 0 {
		width += 1
	}
	width += f.estimateNodeWidth(field.Type)
	return width
}

// shortenStructDef splits long struct type definitions across multiple lines.
func (f *Formatter) shortenStructDef(ts *dst.TypeSpec, structType *dst.StructType) {
	if structType.Fields == nil || len(structType.Fields.List) == 0 {
		return
	}

	// Estimate the width of the struct definition on a single line
	// "type " + name + " struct { " + fields + " }"
	width := 5 // "type "
	if ts.Name != nil {
		width += len(ts.Name.Name)
	}
	width += 10 // " struct { "
	width += f.estimateStructFieldsWidth(structType)
	width += 3 // " }"

	// Add indentation
	width += f.indent * f.opts.TabWidth

	if width > f.opts.LineLength {
		// Split each field onto its own line
		for i, field := range structType.Fields.List {
			if i == 0 {
				field.Decorations().Before = dst.NewLine
			}
			field.Decorations().After = dst.NewLine
		}
	}
}

// estimateStructFieldsWidth estimates the width of struct fields.
func (f *Formatter) estimateStructFieldsWidth(structType *dst.StructType) int {
	width := 0
	for i, field := range structType.Fields.List {
		// Field names
		for j, name := range field.Names {
			width += len(name.Name)
			if j < len(field.Names)-1 {
				width += 2 // ", "
			}
		}
		// Type
		width += f.estimateNodeWidth(field.Type)
		// Tag
		if field.Tag != nil {
			width += len(field.Tag.Value)
		}
		if i < len(structType.Fields.List)-1 {
			width += 2 // "; "
		}
	}
	return width
}

// shortenAnonymousStruct splits long anonymous struct types across multiple lines.
func (f *Formatter) shortenAnonymousStruct(structType *dst.StructType) {
	if structType.Fields == nil || len(structType.Fields.List) == 0 {
		return
	}

	// Estimate width: "struct { " + fields + " }"
	width := 10 // "struct { "
	width += f.estimateStructFieldsWidth(structType)
	width += 3 // " }"
	width += f.indent * f.opts.TabWidth

	if width > f.opts.LineLength {
		for i, field := range structType.Fields.List {
			if i == 0 {
				field.Decorations().Before = dst.NewLine
			}
			field.Decorations().After = dst.NewLine
		}
	}
}

// enforceCommentWhitespace ensures comments have a space after // markers.
// Skips go directives (//go:, //export, //nolint, //line, // +build, //build).
func (f *Formatter) enforceCommentWhitespace(file *dst.File) {
	dst.Inspect(file, func(n dst.Node) bool {
		if n == nil {
			return true
		}
		decs := n.Decorations()
		f.fixDecsComments(decs.Start)
		f.fixDecsComments(decs.End)
		return true
	})
}

// commentDirectives are prefixes that should not have a space inserted.
var commentDirectives = []string{
	"//go:",
	"//export ",
	"//nolint",
	"//line ",
	"// +build",
	"//build ",
}

func (f *Formatter) fixDecsComments(decs dst.Decorations) {
	for i, d := range decs {
		if !strings.HasPrefix(d, "//") {
			continue
		}
		// Skip directives
		isDirective := false
		for _, prefix := range commentDirectives {
			if strings.HasPrefix(d, prefix) {
				isDirective = true
				break
			}
		}
		if isDirective {
			continue
		}
		// Fix missing space: //comment → // comment
		if len(d) > 2 && d[2] != ' ' && d[2] != '\t' {
			decs[i] = "// " + d[2:]
		}
	}
}

// groupFuncParams groups adjacent function parameters that share the same type.
// e.g., func(a int, b int) → func(a, b int)
func (f *Formatter) groupFuncParams(fn *dst.FuncDecl) {
	if fn.Type == nil || fn.Type.Params == nil || len(fn.Type.Params.List) < 2 {
		return
	}

	fields := fn.Type.Params.List
	var grouped []*dst.Field

	i := 0
	for i < len(fields) {
		current := fields[i]

		// Look ahead for consecutive fields with the same type and no names in current
		if len(current.Names) == 0 {
			grouped = append(grouped, current)
			i++
			continue
		}

		// Collect consecutive fields with the same type
		names := make([]*dst.Ident, len(current.Names))
		copy(names, current.Names)

		j := i + 1
		for j < len(fields) {
			next := fields[j]
			if len(next.Names) == 0 {
				break
			}
			if !f.sameType(current.Type, next.Type) {
				break
			}
			names = append(names, next.Names...)
			j++
		}

		if j > i+1 {
			// We grouped some fields - create fresh field to avoid DST decoration bleed
			newField := &dst.Field{
				Names: names,
				Type:  current.Type,
			}
			if current.Tag != nil {
				newField.Tag = current.Tag
			}
			newField.Decs.Before = dst.None
			newField.Decs.After = dst.None
			grouped = append(grouped, newField)
			i = j
		} else {
			grouped = append(grouped, current)
			i++
		}
	}

	if len(grouped) < len(fields) {
		fn.Type.Params.List = grouped
	}
}

// sameType checks if two type expressions are structurally equivalent.
func (f *Formatter) sameType(a, b dst.Expr) bool {
	switch ta := a.(type) {
	case *dst.Ident:
		if tb, ok := b.(*dst.Ident); ok {
			return ta.Name == tb.Name
		}
	case *dst.SelectorExpr:
		if tb, ok := b.(*dst.SelectorExpr); ok {
			return f.sameType(ta.X, tb.X) && ta.Sel.Name == tb.Sel.Name
		}
	case *dst.StarExpr:
		if tb, ok := b.(*dst.StarExpr); ok {
			return f.sameType(ta.X, tb.X)
		}
	case *dst.ArrayType:
		if tb, ok := b.(*dst.ArrayType); ok {
			return f.sameType(ta.Elt, tb.Elt)
		}
	case *dst.MapType:
		if tb, ok := b.(*dst.MapType); ok {
			return f.sameType(ta.Key, tb.Key) && f.sameType(ta.Value, tb.Value)
		}
	case *dst.Ellipsis:
		if tb, ok := b.(*dst.Ellipsis); ok {
			return f.sameType(ta.Elt, tb.Elt)
		}
	}
	return false
}

