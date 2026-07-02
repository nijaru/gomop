package format

import "github.com/dave/dst"

// =============================================================================
// Function Body Collapsing
// =============================================================================

// collapseFuncBodies collapses single-statement function bodies onto one line
// when they fit within the line length limit.
func (f *Formatter) collapseFuncBodies(file *dst.File) {
	for _, decl := range file.Decls {
		if fn, ok := decl.(*dst.FuncDecl); ok {
			f.collapseSingleFunc(fn)
		}
	}
}

// collapseSingleFunc checks if a function body can be collapsed to a single
// line and applies the collapse if it fits.
func (f *Formatter) collapseSingleFunc(fn *dst.FuncDecl) {
	if fn.Body == nil || len(fn.Body.List) != 1 {
		return
	}

	switch fn.Body.List[0].(type) {
	case *dst.IfStmt, *dst.ForStmt, *dst.RangeStmt,
		*dst.SwitchStmt, *dst.TypeSwitchStmt, *dst.SelectStmt:
		return
	}

	if len(fn.Body.List[0].Decorations().Start) > 0 ||
		len(fn.Decorations().End) > 0 {
		return
	}

	width := 5 // "func "

	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		width += 2
		for _, field := range fn.Recv.List {
			width += f.estimateNodeWidth(field)
		}
		width += 2
	}
	if fn.Name != nil {
		width += len(fn.Name.Name)
	}
	if fn.Type != nil {
		if fn.Type.TypeParams != nil && len(fn.Type.TypeParams.List) > 0 {
			width += 2
			for i, field := range fn.Type.TypeParams.List {
				width += f.estimateNodeWidth(field)
				if i < len(fn.Type.TypeParams.List)-1 {
					width += 2
				}
			}
			width += 1
		}
		if fn.Type.Params != nil {
			width += 2
			for i, field := range fn.Type.Params.List {
				width += f.estimateNodeWidth(field)
				if i < len(fn.Type.Params.List)-1 {
					width += 2
				}
			}
		}
		if fn.Type.Results != nil && len(fn.Type.Results.List) > 0 {
			width += 1
			for i, field := range fn.Type.Results.List {
				width += f.estimateNodeWidth(field)
				if i < len(fn.Type.Results.List)-1 {
					width += 2
				}
			}
		}
	}

	width += 3
	width += f.estimateNodeWidth(fn.Body.List[0])
	width += 2

	if width > f.opts.LineLength {
		return
	}

	stmt := dst.Clone(fn.Body.List[0]).(dst.Stmt)
	stmt.Decorations().Before = dst.None
	stmt.Decorations().After = dst.None
	fn.Body.List[0] = stmt
}
