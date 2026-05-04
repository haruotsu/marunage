// Package journal implements the PR-103 work journal: it collects activity
// from git, marunage tasks, GitHub, and other sources on a configurable
// interval and appends timestamped Markdown entries to
// ~/.marunage/journal/YYYY-MM-DD.md.
package journal

import "time"

// Item is a single activity line inside a journal section.
type Item struct {
	Text string
}

// Section groups related items under a heading.
type Section struct {
	Title string
	Items []Item
}

// Entry is one timestamped journal record covering one collection interval.
type Entry struct {
	At       time.Time
	Sections []Section
}
