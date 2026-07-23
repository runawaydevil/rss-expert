package feed

import "encoding/xml"

func (e *Element) Child(space, local string) *Element {
	for i := range e.Children {
		c := &e.Children[i]
		if c.Name.Space == space && c.Name.Local == local {
			return c
		}
	}
	return nil
}

func (e *Element) ChildAny(local string) *Element {
	for i := range e.Children {
		c := &e.Children[i]
		if c.Name.Local == local {
			return c
		}
	}
	return nil
}

func (e *Element) ChildText(space, local string) string {
	if c := e.Child(space, local); c != nil {
		return c.Text
	}
	return ""
}

func (e *Element) Attr(local string) string {
	for _, a := range e.Attrs {
		if a.Name.Local == local && a.Name.Space == "" {
			return a.Value
		}
	}
	return ""
}

func (e *Element) Descendants(local string) []*Element {
	var out []*Element
	var walk func(*Element)
	walk = func(n *Element) {
		for i := range n.Children {
			c := &n.Children[i]
			if c.Name.Local == local {
				out = append(out, c)
			}
			walk(c)
		}
	}
	walk(e)
	return out
}

func IsNamespaceDeclaration(a xml.Attr) bool {
	return a.Name.Space == "xmlns" || a.Name.Space == NSXMLNS ||
		(a.Name.Space == "" && a.Name.Local == "xmlns")
}
