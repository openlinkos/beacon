package configdb

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"go.yaml.in/yaml/v3"
)

// The module file is kept as it was imported, so that probes get it byte for byte, comments and all.
// A change to one module edits that module in the file's YAML tree and writes the tree out again,
// which keeps the other modules and their comments.

type namedModule struct {
	name  string
	value json.RawMessage // the module as JSON, keys sorted
}

// moduleDoc parses the module file into its document and the modules mapping in it, making both when
// the file is empty.
func moduleDoc(text []byte) (*yaml.Node, *yaml.Node, error) {
	doc := &yaml.Node{}
	if err := yaml.Unmarshal(text, doc); err != nil {
		return nil, nil, fmt.Errorf("the module file: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		doc = &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	root := doc.Content[0]
	if root.Kind == yaml.ScalarNode && root.Tag == "!!null" {
		root.Kind, root.Tag, root.Value = yaml.MappingNode, "!!map", ""
	}
	if root.Kind != yaml.MappingNode {
		return nil, nil, errors.New("the module file is not a mapping with the key modules")
	}
	var mods *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "modules" {
			mods = root.Content[i+1]
		}
	}
	if mods == nil {
		mods = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "modules"}, mods)
	}
	if mods.Kind == yaml.ScalarNode && mods.Tag == "!!null" {
		mods.Kind, mods.Tag, mods.Value = yaml.MappingNode, "!!map", ""
	}
	if mods.Kind != yaml.MappingNode {
		return nil, nil, errors.New("the module file's modules is not a mapping of names to modules")
	}
	return doc, mods, nil
}

// moduleList lists the modules of a module file in its order.
func moduleList(text []byte) ([]namedModule, error) {
	if len(bytes.TrimSpace(text)) == 0 {
		return nil, nil
	}
	_, mods, err := moduleDoc(text)
	if err != nil {
		return nil, err
	}
	var out []namedModule
	for i := 0; i+1 < len(mods.Content); i += 2 {
		var v any
		if err := mods.Content[i+1].Decode(&v); err != nil {
			return nil, fmt.Errorf("module %s: %w", mods.Content[i].Value, err)
		}
		v, err := jsonable(v)
		if err != nil {
			return nil, fmt.Errorf("module %s: %w", mods.Content[i].Value, err)
		}
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("module %s: %w", mods.Content[i].Value, err)
		}
		out = append(out, namedModule{name: mods.Content[i].Value, value: b})
	}
	return out, nil
}

// setModule returns the module file with module name set to raw, a JSON object in the shape of the
// file's modules; nil removes it.
func setModule(text []byte, name string, raw json.RawMessage) ([]byte, error) {
	doc, mods, err := moduleDoc(text)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if hasAlias(doc, 0) {
		expandAliases(doc, 0)
	}
	at := -1
	for i := 0; i+1 < len(mods.Content); i += 2 {
		if mods.Content[i].Value == name {
			at = i
		}
	}
	switch {
	case raw == nil && at < 0:
		return nil, fmt.Errorf("%w: no module %q", ErrNotFound, name)
	case raw == nil:
		mods.Content = append(mods.Content[:at], mods.Content[at+2:]...)
	default:
		n, err := nodeFromJSON(raw)
		if err != nil {
			return nil, err
		}
		if at >= 0 {
			n.HeadComment, n.LineComment, n.FootComment = mods.Content[at+1].HeadComment, mods.Content[at+1].LineComment, mods.Content[at+1].FootComment
			mods.Content[at+1] = n
		} else {
			mods.Style = 0 // modules: {} becomes a block
			mods.Content = append(mods.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, n)
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("the module file: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// nodeFromJSON reads a module given as a JSON object into a YAML tree in block style.
func nodeFromJSON(raw json.RawMessage) (*yaml.Node, error) {
	bad := fmt.Errorf("%w: a module is a JSON object such as {\"prober\": \"http\", \"timeout\": \"10s\", \"http\": {\"method\": \"HEAD\"}}", ErrBadInput)
	if !json.Valid(raw) {
		return nil, bad
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%w: %v", bad, err) //nolint:errorlint // one wrapped error is enough
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, bad
	}
	n := doc.Content[0]
	plain(n)
	return n, nil
}

// plain drops the flow and quoting styles JSON brings, so the encoder writes YAML as people do.
func plain(n *yaml.Node) {
	n.Style = 0
	for _, c := range n.Content {
		plain(c)
	}
}

// maxDepth bounds the walks over YAML trees; a module file is never this deep.
const maxDepth = 64

func hasAlias(n *yaml.Node, depth int) bool {
	if n.Kind == yaml.AliasNode {
		return true
	}
	if depth > maxDepth {
		return false
	}
	for _, c := range n.Content {
		if hasAlias(c, depth+1) {
			return true
		}
	}
	return false
}

// expandAliases replaces every alias with a copy of what it names and drops the anchors, so that a
// module can be changed or removed without leaving another pointing at nothing. Merge keys (<<) keep
// working with the copy.
func expandAliases(n *yaml.Node, depth int) {
	n.Anchor = ""
	if depth > maxDepth {
		return
	}
	for i, c := range n.Content {
		if c.Kind == yaml.AliasNode && c.Alias != nil {
			n.Content[i] = deepCopy(c.Alias, depth+1)
		}
		expandAliases(n.Content[i], depth+1)
	}
}

func deepCopy(n *yaml.Node, depth int) *yaml.Node {
	c := *n
	c.Anchor = ""
	c.Content = nil
	if depth > maxDepth {
		return &c
	}
	for _, x := range n.Content {
		if x.Kind == yaml.AliasNode && x.Alias != nil {
			x = x.Alias
		}
		c.Content = append(c.Content, deepCopy(x, depth+1))
	}
	return &c
}

// jsonable turns what YAML decodes into something JSON can write: mappings with keys that are not
// strings get them as text.
func jsonable(v any) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			j, err := jsonable(e)
			if err != nil {
				return nil, err
			}
			out[k] = j
		}
		return out, nil
	case map[any]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			j, err := jsonable(e)
			if err != nil {
				return nil, err
			}
			out[fmt.Sprint(k)] = j
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			j, err := jsonable(e)
			if err != nil {
				return nil, err
			}
			out[i] = j
		}
		return out, nil
	}
	return v, nil
}
