package i18n

import (
	"reflect"
	"testing"
	"testing/fstest"
)

func testBundle(t *testing.T) *Bundle {
	t.Helper()
	fsys := fstest.MapFS{
		"i18n/en.json": {Data: []byte(`{"greet":"Hello","count":"%d items"}`)},
		"i18n/de.json": {Data: []byte(`{"greet":"Hallo"}`)},
		"i18n/readme":  {Data: []byte("ignored, not .json")},
	}
	b, err := Load(fsys, "i18n")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return b
}

func TestLoadLocales(t *testing.T) {
	b := testBundle(t)
	if got, want := b.Locales(), []string{"de", "en"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Locales() = %v, want %v", got, want)
	}
}

func TestLoadMissingDir(t *testing.T) {
	if _, err := Load(fstest.MapFS{}, "i18n"); err == nil {
		t.Fatal("Load of missing dir: want error, got nil")
	}
}

func TestTranslateAndFallback(t *testing.T) {
	b := testBundle(t)
	tests := []struct {
		name string
		loc  *Localizer
		key  string
		want string
	}{
		{"exact locale", b.Localizer("de"), "greet", "Hallo"},
		{"base language match", b.Localizer("de-AT"), "greet", "Hallo"},
		{"missing key falls back to default catalog", b.Localizer("de"), "count", "%d items"},
		{"unknown key returns key", b.Localizer("de"), "nope", "nope"},
		{"unknown locale uses default", b.Localizer("fr"), "greet", "Hello"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.loc.T(tt.key); got != tt.want {
				t.Errorf("T(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestLang(t *testing.T) {
	b := testBundle(t)
	if got := b.Localizer("de-AT", "en").Lang(); got != "de" {
		t.Errorf("Lang() = %q, want de", got)
	}
	if got := b.Localizer("fr").Lang(); got != "en" {
		t.Errorf("Lang() for unknown = %q, want en (default)", got)
	}
}

func TestTranslateArgs(t *testing.T) {
	b := testBundle(t)
	if got := b.Localizer("en").T("count", 3); got != "3 items" {
		t.Errorf("T with args = %q, want %q", got, "3 items")
	}
}

func TestName(t *testing.T) {
	fsys := fstest.MapFS{
		"i18n/en.json": {Data: []byte(`{"locale.name":"English"}`)},
		"i18n/de.json": {Data: []byte(`{}`)},
	}
	b, err := Load(fsys, "i18n")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := b.Name("en"); got != "English" {
		t.Errorf("Name(en) = %q, want English", got)
	}
	if got := b.Name("de"); got != "de" {
		t.Errorf("Name(de) = %q, want code fallback de (not English)", got)
	}
	if got := b.Name("xx"); got != "xx" {
		t.Errorf("Name(xx) = %q, want xx", got)
	}
}

func TestDef(t *testing.T) {
	b := testBundle(t)
	if got := b.Localizer("de").Def("greet", "fallback"); got != "Hallo" {
		t.Errorf("Def with present key = %q, want Hallo", got)
	}
	if got := b.Localizer("de").Def("nope", "fallback %d", 7); got != "fallback 7" {
		t.Errorf("Def with missing key = %q, want %q", got, "fallback 7")
	}
}

func TestParseAcceptLanguage(t *testing.T) {
	tests := []struct {
		header string
		want   []string
	}{
		{"", nil},
		{"de", []string{"de"}},
		{"de-DE,de;q=0.9,en;q=0.8", []string{"de-DE", "de", "en"}},
		{"en;q=0.5, de;q=0.9", []string{"de", "en"}}, // q reorders
		{"*, en", []string{"en"}},                    // wildcard skipped
	}
	for _, tt := range tests {
		if got := ParseAcceptLanguage(tt.header); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("ParseAcceptLanguage(%q) = %v, want %v", tt.header, got, tt.want)
		}
	}
}
