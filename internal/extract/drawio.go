package extract

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"io"
	"net/url"
	"strings"
)

// maxDiagramBytes bounds the size of one decompressed draw.io diagram.
const maxDiagramBytes = 64 << 20

// DecodeDrawio expands the compressed diagrams of a draw.io file into
// readable XML. draw.io can store each <diagram> as a blob (base64, raw
// deflate, URL-encoding). Files with nothing to expand are returned untouched.
func DecodeDrawio(text string) string {
	root, err := parseXML(strings.NewReader(text))
	if err != nil {
		return text
	}
	changed := false
	expand := func(diagram *node) {
		if diagram.name != "diagram" || diagram.hasElements() {
			return
		}
		payload := strings.TrimSpace(diagram.innerText())
		if payload == "" {
			return
		}
		compressed, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return
		}
		inflated, err := io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(compressed)), maxDiagramBytes))
		if err != nil {
			return
		}
		decoded, err := url.PathUnescape(string(inflated))
		if err != nil {
			return
		}
		graph, err := parseXML(strings.NewReader(decoded))
		if err != nil {
			return
		}
		diagram.children = []*node{graph}
		changed = true
	}
	expand(root)
	root.walk(func(n *node) bool {
		expand(n)
		return true
	})
	if !changed {
		return text
	}
	return root.render()
}
