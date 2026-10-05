package ghstack

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
)

// node is an ordered JSON tree. Objects keep their member order and anything
// that is not an object or an array is kept as its raw bytes, so a file
// edited through it keeps members we don't model and the order gh stack
// wrote them in.
type node struct {
	kind    byte // '{', '[' or 0 for a scalar
	members []member
	items   []*node
	raw     jsontext.Value
}

type member struct {
	key string
	val *node
}

func parseTree(data []byte) (*node, error) {
	d := jsontext.NewDecoder(bytes.NewReader(data))
	n, err := parseNode(d)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", FileName, err)
	}
	return n, nil
}

func parseNode(d *jsontext.Decoder) (*node, error) {
	switch d.PeekKind() {
	case '{':
		if _, err := d.ReadToken(); err != nil {
			return nil, err
		}
		n := &node{kind: '{'}
		for d.PeekKind() != '}' {
			k, err := d.ReadToken()
			if err != nil {
				return nil, err
			}
			key := k.String() // a token is voided by the next decoder call
			v, err := parseNode(d)
			if err != nil {
				return nil, err
			}
			n.members = append(n.members, member{key, v})
		}
		_, err := d.ReadToken()
		return n, err
	case '[':
		if _, err := d.ReadToken(); err != nil {
			return nil, err
		}
		n := &node{kind: '['}
		for d.PeekKind() != ']' {
			v, err := parseNode(d)
			if err != nil {
				return nil, err
			}
			n.items = append(n.items, v)
		}
		_, err := d.ReadToken()
		return n, err
	default:
		v, err := d.ReadValue()
		if err != nil {
			return nil, err
		}
		return &node{raw: v.Clone()}, nil
	}
}

// encode renders the tree with two space indentation and a trailing newline.
func (n *node) encode() ([]byte, error) {
	var buf bytes.Buffer
	e := jsontext.NewEncoder(&buf, jsontext.WithIndent("  "))
	if err := n.write(e); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (n *node) write(e *jsontext.Encoder) error {
	switch n.kind {
	case '{':
		if err := e.WriteToken(jsontext.BeginObject); err != nil {
			return err
		}
		for _, m := range n.members {
			if err := e.WriteToken(jsontext.String(m.key)); err != nil {
				return err
			}
			if err := m.val.write(e); err != nil {
				return err
			}
		}
		return e.WriteToken(jsontext.EndObject)
	case '[':
		if err := e.WriteToken(jsontext.BeginArray); err != nil {
			return err
		}
		for _, it := range n.items {
			if err := it.write(e); err != nil {
				return err
			}
		}
		return e.WriteToken(jsontext.EndArray)
	default:
		return e.WriteValue(n.raw)
	}
}

// get returns the member named key, or nil.
func (n *node) get(key string) *node {
	for _, m := range n.members {
		if m.key == key {
			return m.val
		}
	}
	return nil
}

// set replaces the member named key or appends it.
func (n *node) set(key string, v *node) {
	for i, m := range n.members {
		if m.key == key {
			n.members[i].val = v
			return
		}
	}
	n.members = append(n.members, member{key, v})
}

// str returns the member's string value, "" when missing or not a string.
func (n *node) str(key string) string {
	m := n.get(key)
	if m == nil || m.kind != 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(m.raw, &s); err != nil {
		return ""
	}
	return s
}

func stringNode(s string) *node {
	b, _ := json.Marshal(s)
	return &node{raw: jsontext.Value(b)}
}

func objectNode(members ...member) *node {
	return &node{kind: '{', members: members}
}
