package http

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// biographyLanguages are the languages a request asks a person's biography
// in, most wanted first, as primary language subtags ("de-CH" → "de"):
// ?lang= alone when it names a language, otherwise the Accept-Language
// header's languages by quality. The store falls back to English, then to
// any language, after these.
func biographyLanguages(r *http.Request) []string {
	if l, ok := primarySubtag(r.URL.Query().Get("lang")); ok {
		return []string{l}
	}
	return acceptLanguages(r.Header.Get("Accept-Language"))
}

// acceptLanguages parses an Accept-Language header (RFC 9110 §12.5.4) into
// primary subtags by descending quality, ties in header order, each once.
// The wildcard, q=0 and malformed ranges are dropped.
func acceptLanguages(header string) []string {
	type ranked struct {
		lang string
		q    float64
	}
	var all []ranked
	for _, part := range strings.Split(header, ",") {
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		lang, ok := primarySubtag(tag)
		if !ok {
			continue
		}
		q := 1.0
		for _, p := range strings.Split(params, ";") {
			k, v, found := strings.Cut(strings.TrimSpace(p), "=")
			if found && strings.EqualFold(strings.TrimSpace(k), "q") {
				f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
				if err != nil || f < 0 || f > 1 {
					f = 0
				}
				q = f
			}
		}
		if q > 0 {
			all = append(all, ranked{lang, q})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].q > all[j].q })
	var out []string
	seen := map[string]bool{}
	for _, r := range all {
		if !seen[r.lang] {
			seen[r.lang] = true
			out = append(out, r.lang)
		}
	}
	return out
}

// primarySubtag returns the primary language subtag of a language tag,
// lower-cased: "de-CH" → "de", "pt_BR" → "pt". ok is false for anything that
// is not one: the wildcard, a singleton such as "x-…", digits.
func primarySubtag(tag string) (string, bool) {
	tag = strings.TrimSpace(tag)
	if i := strings.IndexAny(tag, "-_"); i >= 0 {
		tag = tag[:i]
	}
	if len(tag) < 2 || len(tag) > 8 {
		return "", false
	}
	for _, c := range tag {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return "", false
		}
	}
	return strings.ToLower(tag), true
}
