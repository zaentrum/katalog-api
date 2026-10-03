package http

import (
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestBiographyLanguages(t *testing.T) {
	for _, c := range []struct {
		query, header string
		want          []string
	}{
		// ?lang= alone when it names a language; its primary subtag.
		{"?lang=de-CH", "fr", []string{"de"}},
		{"?lang=PT_br", "", []string{"pt"}},
		// Not a language: the header decides.
		{"?lang=*", "fr", []string{"fr"}},
		{"?lang=1", "fr", []string{"fr"}},
		{"?lang=", "fr", []string{"fr"}},
		// The header's languages by quality, ties in order, each once.
		{"", "de-CH,de;q=0.9,en-US;q=0.8,en;q=0.7", []string{"de", "en"}},
		{"", "en;q=0.5, fr-CH, de;q=0.9", []string{"fr", "de", "en"}},
		{"", "it;q=0.8, rm;q=0.8", []string{"it", "rm"}},
		// The wildcard, q=0, a bad q and malformed ranges count for nothing.
		{"", "*, es;q=0, x-pig-latin, ja;q=oops, ko;q=2, nl ; Q=0.4", []string{"nl"}},
		{"", "", nil},
		{"", "   ,  ;q=1", nil},
	} {
		r := httptest.NewRequest("GET", "/api/v1/people/p1"+c.query, nil)
		if c.header != "" {
			r.Header.Set("Accept-Language", c.header)
		}
		if got := biographyLanguages(r); !reflect.DeepEqual(got, c.want) {
			t.Errorf("lang %q, Accept-Language %q: %q, want %q", c.query, c.header, got, c.want)
		}
	}
}
