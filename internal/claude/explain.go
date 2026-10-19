package claude

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/egrif/runnr/internal/runbook"
)

// instructions say what runnr is and how its runbooks behave, so steps are
// read the way runnr runs them. It's the same for every runbook.
const instructions = `You review and explain steps of runnr runbooks for the operators who run them: concise, concrete, practical.

How runnr runs a runbook (YAML):
- Steps run in order, each in its own shell (the runbook's shell:, else sh), so shell variables and cd don't carry over between steps on their own.
- Values carry over as the run's state: every input, and every variable a step publishes (outputs / secret_outputs name shell variables captured when the step ends; capture saves stdout; lines written to $RUNNR_OUTPUT / $RUNNR_SECRET). Later steps get all of them as {{.name}} templates and as $name environment variables. secret_outputs are masked everywhere and not stored.
- Steps marked always (usually the first, "setup" steps) run on every run, resume and later start, to rebuild that state. Most steps depend on what they publish.
- run is a script; manual is instructions for a person; checks are commands that each pass on exit 0 (repeat retries the group; retry_checks says what a retry reruns); ways are alternative ways to do a step (one preferred). tty means it needs a keyboard; background runs alongside later steps until stop_after.
- confirm asks first; dangerous needs a typed double check when out of sequence; when skips a step unless its template is true; continue_on_error carries on after a failure.
- Templates are Go templates ({{.x}}, quote, default, eq...). Library functions (listed below the runbook) are shell functions every step can call.
- maintenance_window marks where production is down (window: open/close steps).

When asked about a step:
- Say what it does and why, in terms of this runbook.
- Point out what it relies on from earlier steps (especially the setup steps): which values, where they come from, and what breaks if they're wrong or missing.
- Say what to check before and after, and any risks (destructive or irreversible actions, wrong target, credentials, timing, idempotence on retry).
- Suggest concrete improvements where they'd help, as changed YAML or shell.
Use short headings and bullets; no preamble. Don't repeat the step back in full. Values shown as **** or ****-masked are secrets: don't guess them.`

// RunbookContext is everything about a runbook a question might need: its
// YAML, an index of its steps and the library functions it can call, with
// credentials masked. raw is the runbook as written (with its comments);
// when empty, the runbook is formatted.
func RunbookContext(rb *runbook.Runbook, raw []byte, libs []*runbook.Library) string {
	if len(raw) == 0 {
		raw, _ = runbook.Format(rb)
	}
	m := runbook.NewMasker(rb)
	var b strings.Builder
	fmt.Fprintf(&b, "# Runbook %q\n\n```yaml\n%s\n```\n\n## Steps (numbered as runnr numbers them)\n", rb.Name, strings.TrimRight(m.Mask(string(raw)), "\n"))
	section := ""
	for i, st := range rb.Steps {
		if st.Section != section {
			section = st.Section
			fmt.Fprintf(&b, "Section: %s\n", section)
		}
		var tags []string
		tags = append(tags, st.Kind())
		for _, t := range []struct {
			on   bool
			name string
		}{{st.Always, "always (setup)"}, {st.Confirm, "confirm"}, {st.Dangerous, "dangerous"}, {st.Background, "background"}, {st.Window != "", "window " + st.Window}} {
			if t.on {
				tags = append(tags, t.name)
			}
		}
		if outs := append(append([]string{}, st.Outputs...), st.SecretOutputs...); len(outs) > 0 {
			tags = append(tags, "publishes "+strings.Join(outs, ", "))
		}
		fmt.Fprintf(&b, "%d. %s [%s]\n", i+1, st.Name, strings.Join(tags, "; "))
	}
	fns := map[string]runbook.Function{}
	for _, l := range libs {
		for n, f := range l.Functions {
			fns[n] = f
		}
	}
	if len(fns) > 0 {
		b.WriteString("\n## Library functions\n\n```sh\n" + m.Mask(runbook.FunctionScript(fns)) + "```\n")
	}
	return b.String()
}

// Question asks about step (1-based; 0 for the whole runbook), with the
// operator's own question if they have one.
func Question(rb *runbook.Runbook, step int, ask string) (string, error) {
	ask = strings.TrimSpace(ask)
	if step == 0 {
		if ask != "" {
			return "About this runbook as a whole: " + ask, nil
		}
		return "Review this runbook as a whole: what it does, how the steps depend on each other, the riskiest steps, gaps (missing checks, rollback, idempotence), and the most useful improvements.", nil
	}
	if step < 1 || step > len(rb.Steps) {
		return "", fmt.Errorf("no step %d (the runbook has %d)", step, len(rb.Steps))
	}
	name := rb.Steps[step-1].Name
	if ask != "" {
		return fmt.Sprintf("About step %d (%q): %s", step, name, ask), nil
	}
	return fmt.Sprintf("Explain step %d (%q).", step, name), nil
}

// Explain answers a question about a runbook step. The instructions and the
// runbook context come first and are marked for caching, so asking about
// other steps of the same runbook soon after reads them from the
// provider's cache.
//
// history is the conversation so far about this step: a follow-up goes
// after it, as is (the step was named in the first question). With a
// local cache, the same question about an unchanged runbook (same model,
// context and conversation) is answered from it unless fresh is set.
func Explain(ctx context.Context, p Provider, cache Cache, rb *runbook.Runbook, raw []byte, libs []*runbook.Library, step int, ask string, history []Message, fresh bool, onText func(string)) (Usage, error) {
	q, err := Question(rb, step, ask)
	if err != nil {
		return Usage{}, err
	}
	if len(history) > 0 && strings.TrimSpace(ask) != "" {
		q = strings.TrimSpace(ask)
	}
	system := []Block{{Text: instructions}, {Text: RunbookContext(rb, raw, libs), Cache: true}}
	key := answerKey(p, system, history, q)
	if cache != nil && !fresh {
		if a, ok := cache.GetAnswer(key); ok {
			onText(a)
			return Usage{Local: true}, nil
		}
	}
	var answer strings.Builder
	u, err := p.Stream(ctx, system, history, q, 2000, func(t string) { answer.WriteString(t); onText(t) })
	if err == nil && cache != nil && answer.Len() > 0 {
		_ = cache.PutAnswer(key, p.Name()+" "+p.ModelName(), answer.String())
	}
	return u, err
}

// answerKey fingerprints everything an answer depends on.
func answerKey(p Provider, system []Block, history []Message, q string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00", p.Name(), p.ModelName())
	for _, b := range system {
		fmt.Fprintf(h, "%d\x00%s\x00", len(b.Text), b.Text)
	}
	for _, m := range history {
		fmt.Fprintf(h, "%s\x00%d\x00%s\x00", m.Role, len(m.Content), m.Content)
	}
	fmt.Fprintf(h, "%s", q)
	return hex.EncodeToString(h.Sum(nil))
}
