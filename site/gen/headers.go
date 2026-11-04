package main

import (
	"bufio"
	"strings"
)

// csp is the Content-Security-Policy of every page (site/DESIGN.md §2.2).
const csp = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; " +
	"media-src 'self'; connect-src 'none'; manifest-src 'self'; base-uri 'none'; form-action 'none'; " +
	"frame-ancestors 'none'; object-src 'none'; require-trusted-types-for 'script'; trusted-types 'none'; " +
	"upgrade-insecure-requests"

const permissionsPolicy = "camera=(), microphone=(), geolocation=(), payment=(), usb=(), serial=(), hid=(), " +
	"display-capture=(), browsing-topics=(), autoplay=(self), fullscreen=(self), clipboard-write=(self)"

// Headers renders the Cloudflare Pages _headers file for a build mode.
func Headers(mode string) string {
	var b strings.Builder
	b.WriteString("/*\n")
	b.WriteString("  Content-Security-Policy: " + csp + "\n")
	b.WriteString("  Strict-Transport-Security: max-age=31536000; includeSubDomains\n")
	b.WriteString("  X-Content-Type-Options: nosniff\n")
	b.WriteString("  X-Frame-Options: DENY\n")
	b.WriteString("  Referrer-Policy: no-referrer\n")
	b.WriteString("  Permissions-Policy: " + permissionsPolicy + "\n")
	b.WriteString("  Cross-Origin-Opener-Policy: same-origin\n")
	b.WriteString("  Cross-Origin-Resource-Policy: same-origin\n")
	b.WriteString("  ! Access-Control-Allow-Origin\n")
	if mode != ModeRelease {
		b.WriteString("  X-Robots-Tag: noindex, nofollow\n")
	}
	b.WriteString("\n/assets/*\n")
	b.WriteString("  Cache-Control: public, max-age=31536000, immutable\n")
	b.WriteString("\nhttps://:project.pages.dev/*\n")
	b.WriteString("  X-Robots-Tag: noindex\n")
	b.WriteString("\nhttps://:version.:project.pages.dev/*\n")
	b.WriteString("  X-Robots-Tag: noindex\n")
	if mode != ModeRelease {
		b.WriteString("\n/prelaunch/*\n")
		b.WriteString("  Content-Disposition: attachment\n")
	}
	return b.String()
}

// headerRule is one block of a _headers file.
type headerRule struct {
	pattern string // path pattern, or absolute URL pattern
	set     [][2]string
	remove  []string
}

// parseHeaders reads the subset of the Pages _headers syntax that Headers
// produces: a pattern line, then indented "Name: value" or "! Name" lines.
func parseHeaders(text string) []headerRule {
	var rules []headerRule
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if line[0] != ' ' && line[0] != '\t' {
			rules = append(rules, headerRule{pattern: trimmed})
			continue
		}
		if len(rules) == 0 {
			continue
		}
		r := &rules[len(rules)-1]
		if strings.HasPrefix(trimmed, "!") {
			r.remove = append(r.remove, strings.TrimSpace(trimmed[1:]))
			continue
		}
		name, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		r.set = append(r.set, [2]string{strings.TrimSpace(name), strings.TrimSpace(value)})
	}
	return rules
}

// matchPattern reports whether a request (host and path) matches a _headers
// pattern. "*" is a splat over any characters; ":name" matches one label of
// a host or one segment of a path. Absolute patterns must match the host.
func matchPattern(pattern, host, urlPath string) bool {
	target := urlPath
	if rest, ok := strings.CutPrefix(pattern, "https://"); ok {
		pattern = rest
		target = host + urlPath
	} else if strings.Contains(pattern, "://") {
		return false
	}
	return globMatch(pattern, target)
}

func globMatch(p, s string) bool {
	for len(p) > 0 {
		switch p[0] {
		case '*':
			rest := p[1:]
			for i := len(s); i >= 0; i-- {
				if globMatch(rest, s[i:]) {
					return true
				}
			}
			return false
		case ':':
			j := 1
			for j < len(p) && isNameChar(p[j]) {
				j++
			}
			k := 0
			for k < len(s) && s[k] != '.' && s[k] != '/' {
				k++
			}
			if k == 0 {
				return false
			}
			p, s = p[j:], s[k:]
		default:
			if len(s) == 0 || s[0] != p[0] {
				return false
			}
			p, s = p[1:], s[1:]
		}
	}
	return len(s) == 0
}

func isNameChar(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
