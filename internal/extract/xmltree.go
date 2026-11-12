package extract

import (
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

// node is a minimal XML tree that keeps tag and attribute names exactly as
// written (prefix included), so a document can be re-serialised faithfully.
type node struct {
	name     string // "" for a text node
	attrs    []xml.Attr
	children []*node
	text     string // only for text nodes
}

func rawName(n xml.Name) string {
	if n.Space != "" {
		return n.Space + ":" + n.Local
	}
	return n.Local
}

// parseXML builds the tree of a well-formed document. Comments, processing
// instructions and the XML declaration are dropped.
func parseXML(r io.Reader) (*node, error) {
	decoder := xml.NewDecoder(r)
	var root *node
	var stack []*node
	for {
		token, err := decoder.RawToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			n := &node{name: rawName(t.Name)}
			for _, attr := range t.Attr {
				n.attrs = append(n.attrs, xml.Attr{Name: xml.Name{Local: rawName(attr.Name)}, Value: attr.Value})
			}
			switch {
			case len(stack) > 0:
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, n)
			case root == nil:
				root = n
			default:
				return nil, errors.New("more than one root element")
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) == 0 || stack[len(stack)-1].name != rawName(t.Name) {
				return nil, errors.New("mismatched closing tag </" + rawName(t.Name) + ">")
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, &node{text: string(t)})
			}
		}
	}
	if root == nil || len(stack) != 0 {
		return nil, errors.New("incomplete document")
	}
	return root, nil
}

func (n *node) attr(name string) string {
	for _, a := range n.attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// child returns the first child element with the given name.
func (n *node) child(name string) *node {
	for _, c := range n.children {
		if c.name == name {
			return c
		}
	}
	return nil
}

// walk visits every element below n, depth first. Returning false from visit
// skips the children of that element.
func (n *node) walk(visit func(*node) bool) {
	for _, c := range n.children {
		if c.name == "" {
			continue
		}
		if visit(c) {
			c.walk(visit)
		}
	}
}

func (n *node) hasElements() bool {
	for _, c := range n.children {
		if c.name != "" {
			return true
		}
	}
	return false
}

// innerText concatenates the direct text of an element.
func (n *node) innerText() string {
	var b strings.Builder
	for _, c := range n.children {
		if c.name == "" {
			b.WriteString(c.text)
		}
	}
	return b.String()
}

// render serialises the tree with one element per line and one space of
// indentation per level, so that line-based edits are practical.
func (n *node) render() string {
	var b strings.Builder
	n.write(&b, 0)
	return b.String()
}

func (n *node) write(b *strings.Builder, depth int) {
	indent := strings.Repeat(" ", depth)
	b.WriteString(indent)
	b.WriteByte('<')
	b.WriteString(n.name)
	for _, a := range n.attrs {
		b.WriteByte(' ')
		b.WriteString(a.Name.Local)
		b.WriteString(`="`)
		b.WriteString(escapeAttr(a.Value))
		b.WriteByte('"')
	}
	switch {
	case len(n.children) == 0:
		b.WriteString(" />")
	case !n.hasElements():
		b.WriteByte('>')
		b.WriteString(escapeText(n.innerText()))
		b.WriteString("</" + n.name + ">")
	default:
		b.WriteString(">\n")
		for _, c := range n.children {
			if c.name == "" {
				if text := strings.TrimSpace(c.text); text != "" {
					b.WriteString(indent + " " + escapeText(text) + "\n")
				}
				continue
			}
			c.write(b, depth+1)
			b.WriteByte('\n')
		}
		b.WriteString(indent + "</" + n.name + ">")
	}
}

var (
	textEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	attrEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "\n", "&#10;", "\r", "&#13;", "\t", "&#9;")
)

func escapeText(s string) string { return textEscaper.Replace(s) }
func escapeAttr(s string) string { return attrEscaper.Replace(s) }
