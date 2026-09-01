package browser

import (
	"fmt"
	"strings"

	"golang.org/x/net/html"
)

// selector is the closed subset both Fake and WaitFor understand. Playwright Fill/Click
// accept full CSS; Fake and WaitFor are limited to tag, #id, .class, and [attr=value].
type selector struct {
	tag     string
	id      string
	class   string
	attr    string
	attrVal string
	attrOp  string // "", "exists", "eq"
}

func parseSelector(sel string) (selector, error) {
	sel = strings.TrimSpace(sel)
	if sel == "" {
		return selector{}, fmt.Errorf("empty selector")
	}
	var s selector
	rest := sel
	if i := strings.IndexByte(rest, '['); i >= 0 {
		j := strings.IndexByte(rest, ']')
		if j < 0 || j < i {
			return selector{}, fmt.Errorf("bad attribute selector")
		}
		inner := strings.TrimSpace(rest[i+1 : j])
		rest = rest[:i] + rest[j+1:]
		if inner == "" {
			return selector{}, fmt.Errorf("empty attribute selector")
		}
		if eq := strings.IndexByte(inner, '='); eq >= 0 {
			s.attr = strings.ToLower(strings.TrimSpace(inner[:eq]))
			v := strings.TrimSpace(inner[eq+1:])
			v = strings.Trim(v, `"'`)
			s.attrVal = v
			s.attrOp = "eq"
		} else {
			s.attr = strings.ToLower(inner)
			s.attrOp = "exists"
		}
		if s.attr == "" {
			return selector{}, fmt.Errorf("empty attribute selector")
		}
	}
	rest = strings.TrimSpace(rest)
	switch {
	case rest == "":
	case strings.HasPrefix(rest, "#"):
		s.id = rest[1:]
	case strings.HasPrefix(rest, "."):
		s.class = rest[1:]
	default:
		if i := strings.IndexByte(rest, '#'); i >= 0 {
			s.tag = rest[:i]
			s.id = rest[i+1:]
		} else if i := strings.IndexByte(rest, '.'); i >= 0 {
			s.tag = rest[:i]
			s.class = rest[i+1:]
		} else {
			s.tag = rest
		}
	}
	return s, nil
}

func (s selector) match(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	if s.tag != "" && n.Data != s.tag {
		return false
	}
	if s.id != "" && attr(n, "id") != s.id {
		return false
	}
	if s.class != "" && !hasClass(n, s.class) {
		return false
	}
	switch s.attrOp {
	case "exists":
		if !hasAttr(n, s.attr) {
			return false
		}
	case "eq":
		if attr(n, s.attr) != s.attrVal {
			return false
		}
	}
	return true
}

func hasAttr(n *html.Node, name string) bool {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, name) {
			return true
		}
	}
	return false
}

func htmlMatches(doc, sel string) (bool, error) {
	n, err := findFirst(doc, sel)
	if err != nil {
		return false, err
	}
	return n != nil, nil
}

func findFirst(doc, sel string) (*html.Node, error) {
	s, err := parseSelector(sel)
	if err != nil {
		return nil, err
	}
	root, err := html.Parse(strings.NewReader(doc))
	if err != nil {
		return nil, err
	}
	var found *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != nil || n == nil {
			return
		}
		if s.match(n) {
			found = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return found, nil
}
