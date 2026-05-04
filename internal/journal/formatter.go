package journal

import (
	"fmt"
	"strings"
)

// Format renders an Entry as a Markdown block.
// Sections with no items are omitted so the output stays concise.
func Format(e Entry) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## %s\n\n", e.At.UTC().Format("2006-01-02 15:04"))
	for _, s := range e.Sections {
		if len(s.Items) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "### %s\n", s.Title)
		for _, item := range s.Items {
			fmt.Fprintf(&sb, "- %s\n", item.Text)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
