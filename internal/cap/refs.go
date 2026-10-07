package cap

import (
	"fmt"
	"text/template"
	"text/template/parse"
)

// SecretRefs returns every secret reference a broker-side template resolves.
// It walks the parsed template rather than executing it, and insists that
// secret is only ever called with a literal string: a reference computed from
// data (such as .VM) could resolve to one secret during review and another at
// runtime, which would let a capability file bypass what `cracklet cap add`
// showed before installation.
func SecretRefs(text string) ([]string, error) {
	tmpl, err := template.New("cap").Funcs(template.FuncMap{"secret": func(string) string { return "" }}).Parse(text)
	if err != nil {
		return nil, fmt.Errorf("parse template: %w", err)
	}
	var refs []string
	var walk func(parse.Node) error
	walk = func(n parse.Node) error {
		switch x := n.(type) {
		case *parse.ListNode:
			if x == nil {
				return nil
			}
			for _, c := range x.Nodes {
				if err := walk(c); err != nil {
					return err
				}
			}
		case *parse.ActionNode:
			return walk(x.Pipe)
		case *parse.IfNode:
			return walkBranch(walk, &x.BranchNode)
		case *parse.RangeNode:
			return walkBranch(walk, &x.BranchNode)
		case *parse.WithNode:
			return walkBranch(walk, &x.BranchNode)
		case *parse.TemplateNode:
			return walk(x.Pipe)
		case *parse.PipeNode:
			if x == nil {
				return nil
			}
			for _, cmd := range x.Cmds {
				if err := walk(cmd); err != nil {
					return err
				}
			}
		case *parse.CommandNode:
			for i, arg := range x.Args {
				if id, ok := arg.(*parse.IdentifierNode); ok && id.Ident == "secret" {
					if i != 0 || len(x.Args) != 2 {
						return fmt.Errorf("secret must be called directly as {{ secret \"ref\" }}")
					}
					lit, ok := x.Args[1].(*parse.StringNode)
					if !ok {
						return fmt.Errorf("secret must be called with a literal reference, not an expression")
					}
					refs = append(refs, lit.Text)
					return nil
				}
				if err := walk(arg); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(tmpl.Tree.Root); err != nil {
		return nil, err
	}
	return refs, nil
}

func walkBranch(walk func(parse.Node) error, b *parse.BranchNode) error {
	if err := walk(b.Pipe); err != nil {
		return err
	}
	if err := walk(b.List); err != nil {
		return err
	}
	if b.ElseList != nil {
		return walk(b.ElseList)
	}
	return nil
}
