package metadata

import (
	"fmt"

	"github.com/eawag-rdm/pc/pkg/structs"
)

// RunChecks executes every check registered on every entity of m and returns
// one Message per failure. It is the deferred counterpart to the registration
// a collector performs while mapping its source document.
func RunChecks(m *Metadata) []structs.Message {
	if m == nil {
		return nil
	}
	var messages []structs.Message
	for _, e := range m.Entities {
		for _, bc := range e.checks {
			problem := bc.rule.Fn(e.Fields[bc.field])
			if problem == "" {
				continue
			}
			messages = append(messages, structs.Message{
				Content: fmt.Sprintf("%s %q, field %q: %s",
					e.Kind, e.Name, bc.field, problem),
				Source:   e,
				TestName: bc.rule.Name,
			})
		}
	}
	return messages
}
