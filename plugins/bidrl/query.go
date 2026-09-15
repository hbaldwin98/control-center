package bidrl

import (
	"encoding/json"
	"strings"
	"time"
	"unicode"
)

const maxQueryVariants = 3

var queryStop = map[string]struct{}{
	"a": {}, "an": {}, "the": {}, "and": {}, "or": {}, "of": {}, "for": {},
	"with": {}, "in": {}, "on": {}, "to": {}, "from": {}, "at": {},
	"item": {}, "items": {}, "lot": {}, "auction": {}, "new": {}, "used": {},
	"set": {}, "pack": {}, "pair": {}, "piece": {}, "pieces": {},
	"black": {}, "white": {}, "lot#": {},
}

// expandQuery turns a typed search into BidRL keyword variants. Titles on this
// site are noisy, so a model token alone (K-Supreme, 20V) usually hits more
// than the full phrase, and a later pass scores the original words against
// both the title and any vision identification we already stored.
func expandQuery(q string) []string {
	q = strings.Join(strings.Fields(strings.TrimSpace(q)), " ")
	if q == "" {
		return nil
	}
	tokens := queryTokens(q)
	var models, rest []string
	for _, t := range tokens {
		if isModelToken(t) {
			models = append(models, t)
			continue
		}
		if _, stop := queryStop[strings.ToLower(t)]; stop {
			continue
		}
		if len(t) >= 3 {
			rest = append(rest, t)
		}
	}
	out := []string{q}
	for _, m := range models {
		out = append(out, m)
	}
	if len(models) == 0 && len(rest) >= 2 {
		short := rest[0] + " " + rest[1]
		if !strings.EqualFold(short, q) {
			out = append(out, short)
		}
	}
	return uniqueFold(out, maxQueryVariants)
}

func queryTokens(q string) []string {
	var tok strings.Builder
	var out []string
	flush := func() {
		s := tok.String()
		tok.Reset()
		if s != "" {
			out = append(out, s)
		}
	}
	for _, r := range q {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			tok.WriteRune(r)
		case r == '-' && tok.Len() > 0:
			tok.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return out
}

func isModelToken(t string) bool {
	hasDigit, hasLetter, hasHyphen := false, false, false
	for _, r := range t {
		switch {
		case unicode.IsDigit(r):
			hasDigit = true
		case unicode.IsLetter(r):
			hasLetter = true
		case r == '-':
			hasHyphen = true
		}
	}
	if hasDigit && hasLetter {
		return true
	}
	return hasHyphen && hasLetter && len(t) >= 3
}

// matchScore ranks a lot against the original query. Identification is the
// vision pass: a chair titled "office mesh" that the photos named as an Aeron
// should still rise for "herman miller".
func matchScore(query, title, ident, model, category, terms string) (float64, string) {
	qtoks := contentTokens(query)
	if len(qtoks) == 0 {
		return 0, ""
	}
	titleLow := strings.ToLower(title)
	identLow := strings.ToLower(ident)
	modelLow := strings.ToLower(model)
	catLow := strings.ToLower(category)
	termsLow := strings.ToLower(terms)
	hay := titleLow + " " + identLow + " " + modelLow + " " + catLow + " " + termsLow

	var (
		score   float64
		reasons []string
		hit     int
	)
	for _, t := range qtoks {
		low := strings.ToLower(t)
		inTitle := strings.Contains(titleLow, low)
		inIdent := identLow != "" && strings.Contains(identLow, low)
		inModel := modelLow != "" && strings.Contains(modelLow, low)
		inCat := catLow != "" && strings.Contains(catLow, low)
		inTerms := termsLow != "" && strings.Contains(termsLow, low)
		if !inTitle && !inIdent && !inModel && !inCat && !inTerms && !strings.Contains(hay, low) {
			continue
		}
		hit++
		switch {
		case isModelToken(t) && (inModel || inTitle || inIdent || inTerms):
			score += 3
			reasons = append(reasons, "model "+t)
		case inIdent && !inTitle:
			score += 2
			reasons = append(reasons, "photos: "+t)
		case inTerms && !inTitle:
			score += 2
			reasons = append(reasons, "alias: "+t)
		case inCat:
			score += 1
			reasons = append(reasons, "category "+t)
		default:
			score += 1
		}
	}
	if hit == 0 {
		return 0, ""
	}
	score += float64(hit) / float64(len(qtoks))
	if hit == len(qtoks) {
		score += 1
		reasons = append(reasons, "all words")
	}
	return score, strings.Join(unique(reasons), "; ")
}

func contentTokens(q string) []string {
	var out []string
	for _, t := range queryTokens(q) {
		if _, stop := queryStop[strings.ToLower(t)]; stop {
			continue
		}
		if len(t) < 2 {
			continue
		}
		out = append(out, t)
	}
	return out
}

func notesAndTerms(termsJSON, notes string) string {
	var terms []string
	_ = json.Unmarshal([]byte(termsJSON), &terms)
	return strings.TrimSpace(strings.Join(terms, " ") + " " + notes)
}

const endingSoonWindow = 24 * time.Hour

func endingSoon(endsAt string, now time.Time) bool {
	return endingWithin(endsAt, now, endingSoonWindow)
}

// endingWithin is true when a close time is still ahead and no further away than
// window. An unparseable or already-passed close time is never "ending".
func endingWithin(endsAt string, now time.Time, window time.Duration) bool {
	t, ok := parseEndsAt(endsAt)
	if !ok {
		return false
	}
	if t.Before(now) {
		return false
	}
	return !t.After(now.Add(window))
}

func hasEnded(endsAt string, now time.Time) bool {
	t, ok := parseEndsAt(endsAt)
	return ok && t.Before(now)
}

// auctionEnded is true when the auction close is in the past, or every lot that
// recorded an end time has already closed. Unknown timestamps are left alone.
func auctionEnded(auctionEnds string, lotEnds []string, now time.Time) bool {
	if hasEnded(auctionEnds, now) {
		return true
	}
	known := 0
	for _, ends := range lotEnds {
		if ends == "" {
			continue
		}
		known++
		if !hasEnded(ends, now) {
			return false
		}
	}
	return known > 0
}

func parseEndsAt(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	if canon := parseEndTimeString(s, bidrlUnixOffset); canon != "" {
		t, err := time.Parse(time.RFC3339, canon)
		return t, err == nil
	}
	return time.Time{}, false
}

func uniqueFold(in []string, max int) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		k := strings.ToLower(s)
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, s)
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
}
