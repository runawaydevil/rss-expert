package feedout

import (
	"bytes"
	"encoding/xml"

	"github.com/runawaydevil/rss-expert/internal/feed"
)

type attr struct {
	name  string
	value string
}

type writer struct {
	buf   bytes.Buffer
	depth int
}

func (w *writer) indent() {
	for i := 0; i < w.depth; i++ {
		w.buf.WriteByte('\t')
	}
}

func (w *writer) escape(s string) {
	xml.EscapeText(&w.buf, []byte(s))
}

func (w *writer) attrs(list []attr) {
	for _, a := range list {
		w.buf.WriteByte(' ')
		w.buf.WriteString(a.name)
		w.buf.WriteString(`="`)
		w.escape(a.value)
		w.buf.WriteByte('"')
	}
}

func (w *writer) open(name string, list ...attr) {
	w.indent()
	w.buf.WriteByte('<')
	w.buf.WriteString(name)
	w.attrs(list)
	w.buf.WriteString(">\n")
	w.depth++
}

func (w *writer) close(name string) {
	w.depth--
	w.indent()
	w.buf.WriteString("</")
	w.buf.WriteString(name)
	w.buf.WriteString(">\n")
}

func (w *writer) leaf(name, text string, list ...attr) {
	w.indent()
	w.buf.WriteByte('<')
	w.buf.WriteString(name)
	w.attrs(list)
	if text == "" {
		w.buf.WriteString("/>\n")
		return
	}
	w.buf.WriteByte('>')
	w.escape(text)
	w.buf.WriteString("</")
	w.buf.WriteString(name)
	w.buf.WriteString(">\n")
}

func (w *writer) optional(name, text string, list ...attr) {
	if text == "" {
		return
	}
	w.leaf(name, text, list...)
}

func (w *writer) element(ns *namespaces, el *feed.Element) {
	name := ns.qualify(el.Name)
	list := make([]attr, 0, len(el.Attrs))
	for _, a := range el.Attrs {
		list = append(list, attr{ns.qualify(a.Name), a.Value})
	}

	if len(el.Children) == 0 {
		w.leaf(name, el.Text, list...)
		return
	}

	w.open(name, list...)
	if el.Text != "" {
		w.indent()
		w.escape(el.Text)
		w.buf.WriteByte('\n')
	}
	for i := range el.Children {
		w.element(ns, &el.Children[i])
	}
	w.close(name)
}
