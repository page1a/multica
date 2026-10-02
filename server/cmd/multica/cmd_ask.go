package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

var askCmd = &cobra.Command{Use: "ask", Short: "Ask and answer option questions"}
var askCreateCmd = &cobra.Command{Use: "create", Aliases: []string{"ask"}, Short: "Create an option question", RunE: runAskCreate}
var askListCmd = &cobra.Command{Use: "list", Short: "List questions", RunE: runAskList}
var askGetCmd = &cobra.Command{Use: "get <id>", Args: exactArgs(1), Short: "Read a question", RunE: runAskGet}
var askAnswerCmd = &cobra.Command{Use: "answer <id>", Args: exactArgs(1), Short: "Answer a question", RunE: runAskAnswer}

func init() {
	askCmd.GroupID = groupCore
	askCmd.AddCommand(askCreateCmd, askListCmd, askGetCmd, askAnswerCmd)
	askCreateCmd.Flags().String("title", "", "Question title")
	askCreateCmd.Flags().String("questions-file", "", "JSON file containing an array of questions")
	askCreateCmd.Flags().String("issue", "", "Optional issue ID")
	askCreateCmd.Flags().String("mode", "needs_you", "needs_you or side_question")
	askCreateCmd.Flags().String("output", "table", "Output format: table or json")
	askListCmd.Flags().String("status", "open", "Filter: open, answered, cancelled, or all")
	askListCmd.Flags().String("output", "table", "Output format: table or json")
	askGetCmd.Flags().String("output", "table", "Output format: table or json")
	askAnswerCmd.Flags().String("answers-file", "", "JSON object mapping question index or id to option id/text")
	askAnswerCmd.Flags().String("output", "table", "Output format: table or json")
	rootCmd.AddCommand(askCmd)
}

func askJSON(path string, out any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
func runAskCreate(cmd *cobra.Command, _ []string) error {
	title, _ := cmd.Flags().GetString("title")
	path, _ := cmd.Flags().GetString("questions-file")
	if strings.TrimSpace(title) == "" || path == "" {
		return fmt.Errorf("--title and --questions-file are required")
	}
	var questions []map[string]any
	if err := askJSON(path, &questions); err != nil {
		return fmt.Errorf("read questions: %w", err)
	}
	body := map[string]any{"title": title, "questions": questions}
	if v, _ := cmd.Flags().GetString("issue"); v != "" {
		body["issue_id"] = v
	}
	if v, _ := cmd.Flags().GetString("mode"); v != "" {
		body["mode"] = v
	}
	c, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var out map[string]any
	if err := c.PostJSON(ctx, "/api/asks", body, &out); err != nil {
		return fmt.Errorf("create ask: %w", err)
	}
	return printAskOutput(cmd, out)
}
func runAskList(cmd *cobra.Command, _ []string) error {
	c, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	status, _ := cmd.Flags().GetString("status")
	path := "/api/asks"
	if status != "" && status != "all" {
		path += "?status=" + url.QueryEscape(status)
	}
	var out map[string]any
	if err := c.GetJSON(ctx, path, &out); err != nil {
		return fmt.Errorf("list asks: %w", err)
	}
	return printAskOutput(cmd, out)
}
func runAskGet(cmd *cobra.Command, args []string) error {
	c, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var out map[string]any
	if err := c.GetJSON(ctx, "/api/asks/"+url.PathEscape(args[0]), &out); err != nil {
		return fmt.Errorf("get ask: %w", err)
	}
	return printAskOutput(cmd, out)
}
func runAskAnswer(cmd *cobra.Command, args []string) error {
	path, _ := cmd.Flags().GetString("answers-file")
	if path == "" {
		return fmt.Errorf("--answers-file is required")
	}
	var answers map[string]string
	if err := askJSON(path, &answers); err != nil {
		return fmt.Errorf("read answers: %w", err)
	}
	c, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var out map[string]any
	if err := c.PostJSON(ctx, "/api/asks/"+url.PathEscape(args[0])+"/answer", map[string]any{"answers": answers}, &out); err != nil {
		return fmt.Errorf("answer ask: %w", err)
	}
	return printAskOutput(cmd, out)
}
func printAskOutput(cmd *cobra.Command, out map[string]any) error {
	format, _ := cmd.Flags().GetString("output")
	if format == "json" {
		return cli.PrintJSON(os.Stdout, out)
	}
	if asks, ok := out["asks"].([]any); ok {
		for _, a := range asks {
			m, _ := a.(map[string]any)
			fmt.Printf("%v\t%v\t%v\n", m["id"], m["status"], m["title"])
		}
		return nil
	}
	fmt.Printf("%v\n", out["status"])
	return nil
}
