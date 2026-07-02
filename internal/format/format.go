// Package format provides unified Go formatting.
// It combines: import fixing, line shortening, and gofumpt-style rules.
package format

import (
	"bytes"
	"fmt"
	"go/token"
	"strings"

	"github.com/dave/dst"
	"github.com/dave/dst/decorator"
)

// Options configures the formatter.
type Options struct {
	LineLength    int      // target maximum line length (default: 100)
	TabWidth      int      // tab display width (default: 4)
	ModulePath    string   // module path for import grouping
	LocalPrefixes []string // local import prefixes for grouping
	Fast          bool     // skip sibling scan for import resolution
	Resolve       bool     // load full type info for third-party import resolution
}

// DefaultOptions returns sensible defaults.
func DefaultOptions() Options {
	return Options{
		LineLength: 100,
		TabWidth:   4,
	}
}

// Formatter performs unified Go formatting.
type Formatter struct {
	fset         *token.FileSet
	opts         Options
	imports      map[string]*dst.ImportSpec    // path -> spec
	packageRefs  map[string][]string           // pkgName -> symbols (collected during walk)
	siblingCache map[string]map[string]bool    // directory -> globals
	widthCache   map[dst.Node]int              // node -> estimated width (reset per file)
	indent       int                           // current indentation level
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
		widthCache:   make(map[dst.Node]int),
	}
}

// Format formats source. Returns formatted output or an error if the source
// is not valid Go.
func (f *Formatter) Format(filename string, src []byte) ([]byte, error) {
	// Fast path for truly empty input (no bytes at all or only spaces/tabs/newlines)
	// Don't use bytes.TrimSpace because it catches form feeds etc which are
	// illegal in Go source — we want those to hit the parser and get rejected.
	if len(src) == 0 {
		return src, nil
	}
	allWS := true
	for _, b := range src {
		if b != ' ' && b != '\t' && b != '\n' && b != '\r' {
			allWS = false
			break
		}
	}
	if allWS {
		return src, nil
	}

	file, err := safeParse(src)
	if err != nil {
		return nil, err
	}

	f.transform(filename, file)

	var buf bytes.Buffer
	if err := decorator.Fprint(&buf, file); err != nil {
		return nil, fmt.Errorf("print: %w", err)
	}

	return buf.Bytes(), nil
}

// safeParse wraps decorator.Parse with panic recovery for malformed input.
func safeParse(src []byte) (file *dst.File, err error) {
	defer func() {
		if r := recover(); r != nil {
			file = nil
			err = fmt.Errorf("parse: invalid Go source")
		}
	}()
	return decorator.Parse(src)
}

// transform applies all formatting transforms.
func (f *Formatter) transform(filename string, file *dst.File) {
	f.imports = make(map[string]*dst.ImportSpec)
	f.packageRefs = make(map[string][]string)
	clear(f.widthCache)

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

	f.indent = 0
	f.transformDecls(file.Decls)

	// Apply gofumpt rules (normalization + multiline decl separation)
	f.applyGofumptRules(file)

	// Collect refs + apply post-rules in one walk (before fixImports which needs refs)
	f.collectRefsAndPostProcess(file)

	f.fixImports(filename, file)
	f.collapseFuncBodies(file)
}

// transformDecls processes declarations with indentation context.
func (f *Formatter) transformDecls(decls []dst.Decl) {
	for _, decl := range decls {
		f.transformDecl(decl)
	}
}

// transformDecl processes a single declaration.
func (f *Formatter) transformDecl(decl dst.Decl) {
	switch d := decl.(type) {
	case *dst.FuncDecl:
		f.shortenFuncParams(d)
		if d.Body != nil {
			f.indent = 1
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

// transformBlockStmt processes a block statement body.
func (f *Formatter) transformBlockStmt(block *dst.BlockStmt) {
	f.indent++
	for i, stmt := range block.List {
		if simplified := f.simplifyVarDecl(stmt); simplified != nil {
			block.List[i] = simplified
			continue
		}
		f.transformStmt(stmt)
	}
	f.indent--
}

// simplifyVarDecl converts var x = value to x := value inside functions.
func (f *Formatter) simplifyVarDecl(stmt dst.Stmt) dst.Stmt {
	declStmt, ok := stmt.(*dst.DeclStmt)
	if !ok {
		return nil
	}
	genDecl, ok := declStmt.Decl.(*dst.GenDecl)
	if !ok || genDecl.Tok != token.VAR || len(genDecl.Specs) != 1 {
		return nil
	}
	valueSpec, ok := genDecl.Specs[0].(*dst.ValueSpec)
	if !ok || len(valueSpec.Names) != 1 || len(valueSpec.Values) != 1 || valueSpec.Type != nil {
		return nil
	}

	assign := &dst.AssignStmt{
		Lhs: []dst.Expr{valueSpec.Names[0]},
		Tok: token.DEFINE,
		Rhs: []dst.Expr{valueSpec.Values[0]},
	}
	assign.Decs.Before = declStmt.Decs.Before
	assign.Decs.After = dst.NewLine

	return assign
}

// transformStmt processes a statement.
func (f *Formatter) transformStmt(stmt dst.Stmt) {
	switch s := stmt.(type) {
	case *dst.AssignStmt:
		for i, expr := range s.Rhs {
			if f.isMethodChain(expr) {
				s.Rhs[i] = f.shortenMethodChain(expr)
			} else if split := f.splitLongString(expr, s.Lhs, s.Tok); split != nil {
				s.Rhs[i] = split
			} else {
				f.transformExpr(expr)
			}
		}
	case *dst.BlockStmt:
		f.transformBlockStmt(s)
	case *dst.CaseClause:
		f.shortenCaseClause(s)
		for _, st := range s.Body {
			f.transformStmt(st)
		}
	case *dst.CommClause:
		for _, st := range s.Body {
			f.transformStmt(st)
		}
	case *dst.DeclStmt:
		f.transformDecl(s.Decl)
	case *dst.ExprStmt:
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
					for _, st := range clause.Body {
						f.transformStmt(st)
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
					for _, st := range clause.Body {
						f.transformStmt(st)
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
					for _, st := range clause.Body {
						f.transformStmt(st)
					}
				}
			}
			f.indent--
		}
	}
}

// transformExpr processes an expression.
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
		if st, ok := e.Type.(*dst.StructType); ok {
			f.shortenAnonymousStruct(st)
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
	}
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

// transformSpec processes a spec.
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

// transformBasicLit transforms basic literals (octal, etc.).
func (f *Formatter) transformBasicLit(lit *dst.BasicLit) {
	if lit.Kind != token.INT {
		return
	}
	value := lit.Value
	if len(value) > 1 && value[0] == '0' {
		if len(value) > 2 && (value[1] == 'o' || value[1] == 'O' ||
			value[1] == 'x' || value[1] == 'X' ||
			value[1] == 'b' || value[1] == 'B') {
			return
		}
		isOctal := true
		for i := 1; i < len(value); i++ {
			if c := value[i]; c < '0' || c > '7' {
				isOctal = false
				break
			}
		}
		if isOctal {
			lit.Value = "0o" + value[1:]
		}
	}
}

// estimateNodeWidth estimates formatted width of a node.
// Uses a cache to avoid O(N²) repeated subtree walks.
func (f *Formatter) estimateNodeWidth(node dst.Node) int {
	if node == nil {
		return 0
	}
	if w, ok := f.widthCache[node]; ok {
		return w
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
			width += 3
		case *dst.CallExpr:
			width += 2
			if len(v.Args) > 1 {
				width += (len(v.Args) - 1) * 2
			}
		case *dst.CompositeLit:
			width += 2
			if len(v.Elts) > 1 {
				width += (len(v.Elts) - 1) * 2
			}
		case *dst.KeyValueExpr:
			width += 2
		case *dst.SelectorExpr:
			width += 1
		}
		return true
	})
	f.widthCache[node] = width
	return width
}

// collectRefsAndPostProcess collects package references (for import resolution)
// and applies post-processing rules (comments + interface{} → any) in a single walk.
func (f *Formatter) collectRefsAndPostProcess(file *dst.File) {
	dst.Inspect(file, func(n dst.Node) bool {
		if n == nil {
			return true
		}

		// Collect package references: pkg.Name patterns
		if sel, ok := n.(*dst.SelectorExpr); ok {
			if ident, ok := sel.X.(*dst.Ident); ok {
				f.packageRefs[ident.Name] = append(f.packageRefs[ident.Name], sel.Sel.Name)
			}
		}

		// Comment whitespace enforcement
		decs := n.Decorations()
		f.fixDecsComments(&decs.Start)
		f.fixDecsComments(&decs.End)

		// interface{} → any conversion
		switch node := n.(type) {
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
		case *dst.StructType:
			if node.Fields != nil {
				for _, field := range node.Fields.List {
					field.Type = f.toAnyIfEmptyInterface(field.Type)
				}
			}
		case *dst.ValueSpec:
			if node.Type != nil {
				node.Type = f.toAnyIfEmptyInterface(node.Type)
			}
		case *dst.TypeSpec:
			node.Type = f.toAnyIfEmptyInterface(node.Type)
		case *dst.TypeAssertExpr:
			if node.Type != nil {
				node.Type = f.toAnyIfEmptyInterface(node.Type)
			}
		case *dst.MapType:
			node.Value = f.toAnyIfEmptyInterface(node.Value)
		case *dst.ArrayType:
			node.Elt = f.toAnyIfEmptyInterface(node.Elt)
		case *dst.ChanType:
			node.Value = f.toAnyIfEmptyInterface(node.Value)
		}
		return true
	})
}
