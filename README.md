# elagoht/markdown

A collage plugin that makes a directory of Markdown files page data: YAML front
matter, GitHub-flavoured Markdown with heading ids and footnotes, one dependency tag
per file, and the lists an index page, a feed and a sitemap are made from.

```go
//go:embed content
var content embed.FS

md := markdown.New(markdown.Options{FS: content, Dir: "content/blog"})

app, err := collage.New(&collage.Config{
	Plugins: []collage.Plugin{md},
})

app.RegisterPage(collage.NewPage("post").
	WithContent(collage.NewFragment("post", "pages/post.html").
		WithDataHandler(md.Handler()).
		Required().
		Build()).
	WithPath("en", "/blog/{slug}").
	Static().
	WithStaticParams(md.StaticParams()).
	Build())
```

Requires collage v0.24.0 or later. Register it in `Config.Plugins` or with
`app.RegisterPlugin`: it reads its configuration and its files when the application
starts, which a static build does before it lists the pages to write.

## What it gives you

A plugin cannot add templates, so this one supplies the mechanism and the
application keeps the markup. It hands over:

| | |
| --- | --- |
| `md.Handler()` | A data handler: the `Doc` that `rc.Param("slug")` names, in the render's locale, with the file's dependency tag. No such file, or a draft, is `collage.ErrNotFound`, so a `Required()` fragment answers 404 |
| `md.IndexHandler()` | A data handler for an index page: every document, newest first, with the directory's tag and every file's |
| `md.StaticParams()` | For `WithStaticParams`: `{"slug": …}` for every document of the locale being built |
| `md.List(ctx)`, `md.ListIn(ctx, locale)` | The documents, newest first, for a feed's items or a sitemap's `LastMod` |
| `md.Get(ctx, locale, slug)` | One document |
| `md.Tag(locale, slug)`, `md.DirTag(locale)` | The dependency tags, to invalidate |

The template renders a `Doc` as it likes:

```html
<article>
  <h1>{{.Title}}</h1>
  <time datetime="{{.Date.Format "2006-01-02"}}">{{.Date.Format "2 January 2006"}}</time>
  {{.HTML}}
</article>
```

## A file

```markdown
---
title: Hello, world
description: The first post
date: 2026-09-20
updated: 2026-09-21T10:30:00Z
tags: [go, collage]
author: Ada
---

Some *prose*, a table, a footnote[^1].

[^1]: The note.
```

The file's name is its slug: `content/blog/hello.md` is `hello`. The front matter is
YAML between `---` lines, and a file without one is all body.

| `Doc` field | From |
| --- | --- |
| `Slug` | the file name, without `.md` |
| `Title` | `title`, or else the first `# heading`, which is then taken out of `HTML` since the template shows the title |
| `Description` | `description` |
| `Date`, `Updated` | `date`, `updated` (or `lastmod`): `2026-09-20`, `2026-09-20 10:00`, RFC 3339; a date without a zone is UTC |
| `Tags` | `tags`: a YAML list, or one comma-separated line |
| `Draft` | `draft: true` |
| `Front` | every scalar of the front matter as written, lists joined with `", "` |
| `HTML` | the rendered body |
| `Text` | the body as text — prose, lists, tables, inline code; no code blocks or raw HTML — for a summary, a search index, a word count |
| `Headings` | every heading below the title: `ID`, `Text`, `Level` |
| `Locale` | the locale it was read for |

A date that does not parse, a `draft` that is neither true nor false, and front
matter that is not YAML are errors, and they stop the application from starting: a
post that silently sorts last is a mistake nobody sees.

## Markdown

[goldmark](https://github.com/yuin/goldmark), with GitHub-flavoured Markdown —
tables, strikethrough, autolinks, task lists — and footnotes. Every heading gets an
id made from its text, keeping letters of any alphabet: `## Önemli Başlık` is
`id="önemli-başlık"`, and in a Turkish directory `Işık` is `ışık`. A heading can
name its own id, `## Installation {#install}`, which is how a translation keeps
the original's anchors.

A code block is `<pre><code class="language-go">`, the shape every highlighter
reads; [elagoht/highlight](https://github.com/Elagoht/collage-highlight) colours it.

Raw HTML written in the Markdown is left out unless `unsafe` is on: a document
cannot put a script on the site by accident. Turn it on when the files are yours.

## Keeping it current

Each document is rendered with its file's tag, `markdown:content/blog/hello.md`,
so an edit is one invalidation — of the page, the index listing it, and what the
plugin holds of the file:

```go
app.InvalidateTags(ctx, md.Tag("en", "hello"))
```

A file added or removed is the directory's tag, `md.DirTag("en")`, which an index
page depends on.

In production the files are read once and kept until one of those tags is
invalidated. **In development every request reads the file again**, so an edit
shows on the next reload without a restart.

## Several languages

A locale can have its own directory:

```go
markdown.New(markdown.Options{
	FS:         content,
	Dir:        "content/blog",
	LocaleDirs: map[string]string{"tr": "content/blog/tr"},
})
```

A page with a path in each locale renders `/blog/hello` from `content/blog/hello.md`
and `/tr/blog/merhaba` from `content/blog/tr/merhaba.md`; a static build writes each
locale's own files. Only the `*.md` directly in a directory are read, so a
translation can live in a subdirectory of the original. A locale not named reads
`Dir`.

## A feed, a sitemap

```go
feed.New(feed.Feed{
	Title:   "The blog",
	BaseURL: "https://example.com",
	Tags:    []string{md.DirTag("en")},
	Items: func(ctx context.Context) ([]feed.Item, error) {
		docs, err := md.List(ctx)
		// … one feed.Item per doc: Title, "/blog/"+Slug, Description, HTML, Date, Updated, Tags
	},
})
```

A sitemap's `LastMod` reads the document its URL's `slug` names:

```go
LastMod: func(ctx context.Context, page string, u collage.PageURL) time.Time {
	doc, err := md.Get(ctx, u.Locale, u.Params["slug"])
	if err != nil {
		return time.Time{}
	}
	if !doc.Updated.IsZero() {
		return doc.Updated
	}
	return doc.Date
},
```

## Several sets

A blog and the documentation are two plugins; `Name` tells them apart, and the
plugin is then `elagoht/markdown:<Name>`, which is also its configuration key:

```go
blog := markdown.New(markdown.Options{FS: content, Dir: "content/blog", Name: "blog"})
docs := markdown.New(markdown.Options{FS: content, Dir: "content/docs", Name: "docs"})
```

## Configuration

`FS` and `Name` are set in Go; the rest can come from configuration too:

```json
{
  "elagoht/markdown": {
    "dir": "content/blog",
    "localeDirs": { "tr": "content/blog/tr" },
    "drafts": false,
    "unsafe": false
  }
}
```

`drafts: true` in a development configuration shows drafts while writing and keeps
them out of production. A directory that does not exist, a path leaving the file
system, or a locale the application does not have stops the application from
starting.

## Limitations

- A document's slug is its file name. There is no `slug` front matter key, and no
  nested directories of documents.
- A nested mapping in the front matter is not kept in `Front`, which holds strings.
- In production a file added to the directory is not seen until `DirTag` is
  invalidated or the process restarts; the plugin does not watch the file system.
- A plugin cannot add templates, so there is no default post or index layout.
- `md.Tag` and `md.DirTag` called before the application starts — building a
  feed's options, say — use the directories given in Go; a `dir` or `localeDirs`
  set only in the JSON configuration is read when the application starts.

## Changes

### v0.1.2

- `collage.json`: the plugin described to editors — its template functions,
  snippets and configuration schema — for the Collage Snippets & Highlighter
  extension and any tool reading it.

### v0.1.1

- The plugin sets itself up in `Init` rather than `Configure`, so it can be added
  with `app.RegisterPlugin` too: collage v0.24.0 starts the application before a
  static build lists a page's static parameters. A configuration that cannot work
  now stops the application from starting rather than `collage.New`.
- `md.Tag` and `md.DirTag` read the directories from the plugin's configuration
  once the application has started; before that they read the `Options` given in
  Go.
- Requires collage v0.24.0.
