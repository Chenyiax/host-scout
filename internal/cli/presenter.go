package cli

import (
	"fmt"
	"io"
	"time"

	"host-scout/internal/agent"
)

// turnPresenter is the sole writer of turn and tool activity. The approver
// retains ownership of the y/N question and input, but not its surrounding
// activity layout.
type turnPresenter struct {
	out      io.Writer
	textOpen bool
	stream   safeStream
	finished bool
}

func (p *turnPresenter) endText() {
	if p.textOpen {
		fmt.Fprint(p.out, p.stream.write("", true))
		fmt.Fprintln(p.out)
		p.textOpen = false
	}
}

func (p *turnPresenter) OnEvent(event agent.Event) {
	switch event.Kind {
	case agent.TurnStarted:
		p.textOpen = false
		p.stream = safeStream{}
		p.finished = false
	case agent.ModelRequested:
		p.endText()
		fmt.Fprintf(p.out, "\nWaiting for model response (step %d). Ctrl+C cancels; enter a follow-up when this turn ends.\n", event.Step)
	case agent.TextDelta:
		if !p.textOpen {
			fmt.Fprint(p.out, "\nassistant> ")
			p.textOpen = true
		}
		fmt.Fprint(p.out, p.stream.write(event.Text, false))
	case agent.ToolProposed:
		p.endText()
		fmt.Fprintf(p.out, "\nProposed %s (call=%s); not executed.\n", safeText(event.Tool), safeText(event.CallID))
	case agent.ApprovalRequested:
		p.endText()
		fmt.Fprintf(p.out, "\nWaiting for approval (call=%s).\n", safeText(event.CallID))
		printApprovalDetails(p.out, event.Purpose, event.Argv)
	case agent.ToolStarted:
		p.endText()
		fmt.Fprintf(p.out, "\nRunning %s (call=%s). Ctrl+C cancels.\n", safeText(event.Tool), safeText(event.CallID))
	case agent.ToolFinished:
		p.endText()
		if event.Evidence == nil {
			return
		}
		e := event.Evidence
		fmt.Fprintf(p.out, "\n%s (call=%s): %s", safeText(e.Tool), safeText(e.CallID), e.Result.Status)
		if e.Result.Code != "" {
			fmt.Fprintf(p.out, " (%s)", safeText(e.Result.Code))
		}
		fmt.Fprintf(p.out, ", record %s\n", safeText(e.ID))
		if args, ok := evidenceCommandArguments(*e); ok {
			printApprovalDetails(p.out, args.Purpose, args.Argv)
		}
		if e.StartedAt.IsZero() {
			fmt.Fprintln(p.out, "Not executed; no host observation.")
		} else {
			fmt.Fprintf(p.out, "Duration: %s\n", e.FinishedAt.Sub(e.StartedAt).Round(time.Millisecond))
			if e.Result.ExitCode != nil {
				fmt.Fprintf(p.out, "Exit code: %d\n", *e.Result.ExitCode)
			}
			if e.Result.Output != "" {
				preview, omitted := outputPreview(e.Result.Output)
				fmt.Fprintf(p.out, "Output preview:\n%s\n", preview)
				if omitted || e.Result.Truncated {
					fmt.Fprintf(p.out, "Preview abbreviated; use /evidence %s for available detail.\n", safeText(e.ID))
				}
			}
		}
	case agent.TurnFinished:
		p.endText()
		if !p.finished && event.Err == nil {
			fmt.Fprintln(p.out, "Turn completed.")
		}
		p.finished = true
	}
}
