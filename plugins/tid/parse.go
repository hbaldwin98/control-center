package tid

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// Reading is one calendar day of usage in America/Los_Angeles.
type Reading struct {
	Day       string
	KWh       float64
	CostCents *int64
}

var (
	dayISO    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	kwhToken  = regexp.MustCompile(`(?i)kwh|usage|consumption|kilo`)
	dateToken = regexp.MustCompile(`(?i)^date$|^day$|^period|^read`)
	costToken = regexp.MustCompile(`(?i)cost|charge|amount|\$|dollar`)
)

var dayLayouts = []string{
	"2006-01-02",
	"2006/01/02",
	"01/02/2006",
	"1/2/2006",
	"01-02-2006",
	"Jan 2, 2006",
	"January 2, 2006",
	time.RFC3339,
}

// Parse extracts daily readings from CSV, JSON, or an HTML table.
func Parse(body []byte, mime string) []Reading {
	mime = strings.ToLower(strings.TrimSpace(mime))
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = strings.TrimSpace(mime[:i])
	}
	trim := bytes.TrimSpace(body)
	if len(trim) == 0 {
		return nil
	}
	switch {
	case strings.Contains(mime, "json") || trim[0] == '{' || trim[0] == '[':
		if out := parseJSON(trim); len(out) > 0 {
			return mergeDays(out)
		}
	case strings.Contains(mime, "csv") || looksCSV(trim):
		if out := parseCSV(trim); len(out) > 0 {
			return mergeDays(out)
		}
	}
	if strings.Contains(mime, "html") || bytes.Contains(trim, []byte("<")) {
		if out := parseHTML(trim); len(out) > 0 {
			return mergeDays(out)
		}
	}
	if out := parseCSV(trim); len(out) > 0 {
		return mergeDays(out)
	}
	if out := parseJSON(trim); len(out) > 0 {
		return mergeDays(out)
	}
	return nil
}

func looksCSV(b []byte) bool {
	s := string(b)
	if !strings.Contains(s, ",") {
		return false
	}
	first, _, _ := strings.Cut(s, "\n")
	return strings.Count(first, ",") >= 1 && (kwhToken.MatchString(first) || dateToken.MatchString(first))
}

func parseCSV(b []byte) []Reading {
	r := csv.NewReader(bytes.NewReader(b))
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	rows, err := r.ReadAll()
	if err != nil || len(rows) == 0 {
		return nil
	}
	start := 0
	di, ki, ci := -1, -1, -1
	if looksHeader(rows[0]) {
		di, ki, ci = columnIndexes(rows[0])
		start = 1
	} else {
		di, ki, ci = 0, 1, -1
		if len(rows[0]) > 2 {
			ci = 2
		}
	}
	if di < 0 || ki < 0 {
		return nil
	}
	var out []Reading
	for _, row := range rows[start:] {
		if len(row) == 0 {
			continue
		}
		day := parseDay(cell(row, di))
		kwh, ok := parseKWh(cell(row, ki))
		if day == "" || !ok {
			continue
		}
		item := Reading{Day: day, KWh: kwh}
		if c, ok := parseCents(cell(row, ci)); ok {
			item.CostCents = &c
		}
		out = append(out, item)
	}
	return out
}

func looksHeader(row []string) bool {
	for _, c := range row {
		if dateToken.MatchString(strings.TrimSpace(c)) || kwhToken.MatchString(c) {
			return true
		}
	}
	return false
}

func columnIndexes(header []string) (date, kwh, cost int) {
	date, kwh, cost = -1, -1, -1
	for i, h := range header {
		h = strings.TrimSpace(h)
		switch {
		case date < 0 && dateToken.MatchString(h):
			date = i
		case kwh < 0 && kwhToken.MatchString(h):
			kwh = i
		case cost < 0 && costToken.MatchString(h):
			cost = i
		}
	}
	if date < 0 && len(header) > 0 {
		date = 0
	}
	if kwh < 0 && len(header) > 1 {
		kwh = 1
	}
	return date, kwh, cost
}

func parseJSON(b []byte) []Reading {
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil
	}
	return walkJSON(raw)
}

func walkJSON(v any) []Reading {
	switch t := v.(type) {
	case []any:
		var out []Reading
		allObj := true
		for _, item := range t {
			if _, ok := item.(map[string]any); !ok {
				allObj = false
				break
			}
		}
		if allObj {
			for _, item := range t {
				if r, ok := readingFromMap(item.(map[string]any)); ok {
					out = append(out, r)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
		for _, item := range t {
			out = append(out, walkJSON(item)...)
		}
		return out
	case map[string]any:
		if r, ok := readingFromMap(t); ok {
			return []Reading{r}
		}
		var out []Reading
		for _, key := range []string{"readings", "data", "usage", "usageList", "items", "results", "values", "days"} {
			if child, ok := t[key]; ok {
				out = append(out, walkJSON(child)...)
			}
		}
		if len(out) > 0 {
			return out
		}
		for _, child := range t {
			out = append(out, walkJSON(child)...)
		}
		return out
	default:
		return nil
	}
}

func readingFromMap(m map[string]any) (Reading, bool) {
	var day string
	var kwh float64
	var kwhOK bool
	var cents *int64
	for k, v := range m {
		lk := strings.ToLower(k)
		switch {
		case day == "" && (strings.Contains(lk, "date") || lk == "day" || lk == "period" || lk == "timestamp"):
			day = parseDay(fmt.Sprint(v))
		case !kwhOK && (lk == "usage" || lk == "dailyusage" || strings.Contains(lk, "kwh") || lk == "consumption" || lk == "value"):
			kwh, kwhOK = jsonNumber(v)
		case cents == nil && strings.Contains(lk, "cent"):
			if n, ok := jsonNumber(v); ok {
				c := int64(n + 0.5)
				cents = &c
			}
		case cents == nil && (strings.Contains(lk, "cost") || strings.Contains(lk, "charge") || lk == "amount"):
			if n, ok := jsonNumber(v); ok {
				c := dollarsToCents(n, fmt.Sprint(v))
				cents = &c
			}
		}
	}
	if day == "" || !kwhOK {
		return Reading{}, false
	}
	return Reading{Day: day, KWh: kwh, CostCents: cents}, true
}

func jsonNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		return parseKWh(n)
	default:
		return 0, false
	}
}

func parseHTML(b []byte) []Reading {
	root, err := html.Parse(bytes.NewReader(b))
	if err != nil {
		return nil
	}
	var tables []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "table" || n.Data == "mat-table") {
			tables = append(tables, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	var best []Reading
	for _, table := range tables {
		rows := tableRows(table)
		if len(rows) < 2 {
			continue
		}
		di, ki, ci := columnIndexes(rows[0])
		if di < 0 || ki < 0 {
			continue
		}
		var out []Reading
		for _, row := range rows[1:] {
			day := parseDay(cell(row, di))
			kwh, ok := parseKWh(cell(row, ki))
			if day == "" || !ok {
				continue
			}
			item := Reading{Day: day, KWh: kwh}
			if c, ok := parseCents(cell(row, ci)); ok {
				item.CostCents = &c
			}
			out = append(out, item)
		}
		if len(out) > len(best) {
			best = out
		}
	}
	return best
}

func tableRows(table *html.Node) [][]string {
	var rows [][]string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "tr" || n.Data == "mat-row" || n.Data == "mat-header-row") {
			var row []string
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode && (c.Data == "td" || c.Data == "th" || c.Data == "mat-cell" || c.Data == "mat-header-cell") {
					row = append(row, strings.TrimSpace(textOf(c)))
				}
			}
			if len(row) > 0 {
				rows = append(rows, row)
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(table)
	return rows
}

func textOf(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(textOf(c))
	}
	return b.String()
}

func parseDay(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, 'T'); i > 0 {
		s = s[:i]
	}
	if dayISO.MatchString(s) {
		if _, err := time.Parse("2006-01-02", s); err == nil {
			return s
		}
	}
	for _, layout := range dayLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return ""
}

func parseKWh(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, ",", "")
	s = strings.TrimSuffix(strings.TrimSpace(s), "kWh")
	s = strings.TrimSuffix(strings.TrimSpace(s), "kwh")
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 || f > 1_000_000 {
		return 0, false
	}
	return f, true
}

func parseCents(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	neg := strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")")
	s = strings.Trim(s, "() ")
	s = strings.ReplaceAll(s, ",", "")
	s = strings.TrimPrefix(s, "$")
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	c := dollarsToCents(f, s)
	if neg {
		c = -c
	}
	return c, true
}

func dollarsToCents(n float64, raw string) int64 {
	if !strings.Contains(raw, ".") && n >= 100 && n == float64(int64(n)) {
		// already cents if it looks like an integer of at least a dollar
		if n > 10000 {
			return int64(n)
		}
	}
	return int64(n*100 + 0.5)
}

func cell(row []string, i int) string {
	if i < 0 || i >= len(row) {
		return ""
	}
	return row[i]
}

func mergeDays(in []Reading) []Reading {
	type acc struct {
		kwh   float64
		cents *int64
	}
	order := make([]string, 0, len(in))
	seen := map[string]*acc{}
	for _, r := range in {
		if r.Day == "" {
			continue
		}
		a, ok := seen[r.Day]
		if !ok {
			a = &acc{}
			seen[r.Day] = a
			order = append(order, r.Day)
		}
		a.kwh += r.KWh
		if r.CostCents != nil {
			if a.cents == nil {
				v := *r.CostCents
				a.cents = &v
			} else {
				*a.cents += *r.CostCents
			}
		}
	}
	out := make([]Reading, 0, len(order))
	for _, day := range order {
		a := seen[day]
		out = append(out, Reading{Day: day, KWh: a.kwh, CostCents: a.cents})
	}
	return out
}
