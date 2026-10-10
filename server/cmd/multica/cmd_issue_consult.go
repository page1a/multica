package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

var issueConsultCmd = &cobra.Command{
	Use:   "consult <id> --question-file <path>",
	Short: "Ask a strong-tier seat one question and wait for its advice (DENE-1721)",
	Long: `Ask a stronger model for advice while you keep working the issue.

The server picks a seat on the strongest tier (one that fits the issue's
direction first, never you), hands it the issue's background and your
question, and returns its answer here. The advisor only reads: it changes no
code or issue, and you stay the executor. Each consult shows on the issue's
timeline with the question, the answer and its cost.

When to ask (a ticket gets a few, 3 by default — spend them well):
  - before you start, with your plan: "here is how I would do it, what am I missing?"
  - before you deliver, with your diff or result: "review this before I close"
  - when you are stuck between two approaches you cannot judge yourself
Do not ask for things you can look up, and do not consult instead of working.

The advisor sees the issue, its state card and your question — not your
working copy. Paste the code, diff or error that matters into the question,
best through --question-file.

The command waits for the answer (--wait, 15 minutes by default). If the
wait runs out, resume with --resume <consult-id>; the answer is not lost.
A refusal (limit reached, no strong seat, all of them busy or out of quota)
comes back at once with the reason: carry on with your own judgment.

Examples:
  multica issue consult MUL-123 --question-file ./ask.md
  multica issue consult MUL-123 --question "Index on (issue_id, created_at) or a partial one?"
  multica issue consult MUL-123 --resume <consult-id> --output json`,
	Args: cobra.ExactArgs(1),
	RunE: runIssueConsult,
}

func init() {
	addIssueConsultFlags(issueConsultCmd)
	issueCmd.AddCommand(issueConsultCmd)
}

func addIssueConsultFlags(cmd *cobra.Command) {
	cmd.Flags().String("question", "", "The question, with the context the advisor needs")
	cmd.Flags().Bool("question-stdin", false, "Read the question from stdin")
	cmd.Flags().String("question-file", "", "Read the question from a UTF-8 file inside the workdir")
	cmd.Flags().Bool("allow-external-file", false, "Allow --question-file to point outside the working directory")
	cmd.Flags().String("resume", "", "Wait again for an earlier consult instead of asking a new one")
	cmd.Flags().Duration("wait", 15*time.Minute, "How long to wait for the answer")
	cmd.Flags().String("output", "table", "Output format: table or json")
}

// consultPollInterval is how often the CLI checks for the answer.
var consultPollInterval = 3 * time.Second

type consultResult struct {
	ID              string `json:"id"`
	IssueID         string `json:"issue_id"`
	Status          string `json:"status"`
	Question        string `json:"question"`
	Answer          string `json:"answer,omitempty"`
	FailureReason   string `json:"failure_reason,omitempty"`
	AdvisorAgentID  string `json:"advisor_agent_id"`
	AdvisorName     string `json:"advisor_name"`
	TokensUsed      int64  `json:"tokens_used"`
	DurationSeconds int64  `json:"duration_seconds"`
}

type consultRefusalBody struct {
	Code  string `json:"code"`
	Error string `json:"error"`
	Limit int    `json:"limit,omitempty"`
}

// consultRefusal pulls the server's structured "no" out of an HTTP error.
func consultRefusal(err error) (consultRefusalBody, bool) {
	var httpErr *cli.HTTPError
	if !errors.As(err, &httpErr) || (httpErr.StatusCode != 403 && httpErr.StatusCode != 409) {
		return consultRefusalBody{}, false
	}
	var body consultRefusalBody
	if json.Unmarshal([]byte(httpErr.Body), &body) != nil || !strings.HasPrefix(body.Code, "consult_") {
		return consultRefusalBody{}, false
	}
	return body, true
}

func runIssueConsult(cmd *cobra.Command, args []string) error {
	resumeID, _ := cmd.Flags().GetString("resume")
	question, hasQuestion, err := resolveTextFlag(cmd, "question")
	if err != nil {
		return err
	}
	if resumeID == "" && (!hasQuestion || strings.TrimSpace(question) == "") {
		return fmt.Errorf("--question, --question-stdin or --question-file is required (or --resume <consult-id>)")
	}
	if resumeID != "" && hasQuestion {
		return fmt.Errorf("--resume waits for an earlier consult; drop the question flags")
	}
	jsonOut := false
	if format, _ := cmd.Flags().GetString("output"); format == "json" {
		jsonOut = true
	}
	wait, _ := cmd.Flags().GetDuration("wait")

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	issueRef, err := resolveIssueRef(ctx, client, args[0])
	cancel()
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}
	base := "/api/issues/" + url.PathEscape(issueRef.ID) + "/consults"

	var res consultResult
	if resumeID == "" {
		ctx, cancel := cli.APIContext(context.Background())
		err := client.PostJSON(ctx, base, map[string]any{"question": question}, &res)
		cancel()
		if refusal, ok := consultRefusal(err); ok {
			if jsonOut {
				return cli.PrintJSON(os.Stdout, map[string]any{"status": "refused", "code": refusal.Code, "error": refusal.Error, "limit": refusal.Limit})
			}
			fmt.Printf("Consult refused (%s): %s\n", refusal.Code, refusal.Error)
			return nil
		}
		if err != nil {
			return fmt.Errorf("consult: %w", err)
		}
		if !jsonOut {
			fmt.Fprintf(os.Stderr, "Asked %s (consult %s); waiting for the answer...\n", res.AdvisorName, res.ID)
		}
	} else {
		res.ID = resumeID
	}

	deadline := time.Now().Add(wait)
	for res.Status != "answered" && res.Status != "failed" {
		if res.Status != "" && !time.Now().Before(deadline) {
			break
		}
		if res.Status != "" {
			time.Sleep(consultPollInterval)
		}
		ctx, cancel := cli.APIContext(context.Background())
		err := client.GetJSON(ctx, base+"/"+url.PathEscape(res.ID), &res)
		cancel()
		if err != nil {
			return fmt.Errorf("check consult %s: %w", res.ID, err)
		}
	}

	if jsonOut {
		return cli.PrintJSON(os.Stdout, res)
	}
	switch res.Status {
	case "answered":
		fmt.Printf("Advice from %s (%d tokens, %ds):\n\n%s\n", res.AdvisorName, res.TokensUsed, res.DurationSeconds, res.Answer)
	case "failed":
		fmt.Printf("Consult %s got no answer: %s\nIt does not count against the limit. Carry on with your own judgment.\n", res.ID, res.FailureReason)
	default:
		fmt.Printf("Still waiting for %s. Resume with: multica issue consult %s --resume %s\n", res.AdvisorName, args[0], res.ID)
	}
	return nil
}
