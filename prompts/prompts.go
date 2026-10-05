// Package prompts embeds the agent prompt templates (06-prompts.md).
// Texts are normative: change them only together with PromptsVersion.
package prompts

import (
	"bytes"
	"embed"
	"fmt"
	"text/template"
)

// PromptsVersion identifies the embedded prompt set; the worker writes it to
// jobs.state.prompts_version.
const PromptsVersion = "2026-10-1"

//go:embed *.md
var files embed.FS

// Vars are the template variables (06-prompts.md).
type Vars struct {
	Prompt       string // запрос клиента
	Files        string // список input/ строками «- input/… (размер)»
	Version      int    // номер версии (для revise — какую создаёт)
	PrevVersion  int    // предыдущая версия (для revise)
	Answer       string // ответ клиента
	RevisionFile string // REVISION-<n>.md
	Retry        bool   // авто-повтор этапа
}

var tmpl = template.Must(template.ParseFS(files, "*.md"))

// Render renders one of the templates: draft, verify, revise, answer, continue.
func Render(name string, v Vars) (string, error) {
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name+".md", v); err != nil {
		return "", fmt.Errorf("prompts: render %s: %w", name, err)
	}
	return buf.String(), nil
}
