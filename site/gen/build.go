package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Build modes. Pre-launch is the default and the only mode allowed before
// Stage 6: downloads are test files and every page is noindex.
const (
	ModePrelaunch = "prelaunch"
	ModeRelease   = "release"
)

// prelaunchVersion fills {version} in asset names before a release exists.
const prelaunchVersion = "0.0.0"

// Options configures one build.
type Options struct {
	Src     string
	Out     string
	Mode    string
	Version string
	Date    string
	BaseURL string

	// allowTODO lets tests build release mode from sources that still carry
	// TODO( markers. It is never set from the command line.
	allowTODO bool
}

// Result summarises a build.
type Result struct {
	Pages int
	Files []string // output paths relative to Out, slash-separated, sorted
}

// Site is site/src/site.json.
type Site struct {
	Brand        string   `json:"brand"`
	BaseURL      string   `json:"baseURL"`
	Repo         string   `json:"repo"`
	PagesProject string   `json:"pagesProject"`
	ThemeColor   string   `json:"themeColor"`
	XDefault     string   `json:"xDefault"`
	Languages    []Lang   `json:"languages"`
	Pages        []string `json:"pages"`
}

// Lang is one site language.
type Lang struct {
	Code     string `json:"code"`
	Prefix   string `json:"prefix"`
	OGLocale string `json:"ogLocale"`
	Name     string `json:"name"`
}

// ReleaseSpec is site/src/release.json.
type ReleaseSpec struct {
	Version string `json:"version"`
	Date    string `json:"date"`
	Groups  []struct {
		ID    string `json:"id"`
		Files []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
			Name  string `json:"name"`
		} `json:"files"`
	} `json:"groups"`
}

// DLFile is one download, resolved for the build mode.
type DLFile struct {
	ID, Label, Name, Href string
}

// DLGroup is one card on /download/.
type DLGroup struct {
	ID    string
	Files []DLFile
}

// ReleaseView is what templates see of the release.
type ReleaseView struct {
	Version string // with leading v; empty before launch
	Date    string
	Groups  []DLGroup
}

// Alt is one language version of the current page.
type Alt struct {
	Lang    Lang
	Path    string
	URL     string
	Current bool
}

// PageData is the template context of one page.
type PageData struct {
	Site      Site
	Lang      Lang
	Page      string
	Path      string // site-absolute path, e.g. /en/download/
	URL       string // absolute canonical URL
	Prelaunch bool
	Release   ReleaseView
	Alts      []Alt  // same page in every language, in site.json order
	XDefault  string // absolute URL of the x-default version
	OGImage   string // absolute URL

	strings map[string]map[string]string
	assets  map[string]string
}

// T returns a UI string of the page language; a missing key fails the build.
func (p PageData) T(key string) (string, error) {
	return p.TL(p.Lang.Code, key)
}

// TL returns a UI string of another language (used by the language hint).
func (p PageData) TL(lang, key string) (string, error) {
	s, ok := p.strings[lang][key]
	if !ok {
		return "", fmt.Errorf("missing string %q for %s", key, lang)
	}
	return s, nil
}

// Asset returns the fingerprinted path of a file under static/assets.
func (p PageData) Asset(name string) (string, error) {
	h, ok := p.assets[name]
	if !ok {
		return "", fmt.Errorf("unknown asset %q", name)
	}
	return h, nil
}

// Href returns the path of another page in the current language.
func (p PageData) Href(page string) string { return pagePath(p.Lang, page) }

// Group returns one download card.
func (p PageData) Group(id string) (DLGroup, error) {
	for _, g := range p.Release.Groups {
		if g.ID == id {
			return g, nil
		}
	}
	return DLGroup{}, fmt.Errorf("unknown download group %q", id)
}

// OtherAlts are the language versions other than the current one.
func (p PageData) OtherAlts() []Alt {
	var out []Alt
	for _, a := range p.Alts {
		if !a.Current {
			out = append(out, a)
		}
	}
	return out
}

// Title is the document title: the qualified brand first, then the page
// title. The brand always carries the localized "unofficial" qualifier
// (Telegram API Terms §2.3): before the name in English, after it in pt-BR
// and es, following each language's natural order (site/DESIGN.md §0).
func (p PageData) Title() (string, error) {
	n, err := p.SiteName()
	if err != nil {
		return "", err
	}
	t, err := p.T("meta." + p.Page + ".title")
	if err != nil {
		return "", err
	}
	return n + " · " + t, nil
}

// SiteName is the qualified brand used in titles, og:site_name and the
// social images, for example "Telegram MCP (não oficial)".
func (p PageData) SiteName() (string, error) { return p.T("brand.title") }

// BrandName is the qualified brand used in running text and headings, for
// example "Telegram MCP não oficial".
func (p PageData) BrandName() (string, error) { return p.T("brand.name") }

// QualifierFirst reports whether the qualifier precedes the brand in this
// language (English) or follows it (pt-BR, es).
func (p PageData) QualifierFirst() (bool, error) {
	o, err := p.T("brand.order")
	if err != nil {
		return false, err
	}
	switch o {
	case "qualifier-first":
		return true, nil
	case "name-first":
		return false, nil
	}
	return false, fmt.Errorf("brand.order %q for %s: want qualifier-first or name-first", o, p.Lang.Code)
}

// LinkArgs and CmdArgs carry the page context into partials that need a
// second value (html/template passes a single pipeline to a template).
type LinkArgs struct {
	P    PageData
	File DLFile
}

type CmdArgs struct {
	P    PageData
	Text string
}

var funcs = template.FuncMap{
	"args": func(p PageData, f DLFile) LinkArgs { return LinkArgs{P: p, File: f} },
	"cmd":  func(p PageData, text string) CmdArgs { return CmdArgs{P: p, Text: strings.TrimSpace(text)} },
}

func pagePath(l Lang, page string) string {
	switch page {
	case "index":
		return "/" + l.Prefix
	case "404":
		return "/" + l.Prefix + "404.html"
	default:
		return "/" + l.Prefix + page + "/"
	}
}

func outFile(l Lang, page string) string {
	switch page {
	case "index":
		return l.Prefix + "index.html"
	case "404":
		return l.Prefix + "404.html"
	default:
		return l.Prefix + page + "/index.html"
	}
}

func readJSON(file string, v any) error {
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	return nil
}

var versionRE = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)

// Build renders the whole site into opts.Out, replacing its contents.
func Build(opts Options) (Result, error) {
	var res Result
	if opts.Mode == "" {
		opts.Mode = ModePrelaunch
	}
	if opts.Mode != ModePrelaunch && opts.Mode != ModeRelease {
		return res, fmt.Errorf("unknown mode %q", opts.Mode)
	}
	release := opts.Mode == ModeRelease
	if !release && (opts.Version != "" || opts.Date != "") {
		return res, errors.New("-version and -date are only valid with -mode release")
	}

	var site Site
	if err := readJSON(filepath.Join(opts.Src, "site.json"), &site); err != nil {
		return res, err
	}
	if opts.BaseURL != "" {
		site.BaseURL = opts.BaseURL
	}
	site.BaseURL = strings.TrimRight(site.BaseURL, "/")
	if !strings.HasPrefix(site.BaseURL, "https://") {
		return res, fmt.Errorf("baseURL must be https: %q", site.BaseURL)
	}
	var spec ReleaseSpec
	if err := readJSON(filepath.Join(opts.Src, "release.json"), &spec); err != nil {
		return res, err
	}
	if release {
		// Flags override release.json; pre-launch builds ignore both.
		if opts.Version == "" {
			opts.Version = spec.Version
		}
		if opts.Date == "" {
			opts.Date = spec.Date
		}
		if !versionRE.MatchString(opts.Version) {
			return res, errors.New("release mode needs a version vX.Y.Z (-version or release.json)")
		}
		if opts.Date != "" {
			if _, err := time.Parse("2006-01-02", opts.Date); err != nil {
				return res, fmt.Errorf("release date: %w", err)
			}
		}
	}

	strs, err := loadStrings(opts.Src, site)
	if err != nil {
		return res, err
	}
	if err := checkBrand(site, strs); err != nil {
		return res, err
	}
	if release && !opts.allowTODO {
		if err := refuseTODO(opts.Src, strs); err != nil {
			return res, err
		}
	}

	if err := os.RemoveAll(opts.Out); err != nil {
		return res, err
	}
	if err := os.MkdirAll(opts.Out, 0o755); err != nil {
		return res, err
	}
	w := &writer{root: opts.Out}

	assets, err := copyStatic(filepath.Join(opts.Src, "static"), w)
	if err != nil {
		return res, err
	}

	rv := ReleaseView{}
	ver := prelaunchVersion
	if release {
		rv.Version, rv.Date = opts.Version, opts.Date
		ver = strings.TrimPrefix(opts.Version, "v")
	}
	var prelaunchFiles []string
	for _, g := range spec.Groups {
		dg := DLGroup{ID: g.ID}
		for _, f := range g.Files {
			name := strings.ReplaceAll(f.Name, "{version}", ver)
			href := "/prelaunch/" + name + ".txt"
			if release {
				href = "https://github.com/" + site.Repo + "/releases/download/" + opts.Version + "/" + name
			} else {
				prelaunchFiles = append(prelaunchFiles, name)
			}
			dg.Files = append(dg.Files, DLFile{ID: f.ID, Label: f.Label, Name: name, Href: href})
		}
		rv.Groups = append(rv.Groups, dg)
	}

	layout, err := template.New("").Funcs(funcs).ParseGlob(filepath.Join(opts.Src, "layout", "*.html"))
	if err != nil {
		return res, err
	}

	pages := append(append([]string{}, site.Pages...), "404")
	for _, l := range site.Languages {
		shared, err := os.ReadFile(filepath.Join(opts.Src, "pages", l.Code, "_shared.html"))
		if err != nil {
			return res, err
		}
		for _, page := range pages {
			src, err := os.ReadFile(filepath.Join(opts.Src, "pages", l.Code, page+".html"))
			if err != nil {
				return res, err
			}
			t, err := layout.Clone()
			if err != nil {
				return res, err
			}
			if _, err := t.New("shared").Parse(string(shared)); err != nil {
				return res, fmt.Errorf("%s/_shared.html: %w", l.Code, err)
			}
			if _, err := t.New("page").Parse(string(src)); err != nil {
				return res, fmt.Errorf("%s/%s.html: %w", l.Code, page, err)
			}
			d := PageData{
				Site: site, Lang: l, Page: page, Prelaunch: !release, Release: rv,
				strings: strs, assets: assets,
			}
			d.Path = pagePath(l, page)
			d.URL = site.BaseURL + d.Path
			for _, ol := range site.Languages {
				p := pagePath(ol, page)
				if page == "404" {
					// A 404 page has no URL of its own: switch to the home page.
					p = pagePath(ol, "index")
				}
				d.Alts = append(d.Alts, Alt{Lang: ol, Path: p, URL: site.BaseURL + p, Current: ol.Code == l.Code})
				if ol.Code == site.XDefault {
					d.XDefault = site.BaseURL + p
				}
			}
			og, ok := assets["og/og-"+l.Code+".png"]
			if !ok {
				return res, fmt.Errorf("missing og/og-%s.png", l.Code)
			}
			d.OGImage = site.BaseURL + og
			var buf bytes.Buffer
			if err := t.ExecuteTemplate(&buf, "base", d); err != nil {
				return res, fmt.Errorf("%s/%s: %w", l.Code, page, err)
			}
			if err := w.write(outFile(l, page), buf.Bytes()); err != nil {
				return res, err
			}
			res.Pages++
		}
	}

	if err := w.write("_headers", []byte(Headers(opts.Mode))); err != nil {
		return res, err
	}
	if release {
		if err := w.write("robots.txt", []byte("User-agent: *\nAllow: /\n\nSitemap: "+site.BaseURL+"/sitemap.xml\n")); err != nil {
			return res, err
		}
		if err := w.write("sitemap.xml", sitemap(site)); err != nil {
			return res, err
		}
	} else {
		if err := w.write("robots.txt", []byte("User-agent: *\nDisallow: /\n")); err != nil {
			return res, err
		}
		for _, name := range prelaunchFiles {
			body := "Test file for the Unofficial " + site.Brand + " pre-launch preview. This is not an installer.\n" +
				"It stands in for: " + name + "\n"
			if err := w.write("prelaunch/"+name+".txt", []byte(body)); err != nil {
				return res, err
			}
		}
	}

	sort.Strings(w.files)
	res.Files = w.files
	return res, nil
}

// loadStrings reads every language file and requires identical key sets.
func loadStrings(src string, site Site) (map[string]map[string]string, error) {
	all := map[string]map[string]string{}
	var ref []string
	for i, l := range site.Languages {
		m := map[string]string{}
		if err := readJSON(filepath.Join(src, "strings", l.Code+".json"), &m); err != nil {
			return nil, err
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if i == 0 {
			ref = keys
		} else if strings.Join(keys, "\n") != strings.Join(ref, "\n") {
			return nil, fmt.Errorf("strings/%s.json keys differ from strings/%s.json", l.Code, site.Languages[0].Code)
		}
		all[l.Code] = m
	}
	return all, nil
}

// checkBrand enforces the brand rule of site/DESIGN.md §0 on the strings:
// brand.name and brand.title always contain the brand and the qualifier, in
// the order that brand.order declares (qualifier first only in English).
func checkBrand(site Site, strs map[string]map[string]string) error {
	for _, l := range site.Languages {
		s := strs[l.Code]
		q := strings.ToLower(s["brand.qualifier"])
		if q == "" {
			return fmt.Errorf("strings/%s.json: empty brand.qualifier", l.Code)
		}
		for _, key := range []string{"brand.name", "brand.title"} {
			v := strings.ToLower(s[key])
			bi, qi := strings.Index(v, strings.ToLower(site.Brand)), strings.Index(v, q)
			if bi < 0 || qi < 0 || strings.Count(v, strings.ToLower(site.Brand)) != 1 {
				return fmt.Errorf("strings/%s.json: %s %q must contain %q and the qualifier %q once", l.Code, key, s[key], site.Brand, s["brand.qualifier"])
			}
			switch s["brand.order"] {
			case "qualifier-first":
				if qi > bi {
					return fmt.Errorf("strings/%s.json: %s %q must put the qualifier first", l.Code, key, s[key])
				}
			case "name-first":
				if qi < bi {
					return fmt.Errorf("strings/%s.json: %s %q must put the brand first", l.Code, key, s[key])
				}
			default:
				return fmt.Errorf("strings/%s.json: brand.order %q: want qualifier-first or name-first", l.Code, s["brand.order"])
			}
		}
	}
	return nil
}

// refuseTODO blocks release builds while any TODO( marker remains in the
// strings or page sources (site/DESIGN.md §6.1).
func refuseTODO(src string, strs map[string]map[string]string) error {
	var found []string
	for lang, m := range strs {
		for k, v := range m {
			if strings.Contains(v, "TODO(") {
				found = append(found, "strings/"+lang+".json:"+k)
			}
		}
	}
	err := filepath.WalkDir(filepath.Join(src, "pages"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if bytes.Contains(b, []byte("TODO(")) {
			found = append(found, p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(found) > 0 {
		sort.Strings(found)
		return fmt.Errorf("release mode refused: TODO( markers remain in %s", strings.Join(found, ", "))
	}
	return nil
}

func sitemap(site Site) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:xhtml="http://www.w3.org/1999/xhtml">` + "\n")
	for _, page := range site.Pages {
		for _, l := range site.Languages {
			fmt.Fprintf(&b, "  <url>\n    <loc>%s%s</loc>\n", site.BaseURL, pagePath(l, page))
			for _, ol := range site.Languages {
				fmt.Fprintf(&b, "    <xhtml:link rel=\"alternate\" hreflang=\"%s\" href=\"%s%s\"/>\n", ol.Code, site.BaseURL, pagePath(ol, page))
			}
			for _, ol := range site.Languages {
				if ol.Code == site.XDefault {
					fmt.Fprintf(&b, "    <xhtml:link rel=\"alternate\" hreflang=\"x-default\" href=\"%s%s\"/>\n", site.BaseURL, pagePath(ol, page))
				}
			}
			b.WriteString("  </url>\n")
		}
	}
	b.WriteString("</urlset>\n")
	return []byte(b.String())
}

// writer writes files below root and records their relative paths.
type writer struct {
	root  string
	files []string
}

func (w *writer) write(rel string, data []byte) error {
	rel = path.Clean(rel)
	if strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
		return fmt.Errorf("refusing to write outside the output: %s", rel)
	}
	full := filepath.Join(w.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return err
	}
	w.files = append(w.files, rel)
	return nil
}
