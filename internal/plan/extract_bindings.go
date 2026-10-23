package plan

import (
	"github.com/dop251/goja/ast"
)

func (ex *extractor) prebindLexicalDeclarations(statements []ast.Statement, sc *scope) {
	for _, statement := range statements {
		switch declaration := statement.(type) {
		case *ast.LexicalDeclaration:
			for _, binding := range declaration.List {
				if binding != nil {
					ex.bindTarget(binding.Target, sc)
				}
			}
		case *ast.FunctionDeclaration:
			if declaration.Function != nil && declaration.Function.Name != nil {
				sc.declare(declaration.Function.Name.Name.String())
			}
		case *ast.ClassDeclaration:
			if declaration.Class != nil && declaration.Class.Name != nil {
				sc.declare(declaration.Class.Name.Name.String())
			}
		}
	}
}

// visitParameterInitializers walks expressions that execute while parameters
// are bound. They are not part of a function body in goja's AST, but they are
// lexically inside the function and therefore inherit a pipeline callback's
// stage context.
func (ex *extractor) visitParameterInitializers(params *ast.ParameterList, ctx walkCtx, sc *scope) {
	if params == nil {
		return
	}
	for _, binding := range params.List {
		if binding == nil {
			continue
		}
		ex.visitExpr(binding.Initializer, ctx, sc)
		ex.visitBindingInitializers(binding.Target, ctx, sc)
	}
}

func (ex *extractor) visitBindingInitializers(target ast.BindingTarget, ctx walkCtx, sc *scope) {
	switch pattern := target.(type) {
	case *ast.ArrayPattern:
		for _, element := range pattern.Elements {
			ex.visitBindingElementInitializers(element, ctx, sc)
		}
	case *ast.ObjectPattern:
		for _, property := range pattern.Properties {
			switch property := property.(type) {
			case *ast.PropertyShort:
				ex.visitExpr(property.Initializer, ctx, sc)
			case *ast.PropertyKeyed:
				if property.Computed {
					ex.visitExpr(property.Key, ctx, sc)
				}
				ex.visitBindingElementInitializers(property.Value, ctx, sc)
			}
		}
	}
}

func (ex *extractor) visitBindingElementInitializers(element ast.Expression, ctx walkCtx, sc *scope) {
	switch element := element.(type) {
	case *ast.AssignExpression:
		ex.visitExpr(element.Right, ctx, sc)
		if target, ok := element.Left.(ast.BindingTarget); ok {
			ex.visitBindingInitializers(target, ctx, sc)
		}
	case ast.BindingTarget:
		ex.visitBindingInitializers(element, ctx, sc)
	}
}

func (ex *extractor) bindVarDecl(d *ast.VariableDeclaration, sc *scope) {
	if d == nil {
		return
	}
	for _, b := range d.List {
		ex.bindTarget(b.Target, sc)
	}
}

func (ex *extractor) bindTarget(t ast.BindingTarget, sc *scope) {
	if t == nil {
		return
	}
	switch b := t.(type) {
	case *ast.Identifier:
		sc.declare(b.Name.String())
	case *ast.ArrayPattern:
		for _, el := range b.Elements {
			ex.bindTargetIdent(el, sc)
		}
		ex.bindTargetIdent(b.Rest, sc)
	case *ast.ObjectPattern:
		for _, p := range b.Properties {
			switch pr := p.(type) {
			case *ast.PropertyShort:
				sc.declare(pr.Name.Name.String())
			case *ast.PropertyKeyed:
				ex.bindTargetIdent(pr.Value, sc)
			}
		}
		ex.bindTargetIdent(b.Rest, sc)
	}
}

func (ex *extractor) bindTargetIdent(e ast.Expression, sc *scope) {
	if e == nil {
		return
	}
	if assignment, ok := e.(*ast.AssignExpression); ok {
		ex.bindTargetIdent(assignment.Left, sc)
		return
	}
	if id, ok := e.(*ast.Identifier); ok {
		sc.declare(id.Name.String())
		return
	}
	if b, ok := e.(ast.BindingTarget); ok {
		ex.bindTarget(b, sc)
	}
}
