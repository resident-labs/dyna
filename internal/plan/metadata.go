package plan

import "github.com/dop251/goja/ast"

// Metadata holds literal workflow declarations. Reading it never runs source.
type Metadata struct {
	Name        string
	Description string
	Phases      []PhaseDecl
}

// ReadMetadata reads the top-level meta declaration with the workflow parser.
func ReadMetadata(src string) (Metadata, error) {
	ex := &extractor{}
	prog, err := ex.parse(src)
	if err != nil {
		return Metadata{}, err
	}
	return metadataFromProgram(prog, ex.wrapPrefix > 0), nil
}

func metadataFromProgram(prog *ast.Program, wrapped bool) Metadata {
	body := prog.Body
	if wrapped {
		stmt := body[0].(*ast.ExpressionStatement)
		call := stmt.Expression.(*ast.CallExpression)
		fn := call.Callee.(*ast.ArrowFunctionLiteral)
		body = fn.Body.(*ast.BlockStatement).List
	}
	for _, stmt := range body {
		if obj := metaObject(stmt); obj != nil {
			var m Metadata
			m.Name, _ = staticString(objectProp(obj, "name"))
			m.Description, _ = staticString(objectProp(obj, "description"))
			if arr, ok := objectProp(obj, "phases").(*ast.ArrayLiteral); ok {
				for _, el := range arr.Value {
					if phase, ok := el.(*ast.ObjectLiteral); ok {
						if title, ok := staticString(objectProp(phase, "title")); ok && title != "" {
							m.Phases = append(m.Phases, PhaseDecl{Title: title})
						}
					}
				}
			}
			return m
		}
	}
	return Metadata{}
}

func metaObject(s ast.Statement) *ast.ObjectLiteral {
	switch st := s.(type) {
	case *ast.LexicalDeclaration:
		for _, b := range st.List {
			if id, ok := b.Target.(*ast.Identifier); ok && id.Name.String() == "meta" {
				if o, ok := b.Initializer.(*ast.ObjectLiteral); ok {
					return o
				}
			}
		}
	case *ast.VariableStatement:
		for _, b := range st.List {
			if id, ok := b.Target.(*ast.Identifier); ok && id.Name.String() == "meta" {
				if o, ok := b.Initializer.(*ast.ObjectLiteral); ok {
					return o
				}
			}
		}
	}
	return nil
}
