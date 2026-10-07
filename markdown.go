// Package markdown is a collage plugin that makes a directory of Markdown files
// page data.
//
//	md := markdown.New(markdown.Options{FS: content, Dir: "content/blog"})
//
//	app, err := collage.New(&collage.Config{
//		Plugins: []collage.Plugin{md},
//	})
//
//	post := collage.NewFragment("post", "pages/post.html").
//		WithData(md.Handler()). // a markdown.Doc for rc.Param("slug")
//		Build()
//	app.RegisterPage(collage.NewPage("post").
//		WithContent(post).
//		WithPath("en", "/blog/{slug}").
//		Static().
//		WithStaticParams(md.StaticParams()).
//		Build())
//
// A plugin cannot add templates, so it hands the application the pieces instead:
// a data handler for one document, one for the list an index page shows, the
// path parameters a static build writes, and the list itself, for a feed or a
// sitemap. The template renders a Doc as it likes — {{.HTML}} is the body.
//
// A file is YAML front matter between "---" lines, then GitHub-flavoured
// Markdown: tables, strikethrough, autolinks, task lists, footnotes, and an id on
// every heading. Each document is rendered with its file's dependency tag, so an
// edit is one invalidation; in development every request reads the file again.
package markdown

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Elagoht/collage/pkg/collage"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"gopkg.in/yaml.v3"
)

// Name is the plugin's name, and the key its configuration is found under.
const Name = "elagoht/markdown"

// Options configures the plugin.
type Options struct {
	// FS holds the Markdown files — an embed.FS, or os.DirFS for files edited
	// in place. Required, and only from Go.
	FS fs.FS `json:"-"`
	// Dir is the directory in FS whose *.md files are the documents: one file
	// per document, named for its slug. Subdirectories are not read, so a
	// translation can live in one. Default ".".
	Dir string `json:"dir"`
	// LocaleDirs gives a locale its own directory: {"tr": "content/blog/tr"}. A
	// locale not named reads Dir.
	LocaleDirs map[string]string `json:"localeDirs"`
	// Drafts includes documents whose front matter says draft: true. Off, a
	// draft is not listed, not built, and answers 404.
	Drafts bool `json:"drafts"`
	// Unsafe lets raw HTML written in the Markdown through to the page. Off, it
	// is left out, so a document cannot put a script on the site.
	Unsafe bool `json:"unsafe"`
	// Name tells several sets of documents apart — a blog and the docs — when
	// the application registers more than one: the plugin is then
	// "elagoht/markdown:<Name>", and so is its configuration key. Only from Go.
	Name string `json:"-"`
}

// Doc is one Markdown document.
type Doc struct {
	// Slug is the file's name without ".md".
	Slug string
	// Locale is the locale the document was read for.
	Locale string
	// Title is the front matter's title, or else the first level-one heading,
	// which is then taken out of HTML: the template shows the title.
	Title string
	// Description is the front matter's description.
	Description string
	// Date is when the document was published, and Updated when it last
	// changed; either is zero when the front matter does not say.
	Date    time.Time
	Updated time.Time
	// Tags are the front matter's tags: a list, or one comma-separated line.
	Tags []string
	// Draft is the front matter's draft.
	Draft bool
	// Front is every scalar in the front matter, the ones above included, as
	// written; a list is joined with ", ". A nested mapping is not kept.
	Front map[string]string
	// HTML is the rendered body.
	HTML template.HTML
	// Text is the body as text — prose, lists, tables and inline code, without
	// code blocks or raw HTML — for a summary, a search index or a word count.
	Text string
	// Headings are the body's headings, in order, below the title.
	Headings []Heading
}

// Heading is one heading of a document.
type Heading struct {
	ID    string
	Text  string
	Level int
}

// Plugin reads the documents.
type Plugin struct {
	opts          Options
	md            goldmark.Markdown
	dev           bool
	ready         bool
	defaultLocale string

	mu   sync.Mutex
	sets map[string]*set // by directory; production only
}

// set is every document of one directory, parsed.
type set struct {
	docs map[string]*Doc
	tags map[string]bool // the file tags of docs
}

// The hooks the plugin means to implement: a misspelt method would otherwise
// be a hook that silently never fires.
var (
	_ collage.Plugin              = (*Plugin)(nil)
	_ collage.CacheInvalidateHook = (*Plugin)(nil)
)

// New returns a plugin reading the documents opts describes; the application's
// own configuration is then decoded over opts.
func New(opts Options) *Plugin { return &Plugin{opts: opts} }

// Name is "elagoht/markdown", or "elagoht/markdown:<Options.Name>".
func (p *Plugin) Name() string {
	if p.opts.Name != "" {
		return Name + ":" + p.opts.Name
	}
	return Name
}

func (p *Plugin) Version() string                { return "0.2.2" }
func (p *Plugin) Shutdown(context.Context) error { return nil }

var errNotRegistered = errors.New("markdown: the plugin has not started; register it with the application, in Config.Plugins or with RegisterPlugin")

// Init reads the configuration, prepares the renderer, checks the locales
// against the application's and reads every document, so a file whose front
// matter does not parse stops the application from starting rather than failing
// its first reader. It is all here, rather than partly in Configure, because a
// static build starts the application before it lists a page's static
// parameters, and a plugin without Configure can be added with RegisterPlugin.
func (p *Plugin) Init(_ context.Context, host collage.Host) error {
	var err error
	if p.opts, err = collage.PluginConfig(host, p.opts); err != nil {
		return err
	}
	if p.opts.FS == nil {
		return errors.New("markdown: Options.FS is required")
	}
	p.dev = host.DevMode()
	if p.opts.Dir == "" {
		p.opts.Dir = "."
	}
	dirs := []string{p.opts.Dir}
	for _, dir := range p.opts.LocaleDirs {
		dirs = append(dirs, dir)
	}
	for _, dir := range dirs {
		if !fs.ValidPath(dir) {
			return fmt.Errorf("markdown: %q is not a directory path in the file system: no leading slash, no ..", dir)
		}
		info, err := fs.Stat(p.opts.FS, dir)
		if err != nil {
			return fmt.Errorf("markdown: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("markdown: %s is not a directory", dir)
		}
	}

	var rendering []renderer.Option
	if p.opts.Unsafe {
		rendering = append(rendering, html.WithUnsafe())
	}
	p.md = goldmark.New(
		goldmark.WithExtensions(extension.GFM, extension.Footnote),
		goldmark.WithParserOptions(parser.WithAutoHeadingID(), parser.WithHeadingAttribute()),
		goldmark.WithRendererOptions(rendering...),
	)
	p.sets = make(map[string]*set)

	def, supported := host.Locales()
	p.defaultLocale = def
	for locale, dir := range p.opts.LocaleDirs {
		if !contains(supported, locale) && locale != def {
			return fmt.Errorf("markdown: localeDirs names %q, which is not a locale of the application", locale)
		}
		if _, err := p.load(dir, locale); err != nil {
			return err
		}
	}
	if _, err := p.load(p.opts.Dir, def); err != nil {
		return err
	}
	p.ready = true
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// dir is where locale's documents are.
func (p *Plugin) dir(locale string) string {
	if dir, ok := p.opts.LocaleDirs[locale]; ok {
		return dir
	}
	return p.opts.Dir
}

// Tag is the dependency tag of the document slug in locale: invalidating it
// renders the document again, and has the plugin read the file again.
func (p *Plugin) Tag(locale, slug string) string {
	return fileTag(p.dir(locale), slug)
}

// DirTag is the dependency tag of locale's directory as a whole — what a list
// depends on besides its documents. Invalidate it when a file is added or
// removed. Like Tag, before the application starts it reads the directories
// given in Go, since the plugin's configuration is read in Init.
func (p *Plugin) DirTag(locale string) string {
	return dirTag(p.dir(locale))
}

// Tags are built from the directory as it was configured rather than cleaned,
// so every tag of a directory starts with its directory's tag.
func dirTag(dir string) string        { return "markdown:" + dir + "/" }
func fileTag(dir, slug string) string { return dirTag(dir) + slug + ".md" }

// Handler is a data handler rendering the document rc.Param("slug") names, in
// the render's locale, as a Doc, with the file's dependency tag. A slug with no
// document — or a draft, unless Options.Drafts — is collage.ErrNotFound.
func (p *Plugin) Handler() collage.Data {
	return collage.DataHandler(func(ctx context.Context, rc *collage.RenderContext) (Doc, []string, error) {
		doc, err := p.Get(ctx, rc.Locale, rc.Param("slug"))
		if err != nil {
			return Doc{}, nil, err
		}
		return doc, []string{p.Tag(rc.Locale, doc.Slug)}, nil
	})
}

// IndexHandler is a data handler for an index page: the render's locale's
// documents, as List orders them, with the directory's tag and every
// document's, so an edit to any of them — or a file added — renders the index
// again.
func (p *Plugin) IndexHandler() collage.Data {
	return collage.DataHandler(func(ctx context.Context, rc *collage.RenderContext) ([]Doc, []string, error) {
		docs, err := p.ListIn(ctx, rc.Locale)
		if err != nil {
			return nil, nil, err
		}
		tags := make([]string, 0, len(docs)+1)
		tags = append(tags, p.DirTag(rc.Locale))
		for _, d := range docs {
			tags = append(tags, p.Tag(rc.Locale, d.Slug))
		}
		return docs, tags, nil
	})
}

// StaticParams lists, for a static build, every document of the locale being
// built as {"slug": …}: what PageBuilder.WithStaticParams takes.
func (p *Plugin) StaticParams() collage.StaticParamsFunc {
	return func(ctx context.Context, locale string) ([]map[string]string, error) {
		docs, err := p.ListIn(ctx, locale)
		if err != nil {
			return nil, err
		}
		params := make([]map[string]string, 0, len(docs))
		for _, d := range docs {
			params = append(params, map[string]string{"slug": d.Slug})
		}
		return params, nil
	}
}

// Get returns the document slug in locale.
func (p *Plugin) Get(_ context.Context, locale, slug string) (Doc, error) {
	if !p.ready {
		return Doc{}, errNotRegistered
	}
	dir := p.dir(locale)
	// A slug is one file of the directory, never a path out of it.
	if slug == "" || strings.ContainsAny(slug, `/\`) || slug == "." || slug == ".." {
		return Doc{}, fmt.Errorf("markdown: no document %q: %w", slug, collage.ErrNotFound)
	}
	var doc *Doc
	if p.dev {
		// Read the file on every request, so an edit shows on the next reload.
		source, err := fs.ReadFile(p.opts.FS, path.Join(dir, slug+".md"))
		if errors.Is(err, fs.ErrNotExist) {
			return Doc{}, fmt.Errorf("markdown: no document %q in %s: %w", slug, dir, collage.ErrNotFound)
		}
		if err != nil {
			return Doc{}, fmt.Errorf("markdown: %w", err)
		}
		if doc, err = p.parse(dir, slug, locale, source); err != nil {
			return Doc{}, err
		}
	} else {
		s, err := p.load(dir, locale)
		if err != nil {
			return Doc{}, err
		}
		doc = s.docs[slug]
	}
	if doc == nil || (doc.Draft && !p.opts.Drafts) {
		return Doc{}, fmt.Errorf("markdown: no document %q in %s: %w", slug, dir, collage.ErrNotFound)
	}
	return withLocale(doc, locale), nil
}

// List returns the default locale's documents, newest first, drafts left out
// unless Options.Drafts: an index page's, a feed's or a sitemap's contents.
func (p *Plugin) List(ctx context.Context) ([]Doc, error) {
	return p.ListIn(ctx, p.defaultLocale)
}

// ListIn returns locale's documents, as List does.
//
// Newest first by Date; documents without one come after every dated one, by
// slug.
func (p *Plugin) ListIn(_ context.Context, locale string) ([]Doc, error) {
	if !p.ready {
		return nil, errNotRegistered
	}
	s, err := p.load(p.dir(locale), locale)
	if err != nil {
		return nil, err
	}
	out := make([]Doc, 0, len(s.docs))
	for _, d := range s.docs {
		if d.Draft && !p.opts.Drafts {
			continue
		}
		out = append(out, withLocale(d, locale))
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !a.Date.Equal(b.Date) {
			return a.Date.After(b.Date)
		}
		return a.Slug < b.Slug
	})
	return out, nil
}

// withLocale is a copy of d for locale. Two locales may read one directory, and
// the documents parsed from it are shared between them.
func withLocale(d *Doc, locale string) Doc {
	out := *d
	out.Locale = locale
	return out
}

// OnCacheInvalidate forgets the documents of a directory when its tag, or one of
// its files' tags, is invalidated, so the next render reads the files again: one
// invalidation refreshes the page and what the plugin holds of it.
func (p *Plugin) OnCacheInvalidate(_ context.Context, ev *collage.CacheInvalidateEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for dir, s := range p.sets {
		for _, tag := range ev.Tags {
			if tag == dirTag(dir) || s.tags[tag] {
				delete(p.sets, dir)
				break
			}
		}
	}
	return nil
}

// load returns dir's documents: parsed once and kept in production, read again
// on every call in development.
func (p *Plugin) load(dir, locale string) (*set, error) {
	if !p.dev {
		p.mu.Lock()
		defer p.mu.Unlock()
		if s, ok := p.sets[dir]; ok {
			return s, nil
		}
	}
	files, err := fs.Glob(p.opts.FS, path.Join(dir, "*.md"))
	if err != nil {
		return nil, fmt.Errorf("markdown: %w", err)
	}
	s := &set{docs: make(map[string]*Doc, len(files)), tags: make(map[string]bool, len(files))}
	for _, file := range files {
		source, err := fs.ReadFile(p.opts.FS, file)
		if err != nil {
			return nil, fmt.Errorf("markdown: %w", err)
		}
		slug := strings.TrimSuffix(path.Base(file), ".md")
		doc, err := p.parse(dir, slug, locale, source)
		if err != nil {
			return nil, err
		}
		s.docs[slug] = doc
		s.tags[fileTag(dir, slug)] = true
	}
	if !p.dev {
		p.sets[dir] = s
	}
	return s, nil
}

// parse reads one file. dir and slug name it in errors.
func (p *Plugin) parse(dir, slug, locale string, source []byte) (*Doc, error) {
	file := path.Join(dir, slug+".md")
	front, body, err := splitFront(source)
	if err != nil {
		return nil, fmt.Errorf("markdown: %s: %w", file, err)
	}
	doc := &Doc{Slug: slug, Front: map[string]string{}}
	if err := doc.readFront(front); err != nil {
		return nil, fmt.Errorf("markdown: %s: %w", file, err)
	}

	ctx := parser.NewContext(parser.WithIDs(newIDs(locale)))
	root := p.md.Parser().Parse(text.NewReader(body), parser.WithContext(ctx))

	// Removed after the walk rather than during it: removing the node a walk is
	// on cuts the link to its next sibling, and the walk ends there.
	var title ast.Node
	_ = ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		heading, ok := n.(*ast.Heading)
		if !ok || !entering {
			return ast.WalkContinue, nil
		}
		label := plain(heading, body)
		if heading.Level == 1 && doc.Title == "" && title == nil {
			doc.Title = label
			title = heading
			return ast.WalkSkipChildren, nil
		}
		var id string
		if v, ok := heading.AttributeString("id"); ok {
			if b, ok := v.([]byte); ok {
				id = string(b)
			}
		}
		doc.Headings = append(doc.Headings, Heading{ID: id, Text: label, Level: heading.Level})
		return ast.WalkSkipChildren, nil
	})
	if title != nil {
		title.Parent().RemoveChild(title.Parent(), title)
	}
	// The text leaves code blocks and raw HTML out: they are long, and what
	// they say is usually in the prose around them.
	doc.Text = plain(root, body)

	var out bytes.Buffer
	if err := p.md.Renderer().Render(&out, body, root); err != nil {
		return nil, fmt.Errorf("markdown: %s: %w", file, err)
	}
	doc.HTML = template.HTML(out.String()) // rendered by goldmark, which leaves raw HTML out unless Options.Unsafe
	return doc, nil
}

// splitFront separates a file's front matter from its body. A file that does not
// begin with a "---" line has none.
func splitFront(source []byte) (front, body []byte, err error) {
	source = bytes.TrimPrefix(source, []byte("\xef\xbb\xbf")) // a byte order mark an editor left
	first, rest, _ := bytes.Cut(source, []byte("\n"))
	if string(bytes.TrimRight(first, " \t\r")) != "---" {
		return nil, source, nil
	}
	offset := 0
	for len(rest[offset:]) > 0 {
		line, _, _ := bytes.Cut(rest[offset:], []byte("\n"))
		if s := string(bytes.TrimRight(line, " \t\r")); s == "---" || s == "..." {
			end := offset + len(line)
			if end < len(rest) {
				end++ // the newline
			}
			return rest[:offset], rest[end:], nil
		}
		offset += len(line) + 1
		if offset > len(rest) {
			break
		}
	}
	return nil, nil, errors.New("the front matter is not closed with a --- line")
}

// readFront decodes the front matter into d.
func (d *Doc) readFront(front []byte) error {
	if len(bytes.TrimSpace(front)) == 0 {
		return nil
	}
	var node yaml.Node
	if err := yaml.Unmarshal(front, &node); err != nil {
		return fmt.Errorf("front matter: %w", err)
	}
	if len(node.Content) == 0 {
		return nil
	}
	mapping := node.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return errors.New("front matter: not a mapping of keys to values")
	}
	var tags []string
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key, value := mapping.Content[i].Value, mapping.Content[i+1]
		switch value.Kind {
		case yaml.ScalarNode:
			d.Front[key] = value.Value
			if key == "tags" {
				tags = splitList(value.Value)
			}
		case yaml.SequenceNode:
			var items []string
			for _, item := range value.Content {
				if item.Kind == yaml.ScalarNode && strings.TrimSpace(item.Value) != "" {
					items = append(items, strings.TrimSpace(item.Value))
				}
			}
			d.Front[key] = strings.Join(items, ", ")
			if key == "tags" {
				tags = items
			}
		}
	}
	d.Tags = tags
	d.Title = strings.TrimSpace(d.Front["title"])
	d.Description = strings.TrimSpace(d.Front["description"])
	var err error
	if d.Date, err = parseDate(d.Front["date"]); err != nil {
		return fmt.Errorf("front matter: date: %w", err)
	}
	updated := d.Front["updated"]
	if updated == "" {
		updated = d.Front["lastmod"]
	}
	if d.Updated, err = parseDate(updated); err != nil {
		return fmt.Errorf("front matter: updated: %w", err)
	}
	if v := strings.TrimSpace(d.Front["draft"]); v != "" {
		if d.Draft, err = strconv.ParseBool(v); err != nil {
			return fmt.Errorf("front matter: draft: %q is neither true nor false", v)
		}
	}
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

var dateLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
}

// parseDate reads a date as front matter writes one. A date without a zone is
// UTC. A date that does not parse is an error rather than a zero time: a post
// that silently sorts last is a mistake nobody sees.
func parseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	for _, layout := range dateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not a date such as 2026-09-20 or 2026-09-20T10:00:00Z", s)
}

// plain is a node's text as a reader sees it, markup taken away.
func plain(n ast.Node, source []byte) string {
	var b strings.Builder
	_ = ast.Walk(n, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			// Table cells and list items are blocks inside a block; without a
			// space between them their words run together.
			if n.Type() == ast.TypeBlock {
				b.WriteByte(' ')
			}
			return ast.WalkContinue, nil
		}
		switch node := n.(type) {
		case *ast.FencedCodeBlock, *ast.CodeBlock, *ast.HTMLBlock, *ast.RawHTML:
			return ast.WalkSkipChildren, nil
		case *ast.Text:
			b.Write(node.Segment.Value(source))
			if node.SoftLineBreak() || node.HardLineBreak() {
				b.WriteByte(' ')
			}
		case *ast.String:
			b.Write(node.Value)
		case *ast.AutoLink:
			b.Write(node.Label(source))
		}
		return ast.WalkContinue, nil
	})
	return strings.Join(strings.Fields(b.String()), " ")
}

// ids gives headings ids that keep their letters, whatever the alphabet:
// goldmark's own drops every byte that is not ASCII, which leaves "Önemli
// başlık" as "nemli-balk".
type ids struct {
	locale string
	used   map[string]bool
}

func newIDs(locale string) *ids { return &ids{locale: locale, used: map[string]bool{}} }

func (s *ids) Generate(value []byte, kind ast.NodeKind) []byte {
	base := slugify(string(value), s.locale)
	if base == "" {
		base = "heading"
		if kind != ast.KindHeading {
			base = "id"
		}
	}
	id := base
	for i := 1; s.used[id]; i++ {
		id = base + "-" + strconv.Itoa(i)
	}
	s.used[id] = true
	return []byte(id)
}

func (s *ids) Put(value []byte) { s.used[string(value)] = true }

// slugify makes an id of text: lower case, letters and digits of any alphabet
// kept, everything between them a single hyphen. For Turkish and Azerbaijani,
// "I" is "ı" and "İ" is "i", as those languages lower them; in any other locale
// "İ" is "i" rather than an "i" with a combining dot above.
func slugify(text, locale string) string {
	turkic := locale == "tr" || locale == "az" || strings.HasPrefix(locale, "tr-") || strings.HasPrefix(locale, "az-")
	var b strings.Builder
	hyphen := false
	for _, r := range text {
		switch {
		case r == 'İ':
			r = 'i'
		case r == 'I' && turkic:
			r = 'ı'
		}
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if hyphen && b.Len() > 0 {
				b.WriteByte('-')
			}
			hyphen = false
			b.WriteRune(unicode.ToLower(r))
		case unicode.Is(unicode.Mn, r):
			// A combining mark belongs to the letter before it; an id without
			// it reads the same.
		default:
			hyphen = true
		}
	}
	return b.String()
}
