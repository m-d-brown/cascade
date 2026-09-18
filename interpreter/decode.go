package interpreter

import (
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// validActionKey is every field an action may set.
var validActionKey = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range strings.Split(actionFields, ", ") {
		m[k] = true
	}
	return m
}()

// decodeFile parses the cascade YAML into its raw shape. It reports unknown
// fields, a mistyped `actions`, and `run` given a list with a line number and
// the file's own vocabulary, and it accepts a bare string where needs,
// produces or sources want a list.
func decodeFile(data []byte) (file, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return file{}, friendlyParseError(err)
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return file{}, errors.New("the cascade is empty")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return file{}, fmt.Errorf("line %d: the cascade must be a mapping with an `actions:` key", root.Line)
	}

	for i := 0; i+1 < len(root.Content); i += 2 {
		key, val := root.Content[i], root.Content[i+1]
		switch key.Value {
		case "name":
		case "actions":
			if err := normalizeActions(val); err != nil {
				return file{}, err
			}
		default:
			return file{}, fmt.Errorf("line %d: unknown field %q at the top level (valid: name, actions)", key.Line, key.Value)
		}
	}

	var f file
	if err := doc.Decode(&f); err != nil {
		return file{}, friendlyParseError(err)
	}
	return f, nil
}

// normalizeActions validates action field names and coerces a bare scalar
// into a one-element list for the list-valued fields.
func normalizeActions(actions *yaml.Node) error {
	if actions.Kind == yaml.ScalarNode && actions.Tag == "!!null" {
		return nil // `actions:` with nothing — build() reports "no actions"
	}
	if actions.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: `actions` must be a map of name to action", actions.Line)
	}
	for i := 0; i+1 < len(actions.Content); i += 2 {
		nameNode, act := actions.Content[i], actions.Content[i+1]
		if act.Kind == yaml.ScalarNode && act.Tag == "!!null" {
			continue // `name:` with no body — a bare barrier
		}
		if act.Kind != yaml.MappingNode {
			return fmt.Errorf("line %d: action %q must be a mapping of fields", act.Line, nameNode.Value)
		}
		for j := 0; j+1 < len(act.Content); j += 2 {
			fk, fv := act.Content[j], act.Content[j+1]
			if !validActionKey[fk.Value] {
				return fmt.Errorf("line %d: unknown field %q for action %q (valid: %s)",
					fk.Line, fk.Value, nameNode.Value, actionFields)
			}
			switch fk.Value {
			case "needs", "produces", "sources", "allow-exit":
				wrapScalarInList(fv)
			case "run", "unless", "progress":
				if fv.Kind == yaml.SequenceNode {
					return fmt.Errorf("line %d: %s for action %q must be a single shell command — join steps with `&&` or `;`",
						fv.Line, fk.Value, nameNode.Value)
				}
			}
		}
	}
	return nil
}

// wrapScalarInList turns `needs: fetch` into `needs: [fetch]` in place, so the
// common shorthand is accepted.
func wrapScalarInList(n *yaml.Node) {
	if n.Kind != yaml.ScalarNode || n.Tag == "!!null" {
		return
	}
	inner := *n
	*n = yaml.Node{
		Kind:    yaml.SequenceNode,
		Tag:     "!!seq",
		Line:    inner.Line,
		Column:  inner.Column,
		Content: []*yaml.Node{&inner},
	}
}
