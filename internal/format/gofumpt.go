package format

import (
	"go/token"
	"strings"

	"github.com/dave/dst"
)

// =============================================================================
// Gofumpt-style Rules
// =============================================================================

func (f *Formatter) applyGofumptRules(file *dst.File) {
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *dst.FuncDecl:
			f.normalizeFuncDecl(d)
		case *dst.GenDecl:
			f.normalizeGenDecl(d)
		}
	}
	f.separateMultilineDecls(file.Decls)
}

func (f *Formatter) normalizeFuncDecl(fn *dst.FuncDecl) {
	if fn.Body == nil {
		return
	}
	if len(fn.Body.List) > 0 {
		if fn.Body.List[0].Decorations().Before == dst.EmptyLine {
			fn.Body.List[0].Decorations().Before = dst.NewLine
		}
		last := fn.Body.List[len(fn.Body.List)-1]
		if last.Decorations().After == dst.EmptyLine {
			last.Decorations().After = dst.NewLine
		}
	}
	f.groupFuncParams(fn)
}

func (f *Formatter) separateMultilineDecls(decls []dst.Decl) {
	if len(decls) < 2 {
		return
	}
	for i := 1; i < len(decls); i++ {
		prev := decls[i-1]
		curr := decls[i]
		if (f.isMultilineDecl(prev) || f.isMultilineDecl(curr)) &&
			!f.isImportDecl(prev) && !f.isImportDecl(curr) {
			curr.Decorations().Before = dst.EmptyLine
		}
	}
}

func (f *Formatter) isImportDecl(decl dst.Decl) bool {
	gen, ok := decl.(*dst.GenDecl)
	return ok && gen.Tok == token.IMPORT
}

func (f *Formatter) hasNewlineSpace(space dst.SpaceType) bool {
	return space == dst.NewLine || space == dst.EmptyLine
}

func (f *Formatter) isMultilineDecl(decl dst.Decl) bool {
	switch d := decl.(type) {
	case *dst.FuncDecl:
		if d.Body != nil {
			if len(d.Body.List) > 1 {
				return true
			}
			if len(d.Body.List) == 1 {
				stmt := d.Body.List[0]
				if f.hasNewlineSpace(stmt.Decorations().Before) ||
					f.hasNewlineSpace(stmt.Decorations().After) {
					return true
				}
			}
		}
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
					if f.hasNewlineSpace(val.Decorations().Before) ||
						f.hasNewlineSpace(val.Decorations().After) {
						return true
					}
				}
			}
			if f.hasNewlineSpace(spec.Decorations().Before) ||
				f.hasNewlineSpace(spec.Decorations().After) {
				return true
			}
		}
	}
	return f.hasNewlineSpace(decl.Decorations().Before) ||
		f.hasNewlineSpace(decl.Decorations().After)
}

func (f *Formatter) normalizeGenDecl(gen *dst.GenDecl) {
	switch gen.Tok {
	case token.TYPE:
		for _, spec := range gen.Specs {
			if ts, ok := spec.(*dst.TypeSpec); ok {
				f.normalizeTypeSpec(ts)
			}
		}
	}
}

func (f *Formatter) normalizeTypeSpec(ts *dst.TypeSpec) {
	switch t := ts.Type.(type) {
	case *dst.StructType:
		if t.Fields == nil || len(t.Fields.List) == 0 {
			return
		}
		f.shortenStructDef(ts, t)
		f.alignStructTags(t)
	}
}

func (f *Formatter) alignStructTags(structType *dst.StructType) {
	if structType.Fields == nil || len(structType.Fields.List) == 0 {
		return
	}
	fieldsWithTags := 0
	for _, field := range structType.Fields.List {
		if field.Tag != nil {
			fieldsWithTags++
		}
	}
	if fieldsWithTags < 2 {
		return
	}
	maxWidth := 0
	for _, field := range structType.Fields.List {
		if field.Tag == nil {
			continue
		}
		if w := f.estimateFieldWidth(field); w > maxWidth {
			maxWidth = w
		}
	}
	for _, field := range structType.Fields.List {
		if field.Tag == nil {
			continue
		}
		padding := maxWidth - f.estimateFieldWidth(field)
		if padding > 0 {
			field.Tag.Decs.Start = append(
				dst.Decorations{strings.Repeat(" ", padding+1)},
				field.Tag.Decs.Start...,
			)
		}
	}
}

func (f *Formatter) estimateFieldWidth(field *dst.Field) int {
	width := 0
	for j, name := range field.Names {
		width += len(name.Name)
		if j < len(field.Names)-1 {
			width += 2
		}
	}
	if len(field.Names) > 0 {
		width += 1
	}
	return width + f.estimateNodeWidth(field.Type)
}

func (f *Formatter) shortenStructDef(ts *dst.TypeSpec, structType *dst.StructType) {
	if structType.Fields == nil || len(structType.Fields.List) == 0 {
		return
	}
	width := 5
	if ts.Name != nil {
		width += len(ts.Name.Name)
	}
	width += 10
	width += f.estimateStructFieldsWidth(structType)
	width += 3
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

func (f *Formatter) estimateStructFieldsWidth(structType *dst.StructType) int {
	width := 0
	for i, field := range structType.Fields.List {
		for j, name := range field.Names {
			width += len(name.Name)
			if j < len(field.Names)-1 {
				width += 2
			}
		}
		width += f.estimateNodeWidth(field.Type)
		if field.Tag != nil {
			width += len(field.Tag.Value)
		}
		if i < len(structType.Fields.List)-1 {
			width += 2
		}
	}
	return width
}

func (f *Formatter) shortenAnonymousStruct(structType *dst.StructType) {
	if structType.Fields == nil || len(structType.Fields.List) == 0 {
		return
	}
	width := 10
	width += f.estimateStructFieldsWidth(structType)
	width += 3
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

var commentDirectives = []string{
	"//go:",
	"//export ",
	"//nolint",
	"//line ",
	"// +build",
	"//build ",
}

func (f *Formatter) fixDecsComments(decs *dst.Decorations) {
	n := 0
	for _, d := range *decs {
		if strings.HasPrefix(d, "//") && strings.TrimSpace(d[2:]) == "" {
			continue
		}
		(*decs)[n] = d
		n++
	}
	*decs = (*decs)[:n]

	for i := range *decs {
		d := (*decs)[i]
		if !strings.HasPrefix(d, "//") {
			continue
		}
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
		if len(d) > 2 && d[2] != ' ' && d[2] != '\t' {
			(*decs)[i] = "// " + d[2:]
		}
	}
}

func (f *Formatter) groupFuncParams(fn *dst.FuncDecl) {
	if fn.Type == nil || fn.Type.Params == nil || len(fn.Type.Params.List) < 2 {
		return
	}

	fields := fn.Type.Params.List
	var grouped []*dst.Field

	i := 0
	for i < len(fields) {
		current := fields[i]
		if len(current.Names) == 0 {
			grouped = append(grouped, current)
			i++
			continue
		}

		names := make([]*dst.Ident, len(current.Names))
		copy(names, current.Names)

		j := i + 1
		for j < len(fields) {
			next := fields[j]
			if len(next.Names) == 0 || !f.sameType(current.Type, next.Type) {
				break
			}
			names = append(names, next.Names...)
			j++
		}

		if j > i+1 {
			nf := &dst.Field{
				Names: names,
				Type:  current.Type,
			}
			if current.Tag != nil {
				nf.Tag = current.Tag
			}
			nf.Decs.Before = dst.None
			nf.Decs.After = dst.None
			grouped = append(grouped, nf)
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

func (f *Formatter) sameType(a, b dst.Expr) bool {
	switch ta := a.(type) {
	case *dst.Ident:
		tb, ok := b.(*dst.Ident)
		return ok && ta.Name == tb.Name
	case *dst.SelectorExpr:
		tb, ok := b.(*dst.SelectorExpr)
		return ok && f.sameType(ta.X, tb.X) && ta.Sel.Name == tb.Sel.Name
	case *dst.StarExpr:
		tb, ok := b.(*dst.StarExpr)
		return ok && f.sameType(ta.X, tb.X)
	case *dst.ArrayType:
		tb, ok := b.(*dst.ArrayType)
		return ok && f.sameType(ta.Elt, tb.Elt)
	case *dst.MapType:
		tb, ok := b.(*dst.MapType)
		return ok && f.sameType(ta.Key, tb.Key) && f.sameType(ta.Value, tb.Value)
	case *dst.Ellipsis:
		tb, ok := b.(*dst.Ellipsis)
		return ok && f.sameType(ta.Elt, tb.Elt)
	}
	return false
}
