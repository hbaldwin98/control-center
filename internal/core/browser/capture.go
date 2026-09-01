package browser

import (
	"bytes"
	"strings"
)

const maxCapturedResponses = 32

func captureMIME(mime string, body []byte) bool {
	m := strings.ToLower(mime)
	if i := strings.IndexByte(m, ';'); i >= 0 {
		m = strings.TrimSpace(m[:i])
	}
	if strings.Contains(m, "json") || strings.Contains(m, "csv") || strings.Contains(m, "xml") {
		return true
	}
	trim := bytes.TrimSpace(body)
	return len(trim) > 0 && (trim[0] == '{' || trim[0] == '[')
}

func appendCaptured(dst []Resource, r Resource, maxN, maxBytes int) []Resource {
	if maxN <= 0 {
		maxN = maxCapturedResponses
	}
	if maxBytes <= 0 {
		maxBytes = defaultMaxResource
	}
	if len(r.Body) > maxBytes {
		return dst
	}
	item := Resource{
		URL:    r.URL,
		MIME:   r.MIME,
		Status: r.Status,
		Body:   append([]byte(nil), r.Body...),
	}
	dst = append(dst, item)
	if len(dst) > maxN {
		dst = dst[len(dst)-maxN:]
	}
	return dst
}

func copyResources(in []Resource) []Resource {
	if len(in) == 0 {
		return nil
	}
	out := make([]Resource, len(in))
	for i, r := range in {
		out[i] = Resource{
			URL:    r.URL,
			MIME:   r.MIME,
			Status: r.Status,
			Body:   append([]byte(nil), r.Body...),
		}
	}
	return out
}
