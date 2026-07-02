package format

import (
	"go/token"
	"strings"

	"github.com/dave/dst"
)

// =============================================================================
// Line Shortening (golines-style)
// =============================================================================

// shortenFuncParams splits long function parameter lists.
func (f *Formatter) shortenFuncParams(fn *dst.FuncDecl) {
	if fn.Type == nil || fn.Type.Params == nil || len(fn.Type.Params.List) <= 1 {
		return
	}

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
		for _, field := range fn.Type.Params.List {
			field.Decorations().Before = dst.NewLine
			field.Decorations().After = dst.NewLine
		}
	}
}

// shortenCallExpr breaks long function calls across multiple lines.
func (f *Formatter) shortenCallExpr(call *dst.CallExpr) {
	if len(call.Args) <= 1 {
		return
	}

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
	selectors := f.collectMethodChainSelectors(expr)
	if len(selectors) <= 1 {
		return expr
	}

	width := f.estimateNodeWidth(expr) + (f.indent * f.opts.TabWidth)
	if width <= f.opts.LineLength {
		return expr
	}

	for _, sel := range selectors {
		sel.Decs.X.Prepend("\n")
	}
	return expr
}

func (f *Formatter) isMethodChain(expr dst.Expr) bool {
	return len(f.collectMethodChainSelectors(expr)) >= 2
}

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

// shortenBinaryExpr splits long boolean expressions at && / ||.
func (f *Formatter) shortenBinaryExpr(expr *dst.BinaryExpr) {
	if expr.Op != token.LAND && expr.Op != token.LOR {
		return
	}
	width := f.estimateNodeWidth(expr) + (f.indent * f.opts.TabWidth)
	if width <= f.opts.LineLength {
		return
	}
	f.splitLogicalChain(expr, expr.Op)
}

func (f *Formatter) splitLogicalChain(expr dst.Expr, op token.Token) {
	bin, ok := expr.(*dst.BinaryExpr)
	if !ok || bin.Op != op {
		return
	}
	f.splitLogicalChain(bin.X, op)
	bin.Y.Decorations().Before = dst.NewLine
}

// shortenCaseClause collapses short case clauses onto a single line.
func (f *Formatter) shortenCaseClause(clause *dst.CaseClause) {
	if len(clause.List) == 0 {
		return
	}
	width := 6 // "case "
	for i, expr := range clause.List {
		width += f.estimateNodeWidth(expr)
		if i < len(clause.List)-1 {
			width += 2
		}
	}
	width += 1
	width += f.indent * f.opts.TabWidth

	if width < f.opts.LineLength-20 {
		for _, expr := range clause.List {
			expr.Decorations().Before = dst.None
			expr.Decorations().After = dst.None
		}
	}
}

// splitLongString splits a long string literal into concatenated parts.
func (f *Formatter) splitLongString(expr dst.Expr, lhs []dst.Expr, tok token.Token) dst.Expr {
	lit, ok := expr.(*dst.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return nil
	}

	prefix := f.indent * f.opts.TabWidth
	if len(lhs) > 0 {
		for _, l := range lhs {
			prefix += f.estimateNodeWidth(l)
		}
		if tok == token.DEFINE {
			prefix += 2
		} else {
			prefix += 1
		}
		prefix += 1
	}

	totalWidth := prefix + len(lit.Value)
	if totalWidth <= f.opts.LineLength {
		return nil
	}

	raw := lit.Value
	isRaw := strings.HasPrefix(raw, "`")
	var content string
	if isRaw {
		content = strings.Trim(raw, "`")
	} else {
		content = strings.Trim(raw, `"`)
		content = strings.ReplaceAll(content, `\n`, "\n")
		content = strings.ReplaceAll(content, `\t`, "\t")
	}
	if isRaw || strings.Contains(content, "\n") {
		return nil
	}

	availableWidth := f.opts.LineLength - prefix - 2
	if availableWidth < 20 {
		return nil
	}

	chunks := f.splitStringAtWords(content, availableWidth)
	if len(chunks) <= 1 {
		return nil
	}

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
			part.Decorations().Before = dst.NewLine
		}
	}
	return result
}

func (f *Formatter) splitStringAtWords(s string, maxWidth int) []string {
	if len(s) <= maxWidth {
		return []string{s}
	}
	var chunks []string
	words := strings.Fields(s)
	if len(words) == 0 {
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
