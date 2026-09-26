// A collage plugin that makes a directory of Markdown files page data: front
// matter, GitHub-flavoured Markdown, heading ids and footnotes, one dependency
// tag per file, and the lists an index, a feed and a sitemap are made from.
module github.com/Elagoht/collage-markdown

go 1.26

require (
	github.com/Elagoht/collage v0.24.0
	github.com/yuin/goldmark v1.8.6
	gopkg.in/yaml.v3 v3.0.1
)
