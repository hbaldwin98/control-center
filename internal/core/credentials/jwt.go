package credentials

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// claims decodes the payload of a JWT without verifying its signature.
//
// Nothing here is a security decision. The token arrives either from the provider's
// token endpoint over TLS or from the administrator's own machine, and the values read
// out of it — the ChatGPT account id, the plan name, the expiry — are routing and
// display metadata. Authority still rests entirely with the provider, which rejects a
// forged bearer token regardless of what its payload claims.
func claims(jwt string) (map[string]any, bool) {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, false
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false
	}
	return out, true
}

// claimString walks a nested claim path and returns the string it ends on.
// OpenAI namespaces its ChatGPT claims, so the account id lives at
// ["https://api.openai.com/auth", "chatgpt_account_id"].
func claimString(jwt string, path []string) string {
	if len(path) == 0 {
		return ""
	}
	node, ok := claims(jwt)
	if !ok {
		return ""
	}
	for i, key := range path {
		v, present := node[key]
		if !present {
			return ""
		}
		if i == len(path)-1 {
			s, _ := v.(string)
			return s
		}
		next, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		node = next
	}
	return ""
}

// jwtExpiry reads the standard `exp` claim. A provider that omits expires_in from its
// token response still tells us when the access token dies, and a credential whose
// expiry we do not know is one we would never refresh.
func jwtExpiry(jwt string) time.Time {
	c, ok := claims(jwt)
	if !ok {
		return time.Time{}
	}
	exp, ok := c["exp"].(float64)
	if !ok || exp <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(exp), 0).UTC()
}
