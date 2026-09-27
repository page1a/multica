package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var planCmd = &cobra.Command{
	Use:   "plan",
	Short: "Build a staged issue tree from a plan file",
}

var planApplyCmd = &cobra.Command{
	Use:   "apply <plan.yaml>",
	Short: "Create a parent and its staged sub-issues from one plan file, in one transaction",
	Long: "One command for building a staged plan. The server creates the parent and\n" +
		"every sub-issue in one transaction, with executors seated at create time:\n" +
		"stage 1 lands in todo and starts, later stages wait in backlog until\n" +
		"`multica issue stage advance <parent>`.\n\n" +
		"Plan file (YAML):\n\n" +
		"  key: dene-858-ritual-commands     # required; identity for re-apply\n" +
		"  project: <project id>              # optional, new parent only\n" +
		"  parent:                            # a new coordinating parent ...\n" +
		"    title: ...\n" +
		"    description: ...\n" +
		"    assignee: <name>                 # the seat woken when a stage closes\n" +
		"    priority: high\n" +
		"  # parent: DENE-858                 # ... or an existing issue\n" +
		"  children:\n" +
		"    - key: close                     # optional, defaults to the title\n" +
		"      title: ...\n" +
		"      stage: 1                       # required, >= 1\n" +
		"      assignee: <name>\n" +
		"      description: ...\n\n" +
		"Applying the same plan again creates only the nodes that do not exist yet;\n" +
		"existing issues are never rewritten. The response lists the whole tree with\n" +
		"a created flag per issue.",
	Args: exactArgs(1),
	RunE: runPlanApply,
}

var issueStageCmd = &cobra.Command{
	Use:   "stage",
	Short: "Work with the stages of a parent issue",
}

var issueStageAdvanceCmd = &cobra.Command{
	Use:   "advance <parent>",
	Short: "Promote the next stage once every stage below it is terminal",
	Long: "Checks the parent's staged sub-issues. When every stage below the next one\n" +
		"is terminal (done or cancelled), the next stage's backlog sub-issues move to\n" +
		"todo and their executors are woken. When the current stage still has open\n" +
		"work, nothing is written and the refusal names each ticket it waits on.",
	Args: exactArgs(1),
	RunE: runIssueStageAdvance,
}

func init() {
	rootCmd.AddCommand(planCmd)
	planCmd.AddCommand(planApplyCmd)
	planApplyCmd.Flags().String("output", "table", "Output format: table or json")

	issueCmd.AddCommand(issueStageCmd)
	issueStageCmd.AddCommand(issueStageAdvanceCmd)
	issueStageAdvanceCmd.Flags().String("output", "table", "Output format: table or json")
}

// planFileNode is one issue in a plan file. Assignee is a name (or id) the
// CLI resolves the same way `issue create --assignee` does.
type planFileNode struct {
	Key         string `yaml:"key"`
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	Priority    string `yaml:"priority"`
	Stage       *int32 `yaml:"stage"`
	Assignee    string `yaml:"assignee"`
}

type planFile struct {
	Key      string         `yaml:"key"`
	Project  string         `yaml:"project"`
	Parent   yaml.Node      `yaml:"parent"`
	Children []planFileNode `yaml:"children"`
}

// parsePlanFile reads the YAML. `parent` is either a mapping (a new parent)
// or a scalar (an existing issue reference); the scalar is returned as ref.
func parsePlanFile(data []byte) (planFile, *planFileNode, string, error) {
	var plan planFile
	if err := yaml.Unmarshal(data, &plan); err != nil {
		return planFile{}, nil, "", fmt.Errorf("parse plan: %w", err)
	}
	if strings.TrimSpace(plan.Key) == "" {
		return planFile{}, nil, "", fmt.Errorf("plan needs a key: it is how a second apply finds the first one's issues")
	}
	switch plan.Parent.Kind {
	case yaml.MappingNode:
		var parent planFileNode
		if err := plan.Parent.Decode(&parent); err != nil {
			return planFile{}, nil, "", fmt.Errorf("parse plan parent: %w", err)
		}
		return plan, &parent, "", nil
	case yaml.ScalarNode:
		if ref := strings.TrimSpace(plan.Parent.Value); ref != "" {
			return plan, nil, ref, nil
		}
	}
	return planFile{}, nil, "", fmt.Errorf("plan needs a parent: a mapping for a new parent, or an existing issue like DENE-858")
}

func runPlanApply(cmd *cobra.Command, args []string) error {
	data, err := os.ReadFile(args[0])
	if err != nil {
		return fmt.Errorf("read plan: %w", err)
	}
	plan, parent, parentRef, err := parsePlanFile(data)
	if err != nil {
		return err
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	toNode := func(n planFileNode) (map[string]any, error) {
		node := map[string]any{"key": n.Key, "title": n.Title, "description": n.Description, "priority": n.Priority}
		if n.Stage != nil {
			node["stage"] = *n.Stage
		}
		if strings.TrimSpace(n.Assignee) != "" {
			kind, id, err := resolveAssignee(ctx, client, n.Assignee, issueAssigneeKinds)
			if err != nil {
				return nil, fmt.Errorf("%q: resolve assignee: %w", n.Title, err)
			}
			node["assignee_type"], node["assignee_id"] = kind, id
		}
		return node, nil
	}
	body := map[string]any{"key": plan.Key}
	if parent != nil {
		node, err := toNode(*parent)
		if err != nil {
			return err
		}
		body["parent"] = node
		if plan.Project != "" {
			project, err := resolveProjectID(ctx, client, plan.Project)
			if err != nil {
				return fmt.Errorf("resolve project: %w", err)
			}
			body["project_id"] = project.ID
		}
	} else {
		ref, err := resolveIssueRef(ctx, client, parentRef)
		if err != nil {
			return fmt.Errorf("resolve parent: %w", err)
		}
		body["parent_issue_id"] = ref.ID
	}
	children := make([]map[string]any, 0, len(plan.Children))
	for _, c := range plan.Children {
		node, err := toNode(c)
		if err != nil {
			return err
		}
		children = append(children, node)
	}
	body["children"] = children

	var out planApplyResult
	if err := client.PostJSON(ctx, "/api/issues/plan-apply", body, &out); err != nil {
		return fmt.Errorf("apply plan: %w", err)
	}
	if format, _ := cmd.Flags().GetString("output"); format == "json" {
		return cli.PrintJSON(os.Stdout, out)
	}
	printPlanTree(os.Stdout, out)
	return nil
}

type planApplyIssue struct {
	ID           string  `json:"id"`
	Identifier   string  `json:"identifier"`
	Title        string  `json:"title"`
	Status       string  `json:"status"`
	Stage        *int32  `json:"stage,omitempty"`
	AssigneeType *string `json:"assignee_type,omitempty"`
	AssigneeID   *string `json:"assignee_id,omitempty"`
	Created      bool    `json:"created"`
}

type planApplyResult struct {
	Key      string           `json:"key"`
	Parent   planApplyIssue   `json:"parent"`
	Children []planApplyIssue `json:"children"`
	Created  int              `json:"created"`
	Existing int              `json:"existing"`
}

func printPlanTree(w *os.File, out planApplyResult) {
	mark := func(created bool) string {
		if created {
			return "created"
		}
		return "existing"
	}
	fmt.Fprintf(w, "Plan %s: %d created, %d already existed\n", out.Key, out.Created, out.Existing)
	fmt.Fprintf(w, "%s  %s  [%s, %s]\n", out.Parent.Identifier, out.Parent.Title, out.Parent.Status, mark(out.Parent.Created))
	for _, c := range out.Children {
		stage := "-"
		if c.Stage != nil {
			stage = fmt.Sprint(*c.Stage)
		}
		fmt.Fprintf(w, "  stage %s  %s  %s  [%s, %s]\n", stage, c.Identifier, c.Title, c.Status, mark(c.Created))
	}
}

func runIssueStageAdvance(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	ref, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve issue: %w", err)
	}
	var out map[string]any
	if err := client.PostJSON(ctx, "/api/issues/"+url.PathEscape(ref.ID)+"/stage-advance", map[string]any{}, &out); err != nil {
		return fmt.Errorf("advance stage: %w", err)
	}
	if format, _ := cmd.Flags().GetString("output"); format == "json" {
		return cli.PrintJSON(os.Stdout, out)
	}
	fmt.Println(out["message"])
	return nil
}
