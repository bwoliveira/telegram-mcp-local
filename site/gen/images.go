package main

import (
	"fmt"
	"html/template"
	"os"
	"path/filepath"
)

// The social images (og-<lang>.png, 1200x630) and the apple-touch icon
// (180x180) are rendered from these HTML sources by site/tools/render-images.sh
// with headless Chromium, then committed under static/assets/og/ and static/.
// The HTML is a build-time tool input only; it is never shipped in dist/.

const ogTemplate = `<!doctype html>
<html lang="{{.Lang}}"><head><meta charset="utf-8"><style>
@font-face{font-family:"AHN";src:url("{{.FontURL}}") format("woff2");font-weight:200 800}
html,body{margin:0;width:1200px;height:630px;overflow:hidden}
body{background:#0c1f4d;color:#eef2fb;font-family:"AHN",sans-serif;display:flex;flex-direction:column;justify-content:space-between;padding:72px 80px;box-sizing:border-box}
.row{display:flex;align-items:center;gap:28px}
svg{width:132px;height:132px}
.q{font-size:30px;font-weight:700;letter-spacing:.08em;text-transform:uppercase;color:#b8c5e6}
.q-after{margin-top:14px}
.n{font-size:92px;font-weight:780;line-height:1;letter-spacing:-.01em}
.t{font-size:44px;font-weight:600;line-height:1.25;max-width:1000px;margin:0}
.f{font-size:26px;color:#b8c5e6;margin:0}
.bar{position:absolute;left:0;bottom:0;width:1200px;height:14px;background:#2346c8}
</style></head><body>
<div class="row">{{.Mark}}<div>{{if .QualifierFirst}}<div class="q">{{.Qualifier}}</div><div class="n">{{.Brand}}</div>{{else}}<div class="n">{{.Brand}}</div><div class="q q-after">{{.Qualifier}}</div>{{end}}</div></div>
<p class="t">{{.Tagline}}</p>
<p class="f">{{.Notice}}</p>
<div class="bar"></div>
</body></html>`

const iconTemplate = `<!doctype html>
<html><head><meta charset="utf-8"><style>
html,body{margin:0;width:180px;height:180px;overflow:hidden;background:#0c1f4d}
body{display:grid;place-items:center}
svg{width:136px;height:136px}
</style></head><body>{{.Mark}}</body></html>`

// markSVG mirrors the mark in layout/base.html, with explicit colors.
const markSVG = `<svg viewBox="0 0 64 64" xmlns="http://www.w3.org/2000/svg"><path fill="#8fb4ff" d="M15 9h26a11 11 0 0 1 11 11v13a11 11 0 0 1-11 11H27l-11 9v-9.4A11 11 0 0 1 4 33V20A11 11 0 0 1 15 9z"/><path fill="none" stroke="#0c1f4d" stroke-width="4" stroke-linecap="round" stroke-linejoin="round" d="m22 20.5-6.5 6 6.5 6m12-12 6.5 6-6.5 6"/><path fill="none" stroke="#8fb4ff" stroke-width="4" stroke-linecap="round" d="M52 26.5h3.5"/><circle cx="58" cy="26.5" r="4" fill="#eef2fb"/></svg>`

// WriteImageSources writes og-<lang>.html and apple-touch-icon.html into dir.
func WriteImageSources(src, dir string) error {
	var site Site
	if err := readJSON(filepath.Join(src, "site.json"), &site); err != nil {
		return err
	}
	strs, err := loadStrings(src, site)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	font, err := filepath.Abs(filepath.Join(src, "static", "assets", "fonts", "atkinson-hyperlegible-next.woff2"))
	if err != nil {
		return err
	}
	og := template.Must(template.New("og").Parse(ogTemplate))
	for _, l := range site.Languages {
		s := strs[l.Code]
		f, err := os.Create(filepath.Join(dir, "og-"+l.Code+".html"))
		if err != nil {
			return err
		}
		err = og.Execute(f, map[string]any{
			"Lang": l.Code, "FontURL": template.URL("file://" + font), "Mark": template.HTML(markSVG),
			"Qualifier": s["brand.qualifier"], "QualifierFirst": s["brand.order"] == "qualifier-first", "Brand": site.Brand, "Tagline": s["og.tagline"], "Notice": s["og.notice"],
		})
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("og-%s: %w", l.Code, err)
		}
	}
	icon := template.Must(template.New("icon").Parse(iconTemplate))
	f, err := os.Create(filepath.Join(dir, "apple-touch-icon.html"))
	if err != nil {
		return err
	}
	defer f.Close()
	return icon.Execute(f, map[string]any{"Mark": template.HTML(markSVG)})
}
