package markdown_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	markdown "github.com/Elagoht/collage-markdown"
	"github.com/Elagoht/collage/pkg/collage"
	"github.com/Elagoht/collage/pkg/collagetest"
)

const hello = `---
title: Hello, world
description: The first post
date: 2026-09-20
updated: 2026-09-21T10:30:00Z
tags: [go, collage]
author: Ada
---

Some *prose* with ~~old~~ words, a footnote[^1] and https://example.com.

## Önemli Başlık

| a | b |
|---|---|
| 1 | 2 |

- [x] done
- [ ] todo

## Önemli Başlık

<script>alert(1)</script>

` + "```go\nfmt.Println(\"<hi>\")\n```" + `

[^1]: The note.
`

func content() fstest.MapFS {
	return fstest.MapFS{
		"content/blog/hello.md": {Data: []byte(hello)},
		"content/blog/second.md": {Data: []byte(`---
date: 2026-09-22
tags: one, two
---

# Second post

Body of the second.
`)},
		"content/blog/undated.md":    {Data: []byte("# Undated\n\nNo date.\n")},
		"content/blog/draft.md":      {Data: []byte("---\ntitle: Secret\ndraft: true\ndate: 2026-10-01\n---\nNot yet.\n")},
		"content/blog/tr/merhaba.md": {Data: []byte("---\ntitle: Merhaba\ndate: 2026-09-20\n---\n## Işık ve İstanbul\n\nMetin.\n")},
	}
}

type setup struct {
	dev    bool
	opts   markdown.Options
	cached bool
	// late registers the plugin with RegisterPlugin rather than Config.Plugins.
	late bool
	// unstarted keeps the application hand-built, for a test that asserts the 503
	// an application serves when a plugin's Init fails (NewSite fails the test).
	unstarted bool
}

const (
	postHTML  = `<html><body><h1>{{.Title}}</h1><p class="d">{{.Description}}</p>{{range .Tags}}<i>{{.}}</i>{{end}}<b>{{index .Front "author"}}</b><main>{{.HTML}}</main></body></html>`
	indexHTML = `<html><body>{{range .}}<li>{{.Slug}}:{{.Title}}</li>{{end}}</body></html>`
)

// pages are the post and index pages the tests render, served by md.
func pages(md *markdown.Plugin) []*collage.Page {
	post := collage.NewPage("post").
		WithContent(collage.NewFragment("post", "post.html").WithData(md.Handler()).Required().Build()).
		WithPath("en", "/blog/{slug}").
		WithPath("tr", "/blog/{slug}").
		Static().
		WithStaticParams(md.StaticParams()).
		Build()
	index := collage.NewPage("index").
		WithContent(collage.NewFragment("index", "index.html").WithData(md.IndexHandler()).Build()).
		WithPath("en", "/blog").
		WithPath("tr", "/blog").
		Static().
		Build()
	return []*collage.Page{post, index}
}

func site(t *testing.T, s setup) (*collage.App, *markdown.Plugin) {
	t.Helper()
	if s.late || s.unstarted {
		return handSite(t, s)
	}
	md := markdown.New(s.opts)
	p := pages(md)
	return collagetest.NewSite(t,
		collagetest.WithPlugins(md),
		collagetest.WithTemplates(map[string]string{"post.html": postHTML, "index.html": indexHTML}),
		collagetest.WithConfig(func(c *collage.Config) {
			c.DevMode = s.dev
			c.Locale = collage.LocaleConfig{Default: "en", Supported: []string{"en", "tr"}}
			if s.cached {
				c.Cache = collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour}
			}
		}),
		collagetest.WithoutHome(),
		collagetest.WithRegister(p[0], p[1]),
	).App, md
}

// handSite is hand-built: with late, the plugin goes in with RegisterPlugin
// before any page, an order a collagetest site cannot give it; with unstarted,
// the application is not started, so a failing Init is seen as a 503.
func handSite(t *testing.T, s setup) (*collage.App, *markdown.Plugin) {
	t.Helper()
	md := markdown.New(s.opts)
	cfg := &collage.Config{
		DevMode: s.dev,
		Server:  collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/post.html":  {Data: []byte(postHTML)},
			"t/index.html": {Data: []byte(indexHTML)},
		}, Root: "t"},
		Locale: collage.LocaleConfig{Default: "en", Supported: []string{"en", "tr"}},
	}
	if s.cached {
		cfg.Cache = collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour}
	}
	if !s.late {
		cfg.Plugins = []collage.Plugin{md}
	}
	app, err := collage.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if s.late {
		if err := app.RegisterPlugin(md); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range pages(md) {
		if err := app.RegisterPage(p); err != nil {
			t.Fatal(err)
		}
	}
	return app, md
}

func get(app *collage.App, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestDocument(t *testing.T) {
	app, _ := site(t, setup{opts: markdown.Options{FS: content(), Dir: "content/blog", LocaleDirs: map[string]string{"tr": "content/blog/tr"}}})
	rec := get(app, "/blog/hello")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /blog/hello = %d\n%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<h1>Hello, world</h1>`,
		`<p class="d">The first post</p>`,
		`<i>go</i><i>collage</i>`,
		`<b>Ada</b>`,
		`<em>prose</em>`,
		`<del>old</del>`,
		`<a href="https://example.com">https://example.com</a>`,
		`<table>`,
		`<input checked="" disabled="" type="checkbox"`,
		`<h2 id="önemli-başlık">Önemli Başlık</h2>`,
		`<h2 id="önemli-başlık-1">Önemli Başlık</h2>`,
		`<pre><code class="language-go">fmt.Println(&quot;&lt;hi&gt;&quot;)`,
		`<div class="footnotes" role="doc-endnotes">`,
		`<!-- raw HTML omitted -->`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %s\n%s", want, body)
		}
	}
	if strings.Contains(body, "<script>") {
		t.Errorf("raw HTML reached the page with Unsafe off:\n%s", body)
	}

	// The first heading is the title when the front matter names none, and is
	// taken out of the body, where the template shows it.
	body = get(app, "/blog/second").Body.String()
	if !strings.Contains(body, "<h1>Second post</h1>") || strings.Contains(body, `<h1 id=`) {
		t.Errorf("title from the first heading:\n%s", body)
	}
	if !strings.Contains(body, "<i>one</i><i>two</i>") {
		t.Errorf("comma-separated tags:\n%s", body)
	}

	// The Turkish directory, with Turkish lower-casing of I and İ.
	body = get(app, "/tr/blog/merhaba").Body.String()
	if !strings.Contains(body, `<h2 id="ışık-ve-istanbul">`) {
		t.Errorf("Turkish heading id:\n%s", body)
	}
}

func TestGetFields(t *testing.T) {
	app, md := site(t, setup{opts: markdown.Options{FS: content(), Dir: "content/blog"}})
	if code := get(app, "/blog").Code; code != http.StatusOK { // starts the application
		t.Fatalf("status %d", code)
	}
	doc, err := md.Get(context.Background(), "en", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if !doc.Date.Equal(time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)) || !doc.Updated.Equal(time.Date(2026, 9, 21, 10, 30, 0, 0, time.UTC)) {
		t.Errorf("dates = %v, %v", doc.Date, doc.Updated)
	}
	if len(doc.Headings) != 2 || doc.Headings[0] != (markdown.Heading{ID: "önemli-başlık", Text: "Önemli Başlık", Level: 2}) {
		t.Errorf("headings = %+v", doc.Headings)
	}
	if !strings.Contains(doc.Text, "Some prose with old words") || strings.Contains(doc.Text, "Println") || strings.Contains(doc.Text, "alert") {
		t.Errorf("text = %q", doc.Text)
	}
	if doc.Front["tags"] != "go, collage" || doc.Locale != "en" {
		t.Errorf("front = %v, locale %q", doc.Front, doc.Locale)
	}
}

func TestNotFoundAndDrafts(t *testing.T) {
	app, md := site(t, setup{opts: markdown.Options{FS: content(), Dir: "content/blog"}})
	for _, path := range []string{"/blog/nothing", "/blog/draft", "/blog/tr%2Fmerhaba"} {
		if code := get(app, path).Code; code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, code)
		}
	}
	// Since collage v0.34.0 a path made dirty by an encoded slash is a 404 before
	// routing, rather than cleaned into another route, so the slug never reaches
	// the plugin; its own guard stays behind that.
	if rec := get(app, "/blog/..%2Fsecret"); rec.Code != http.StatusNotFound {
		t.Errorf("GET /blog/..%%2Fsecret = %d %q, want collage's 404", rec.Code, rec.Header().Get("Location"))
	}
	for _, slug := range []string{"../secret", "tr/merhaba", "..", ""} {
		if _, err := md.Get(context.Background(), "en", slug); !errors.Is(err, collage.ErrNotFound) {
			t.Errorf("Get(%q) = %v, want ErrNotFound", slug, err)
		}
	}
	app, _ = site(t, setup{opts: markdown.Options{FS: content(), Dir: "content/blog", Drafts: true}})
	if body := get(app, "/blog/draft").Body.String(); !strings.Contains(body, "<h1>Secret</h1>") {
		t.Errorf("a draft with Drafts on:\n%s", body)
	}
}

func TestUnsafe(t *testing.T) {
	app, _ := site(t, setup{opts: markdown.Options{FS: content(), Dir: "content/blog", Unsafe: true}})
	if body := get(app, "/blog/hello").Body.String(); !strings.Contains(body, "<script>alert(1)</script>") {
		t.Errorf("Unsafe did not let raw HTML through:\n%s", body)
	}
}

// Newest first; an undated document after every dated one; a draft nowhere.
func TestList(t *testing.T) {
	app, md := site(t, setup{opts: markdown.Options{FS: content(), Dir: "content/blog"}})
	body := get(app, "/blog").Body.String()
	want := "<li>second:Second post</li><li>hello:Hello, world</li><li>undated:Undated</li>"
	if !strings.Contains(body, want) {
		t.Errorf("index = %s, want %s", body, want)
	}
	docs, err := md.List(context.Background(), "")
	if err != nil || len(docs) != 3 {
		t.Fatalf("List = %d docs, %v", len(docs), err)
	}
	app, md = site(t, setup{opts: markdown.Options{FS: content(), Dir: "content/blog", Drafts: true}})
	get(app, "/blog")
	if docs, _ := md.List(context.Background(), ""); len(docs) != 4 || docs[0].Slug != "draft" {
		t.Errorf("List with drafts = %+v", docs)
	}
}

// One invalidation of a file's tag renders its page again from the file as it
// is now, and the index that lists it.
func TestEditIsOneInvalidation(t *testing.T) {
	fsys := content()
	app, md := site(t, setup{cached: true, opts: markdown.Options{FS: fsys, Dir: "content/blog"}})
	get(app, "/blog/second")
	get(app, "/blog")
	fsys["content/blog/second.md"] = &fstest.MapFile{Data: []byte("---\ndate: 2026-09-22\n---\n# Second, edited\n")}
	if body := get(app, "/blog/second").Body.String(); !strings.Contains(body, "<h1>Second post</h1>") {
		t.Fatalf("a cached page changed before any invalidation:\n%s", body)
	}
	if err := app.InvalidateTags(context.Background(), md.Tag("en", "second")); err != nil {
		t.Fatal(err)
	}
	if body := get(app, "/blog/second").Body.String(); !strings.Contains(body, "<h1>Second, edited</h1>") {
		t.Errorf("after invalidating %s:\n%s", md.Tag("en", "second"), body)
	}
	if body := get(app, "/blog").Body.String(); !strings.Contains(body, "second:Second, edited") {
		t.Errorf("the index after invalidating the file:\n%s", body)
	}

	// A new file is the directory's tag.
	fsys["content/blog/third.md"] = &fstest.MapFile{Data: []byte("---\ndate: 2026-09-23\n---\n# Third\n")}
	if err := app.InvalidateTags(context.Background(), md.DirTag("en")); err != nil {
		t.Fatal(err)
	}
	if body := get(app, "/blog").Body.String(); !strings.Contains(body, "<li>third:Third</li>") {
		t.Errorf("the index after invalidating the directory:\n%s", body)
	}
}

// In development a file is read on every request.
func TestDevelopmentRereads(t *testing.T) {
	fsys := content()
	app, _ := site(t, setup{dev: true, cached: true, opts: markdown.Options{FS: fsys, Dir: "content/blog"}})
	get(app, "/blog/second")
	fsys["content/blog/second.md"] = &fstest.MapFile{Data: []byte("# Second, edited\n")}
	if body := get(app, "/blog/second").Body.String(); !strings.Contains(body, "<h1>Second, edited</h1>") {
		t.Errorf("an edit did not show in development:\n%s", body)
	}
}

// A static build writes every document of every locale, and no draft.
// A static build writes a page for every document, with the plugin registered
// either way: the build starts the application, running Init, before it lists
// the static parameters.
func TestStaticBuild(t *testing.T) {
	for _, late := range []bool{false, true} {
		staticBuild(t, late)
	}
}

func staticBuild(t *testing.T, late bool) {
	app, _ := site(t, setup{late: late, cached: true, opts: markdown.Options{FS: content(), Dir: "content/blog", LocaleDirs: map[string]string{"tr": "content/blog/tr"}}})
	out := t.TempDir()
	if _, err := collage.Build(context.Background(), app, collage.BuildOptions{OutDir: out}); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"blog/hello/index.html", "blog/second/index.html", "blog/undated/index.html", "tr/blog/merhaba/index.html"} {
		if _, err := os.Stat(filepath.Join(out, file)); err != nil {
			t.Errorf("late=%v: not written: %s", late, file)
		}
	}
	for _, file := range []string{"blog/draft/index.html", "tr/blog/hello/index.html"} {
		if _, err := os.Stat(filepath.Join(out, file)); err == nil {
			t.Errorf("late=%v: written: %s", late, file)
		}
	}
}

// Two sets of documents register side by side under their own names.
func TestNamedInstances(t *testing.T) {
	blog := markdown.New(markdown.Options{FS: content(), Dir: "content/blog", Name: "blog"})
	docs := markdown.New(markdown.Options{FS: content(), Dir: "content/blog/tr", Name: "docs"})
	if blog.Name() != "elagoht/markdown:blog" {
		t.Errorf("Name = %q", blog.Name())
	}
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(`x`)}}, Root: "t"},
		Plugins:  []collage.Plugin{blog, docs},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = app.RegisterPage(collage.NewPage("home").WithContent(collage.NewFragment("home", "p.html").Build()).WithPath("en", "/").Build())
	if code := get(app, "/").Code; code != http.StatusOK {
		t.Errorf("status %d", code)
	}
}

// A configuration that cannot work, or content that cannot be read, stops the
// application from starting.
func TestMisconfigurationStopsStartup(t *testing.T) {
	for name, opts := range map[string]markdown.Options{
		"no FS":             {Dir: "content/blog"},
		"no such directory": {FS: content(), Dir: "content/nothing"},
		"a file, not a dir": {FS: content(), Dir: "content/blog/hello.md"},
		"an escaping path":  {FS: content(), Dir: "../content"},
		"a bad locale dir":  {FS: content(), Dir: "content/blog", LocaleDirs: map[string]string{"tr": "/abs"}},
	} {
		app, _ := site(t, setup{opts: opts, unstarted: true})
		if code := get(app, "/blog").Code; code != http.StatusServiceUnavailable {
			t.Errorf("%s: status %d, want 503", name, code)
		}
	}

	broken := func(file string) fstest.MapFS {
		fsys := content()
		fsys["content/blog/broken.md"] = &fstest.MapFile{Data: []byte(file)}
		return fsys
	}
	for name, opts := range map[string]markdown.Options{
		"an unknown locale":   {FS: content(), Dir: "content/blog", LocaleDirs: map[string]string{"de": "content/blog/tr"}},
		"unclosed front":      {FS: broken("---\ntitle: x\n\n# Body\n"), Dir: "content/blog"},
		"front not YAML":      {FS: broken("---\ntitle: [x\n---\n"), Dir: "content/blog"},
		"front not a mapping": {FS: broken("---\n- a\n- b\n---\n"), Dir: "content/blog"},
		"a date that is not":  {FS: broken("---\ndate: last tuesday\n---\n"), Dir: "content/blog"},
		"a draft that is not": {FS: broken("---\ndraft: maybe\n---\n"), Dir: "content/blog"},
	} {
		app, _ := site(t, setup{opts: opts, unstarted: true})
		if code := get(app, "/blog").Code; code != http.StatusServiceUnavailable {
			t.Errorf("%s: status %d, want 503", name, code)
		}
	}
}

// A handler of a plugin the application never registered says so.
func TestNotRegistered(t *testing.T) {
	md := markdown.New(markdown.Options{FS: content(), Dir: "content/blog"})
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(`{{.Title}}`)}}, Root: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = app.RegisterPage(collage.NewPage("post").WithContent(collage.NewFragment("post", "p.html").WithData(md.Handler()).Required().Build()).WithPath("en", "/blog/{slug}").Build())
	if code := get(app, "/blog/hello").Code; code != http.StatusInternalServerError {
		t.Errorf("status %d, want 500", code)
	}
	if _, err := md.List(context.Background(), ""); err == nil {
		t.Error("List worked without the plugin being registered")
	}
}

// List takes the locale: "" is the default one's documents, "tr" the Turkish
// directory's.
func TestListInALocale(t *testing.T) {
	md := markdown.New(markdown.Options{FS: content(), Dir: "content/blog", LocaleDirs: map[string]string{"tr": "content/blog/tr"}})
	collagetest.NewSite(t,
		collagetest.WithPlugins(md),
		collagetest.WithConfig(func(c *collage.Config) {
			c.Locale = collage.LocaleConfig{Default: "en", Supported: []string{"en", "tr"}}
		}))
	en, err := md.List(context.Background(), "")
	if err != nil || len(en) != 3 {
		t.Fatalf(`List(ctx, "") = %d docs, %v; want the 3 published en documents`, len(en), err)
	}
	tr, err := md.List(context.Background(), "tr")
	if err != nil || len(tr) != 1 || tr[0].Slug != "merhaba" {
		t.Fatalf(`List(ctx, "tr") = %+v, %v; want merhaba`, tr, err)
	}
}

// "" is the default locale everywhere: List, Get, Tag and DirTag agree with
// the default locale's own name, its LocaleDirs entry included.
func TestEmptyLocaleIsTheDefault(t *testing.T) {
	md := markdown.New(markdown.Options{FS: content(), Dir: "content/blog", LocaleDirs: map[string]string{"en": "content/blog/tr"}})
	collagetest.NewSite(t,
		collagetest.WithPlugins(md),
		collagetest.WithConfig(func(c *collage.Config) {
			c.Locale = collage.LocaleConfig{Default: "en", Supported: []string{"en", "tr"}}
		}))
	ctx := context.Background()
	empty, err1 := md.List(ctx, "")
	en, err2 := md.List(ctx, "en")
	if err1 != nil || err2 != nil || len(empty) != 1 || len(en) != 1 || empty[0].Slug != en[0].Slug || empty[0].Locale != "en" {
		t.Errorf(`List(ctx, "") = %+v, %v; List(ctx, "en") = %+v, %v`, empty, err1, en, err2)
	}
	doc, err := md.Get(ctx, "", "merhaba")
	if err != nil || doc.Locale != "en" {
		t.Errorf(`Get(ctx, "", "merhaba") = locale %q, %v; want en`, doc.Locale, err)
	}
	if md.Tag("", "merhaba") != md.Tag("en", "merhaba") || md.DirTag("") != md.DirTag("en") {
		t.Errorf("tags differ: %q/%q, %q/%q", md.Tag("", "merhaba"), md.Tag("en", "merhaba"), md.DirTag(""), md.DirTag("en"))
	}
}
