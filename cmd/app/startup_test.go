package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestStartupDocumentRetriesUntilHandlerReady(t *testing.T) {
	var handler swapHandler
	request := httptest.NewRequest(http.MethodGet, "/conta?startup=fixture", nil)
	for range 2 {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("waiting status = %d", response.Code)
		}
		if !strings.HasPrefix(response.Header().Get("Content-Type"), "text/html") {
			t.Errorf("native startup document must be HTML, got %q", response.Header().Get("Content-Type"))
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Error("waiting document must not be cached across handler replacement")
		}
		document, err := html.Parse(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !hasStartupRefresh(document) {
			t.Error("waiting document must retry its current URL without user input")
		}
	}
	var seenURI string
	handler.set(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenURI = r.URL.RequestURI()
		_, _ = w.Write([]byte("mounted credential form or panel"))
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if seenURI != "/conta?startup=fixture" || response.Body.String() != "mounted credential form or panel" {
		t.Fatalf("retry did not reach the mounted handler at the original URL: %q %q", seenURI, response.Body.String())
	}
	if hasStartupRefresh(mustParseHTML(t, response.Body.String())) {
		t.Fatal("ready content must stop the startup retry")
	}
}

func TestStartupFailureStopsWaitingRefresh(t *testing.T) {
	var handler swapHandler
	handler.set(startFailure(errors.New("fixture startup failure"), "fixture.log"))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(response.Body.String(), "fixture startup failure") || hasStartupRefresh(mustParseHTML(t, response.Body.String())) {
		t.Fatal("a terminal failure must replace the waiting document without retrying")
	}
}

func mustParseHTML(t *testing.T, text string) *html.Node {
	t.Helper()
	node, err := html.Parse(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	return node
}

func hasStartupRefresh(node *html.Node) bool {
	if node.Type == html.ElementNode && node.Data == "meta" {
		var refresh, currentURL bool
		for _, attr := range node.Attr {
			refresh = refresh || attr.Key == "http-equiv" && strings.EqualFold(attr.Val, "refresh")
			// No URL override: preserve the requested path and query.
			currentURL = currentURL || attr.Key == "content" && attr.Val == "1"
		}
		if refresh && currentURL {
			return true
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if hasStartupRefresh(child) {
			return true
		}
	}
	return false
}
