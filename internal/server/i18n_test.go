package server

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/elian/nixbox/web"
)

// Message keys referenced from Go by means other than the s.t(r, "...")
// pattern the scan below catches.
var extraUsedKeys = map[string]bool{
	"locale.name": true, // read directly by i18n.Bundle.Name for the language picker
}

var (
	reTemplateT   = regexp.MustCompile(`\{\{T "([^"]+)"`)          // {{T "key"}}
	reTemplatePfx = regexp.MustCompile(`T \(printf "([^"%]+)%`)    // {{T (printf "prefix%s" ...)}}
	reTDefPfx     = regexp.MustCompile(`TDef \(printf "([^"%]+)%`) // {{TDef (printf "prefix%s" ...) fallback}}
	reGoT         = regexp.MustCompile(`s\.t\(r, "([^"]+)"`)       // s.t(r, "key", ...)
)

// TestCatalogCoverage keeps the catalogs and the UI in lockstep:
//   - every key the templates or handlers reference must exist in en.json
//     (the source-of-truth catalog — a miss would render the raw key);
//   - every dynamically built T key prefix (e.g. "state.%s") must have at
//     least one en.json entry, as a typo canary;
//   - en.json must not accumulate dead keys;
//   - other locales may only override known keys (en keys, TDef-prefixed
//     registry overrides, or the explicit extras), so a typo in a
//     translation key fails here instead of silently never matching.
func TestCatalogCoverage(t *testing.T) {
	catalogs := loadTestCatalogs(t)
	en, ok := catalogs["en"]
	if !ok {
		t.Fatal("web/i18n/en.json missing")
	}

	usedKeys, tPrefixes, tdefPrefixes := scanUsage(t)

	// 1. Every referenced key resolves in en.json.
	for key := range usedKeys {
		if _, ok := en[key]; !ok {
			t.Errorf("key %q is used in the UI but missing from en.json", key)
		}
	}

	// 2. Each dynamic T prefix has at least one entry (all its runtime
	// values should, but the members are not statically known — one match
	// still catches renamed/typoed prefixes).
	for pfx := range tPrefixes {
		found := false
		for key := range en {
			if strings.HasPrefix(key, pfx) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("dynamic key prefix %q has no entries in en.json", pfx)
		}
	}

	// 3. No dead keys in en.json.
	for key := range en {
		if !keyAccounted(key, usedKeys, tPrefixes, tdefPrefixes) {
			t.Errorf("en.json key %q is never used by the UI", key)
		}
	}

	// 4. Other locales only override known keys.
	for locale, cat := range catalogs {
		if locale == "en" {
			continue
		}
		for key := range cat {
			if _, inEN := en[key]; inEN {
				continue
			}
			if !keyAccounted(key, usedKeys, tPrefixes, tdefPrefixes) {
				t.Errorf("%s.json key %q matches nothing the UI uses (typo?)", locale, key)
			}
		}
	}
}

func keyAccounted(key string, used map[string]bool, prefixSets ...map[string]bool) bool {
	if used[key] || extraUsedKeys[key] {
		return true
	}
	for _, set := range prefixSets {
		for pfx := range set {
			if strings.HasPrefix(key, pfx) {
				return true
			}
		}
	}
	return false
}

// scanUsage collects the message keys referenced by the embedded
// templates and this package's Go sources (globbed from the package
// directory, where `go test` runs).
func scanUsage(t *testing.T) (used, tPrefixes, tdefPrefixes map[string]bool) {
	t.Helper()
	used = map[string]bool{}
	tPrefixes = map[string]bool{}
	tdefPrefixes = map[string]bool{}

	collect := func(src string) {
		for _, m := range reTemplateT.FindAllStringSubmatch(src, -1) {
			used[m[1]] = true
		}
		for _, m := range reTemplatePfx.FindAllStringSubmatch(src, -1) {
			tPrefixes[m[1]] = true
		}
		for _, m := range reTDefPfx.FindAllStringSubmatch(src, -1) {
			tdefPrefixes[m[1]] = true
		}
		for _, m := range reGoT.FindAllStringSubmatch(src, -1) {
			used[m[1]] = true
		}
	}

	tmpls, err := fs.Glob(web.FS, "templates/*.html")
	if err != nil || len(tmpls) == 0 {
		t.Fatalf("globbing embedded templates: %v (%d found)", err, len(tmpls))
	}
	for _, name := range tmpls {
		b, err := fs.ReadFile(web.FS, name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		collect(string(b))
	}

	srcs, err := filepath.Glob("*.go")
	if err != nil || len(srcs) == 0 {
		t.Fatalf("globbing package sources: %v (%d found)", err, len(srcs))
	}
	for _, name := range srcs {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		collect(string(b))
	}
	return used, tPrefixes, tdefPrefixes
}

func loadTestCatalogs(t *testing.T) map[string]map[string]string {
	t.Helper()
	entries, err := fs.ReadDir(web.FS, "i18n")
	if err != nil {
		t.Fatalf("reading embedded i18n dir: %v", err)
	}
	catalogs := map[string]map[string]string{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := fs.ReadFile(web.FS, "i18n/"+e.Name())
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		var m map[string]string
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("parsing %s: %v", e.Name(), err)
		}
		catalogs[strings.TrimSuffix(e.Name(), ".json")] = m
	}
	return catalogs
}
