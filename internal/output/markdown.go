package output

import (
	"strings"

	"github.com/charmbracelet/glamour"
)

// Markdown renders a message body or a description for a human. On a terminal
// the markdown is styled through glamour; when the output is piped it is printed
// verbatim, because markdown is already plain text and a script wants the source,
// not a styled version of it (PLAN.md "Output and UX conventions").
//
// indent is prefixed to every rendered line, which is how a body reads as part
// of the header line above it.
func (p *Printer) Markdown(s, indent string) {
	if strings.TrimSpace(s) == "" {
		return
	}
	out := strings.Trim(s, "\n")
	if p.color {
		if rendered, err := glamour.Render(out, "dark"); err == nil {
			out = strings.Trim(rendered, "\n")
		}
	}
	for _, line := range strings.Split(out, "\n") {
		p.Println(strings.TrimRight(indent+line, " "))
	}
}
