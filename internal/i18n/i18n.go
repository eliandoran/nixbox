// Package i18n provides a minimal, dependency-free message catalog for
// nixbox's server-rendered UI. Catalogs are flat key->string JSON files
// named <locale>.json (e.g. en.json), loaded from web/i18n (embedded in
// production, read from disk in dev). Lookups fall back requested locale
// -> base language (en-US -> en) -> default locale -> the key itself, so
// an unextracted or missing string surfaces as its key rather than an
// error — a visible signal during incremental extraction.
package i18n

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

// DefaultLocale is the source-of-truth catalog every Localizer falls
// back to. en.json must define every key the UI references.
const DefaultLocale = "en"

// Bundle holds every loaded catalog, keyed by normalized locale.
type Bundle struct {
	catalogs map[string]map[string]string
}

// Load reads every <locale>.json file directly under dir in fsys into a
// Bundle. A missing dir is an error; a bundle with no catalogs is valid
// (every lookup then returns its key).
func Load(fsys fs.FS, dir string) (*Bundle, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	b := &Bundle{catalogs: make(map[string]map[string]string)}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		data, err := fs.ReadFile(fsys, dir+"/"+name)
		if err != nil {
			return nil, err
		}
		msgs := make(map[string]string)
		if err := json.Unmarshal(data, &msgs); err != nil {
			return nil, fmt.Errorf("i18n: parsing %s: %w", name, err)
		}
		b.catalogs[normalize(strings.TrimSuffix(name, ".json"))] = msgs
	}
	return b, nil
}

// Locales returns the loaded locale codes, sorted.
func (b *Bundle) Locales() []string {
	out := make([]string, 0, len(b.catalogs))
	for l := range b.catalogs {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// Name returns a locale's self-description — the "locale.name" entry of
// its own catalog (e.g. "English", "Română") — falling back to the code.
// Deliberately not Localizer-based: the fallback chain would answer with
// the default locale's name for a catalog missing the key.
func (b *Bundle) Name(locale string) string {
	if cat, ok := b.catalogs[normalize(locale)]; ok {
		if n, ok := cat["locale.name"]; ok {
			return n
		}
	}
	return locale
}

// Localizer builds a Localizer for the given preferred locales, most
// preferred first (e.g. from a cookie then Accept-Language then the
// server default). Each is matched exactly, then by base language; the
// default locale is always appended as a final fallback.
func (b *Bundle) Localizer(prefs ...string) *Localizer {
	loc := &Localizer{locale: DefaultLocale}
	seen := make(map[string]bool)
	add := func(code string) {
		code = normalize(code)
		if code == "" {
			return
		}
		cat, ok := b.catalogs[code]
		if !ok {
			if base := baseLang(code); base != code {
				cat, ok = b.catalogs[base]
				code = base
			}
		}
		if !ok || seen[code] {
			return
		}
		seen[code] = true
		if len(loc.catalogs) == 0 {
			loc.locale = code
		}
		loc.catalogs = append(loc.catalogs, cat)
	}
	for _, p := range prefs {
		add(p)
	}
	add(DefaultLocale)
	return loc
}

// Localizer resolves messages against an ordered list of catalogs. It is
// immutable once built and safe to share across a request's renders.
type Localizer struct {
	locale   string
	catalogs []map[string]string // fallback order
}

// Lang returns the primary resolved locale, e.g. for <html lang="...">.
func (l *Localizer) Lang() string { return l.locale }

// T returns the message for key, formatted with fmt.Sprintf when args
// are supplied. A key absent from every catalog returns the key itself.
func (l *Localizer) T(key string, args ...any) string {
	return l.Def(key, key, args...)
}

// Def is T with an explicit fallback instead of the key: a key absent
// from every catalog returns def. Used for strings whose English source
// of truth lives in Go (e.g. the workload-type registry), so catalogs
// only need entries for locales that override them.
func (l *Localizer) Def(key, def string, args ...any) string {
	msg := def
	for _, cat := range l.catalogs {
		if m, ok := cat[key]; ok {
			msg = m
			break
		}
	}
	if len(args) > 0 {
		return fmt.Sprintf(msg, args...)
	}
	return msg
}

// ParseAcceptLanguage returns the locales listed in an Accept-Language
// header, most-preferred first, honoring q-weights. Malformed entries
// and the "*" wildcard are skipped.
func ParseAcceptLanguage(header string) []string {
	type weighted struct {
		tag string
		q   float64
	}
	var items []weighted
	for _, part := range strings.Split(header, ",") {
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		tag = strings.TrimSpace(tag)
		if tag == "" || tag == "*" {
			continue
		}
		q := 1.0
		for _, p := range strings.Split(params, ";") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(p), "q="); ok {
				if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
					q = f
				}
			}
		}
		items = append(items, weighted{tag, q})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].q > items[j].q })
	var out []string
	for _, it := range items {
		out = append(out, it.tag)
	}
	return out
}

func normalize(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func baseLang(code string) string {
	if i := strings.IndexAny(code, "-_"); i > 0 {
		return code[:i]
	}
	return code
}
