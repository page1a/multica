package daemon

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/agent"
)

// The provider table is what keeps shared mode honest: a provider is listed
// only with a verified sidecar-free route for the brief, and everything else
// is refused before Prepare instead of started with no brief and no skills.
func TestSharedModeBriefDelivery(t *testing.T) {
	t.Parallel()

	if got := sharedModeBriefDelivery("claude"); got != sharedBriefViaClaudeFlags {
		t.Errorf("claude = %v, want sharedBriefViaClaudeFlags (spike-verified --add-dir / --append-system-prompt-file)", got)
	}
	if got := sharedModeBriefDelivery("codex"); got != sharedBriefViaCodexHome {
		t.Errorf("codex = %v, want sharedBriefViaCodexHome (CODEX_HOME/AGENTS.md)", got)
	}
	if err := sharedModeProviderSupported("codex"); err != nil {
		t.Errorf("sharedModeProviderSupported(codex) = %v, want nil", err)
	}
	if got := sharedModeBriefDelivery("opencode"); got != sharedBriefViaOpencodeConfigDir {
		t.Errorf("opencode = %v, want sharedBriefViaOpencodeConfigDir (OPENCODE_CONFIG_DIR)", got)
	}
	if err := sharedModeProviderSupported("opencode"); err != nil {
		t.Errorf("sharedModeProviderSupported(opencode) = %v, want nil", err)
	}
	// DSH (and grok) load AGENTS.md from cwd in the non-shared path, so they
	// are not in providerNeedsInlineSystemPrompt. Shared mode still has to
	// prepend SystemPrompt because the brief file sits under the sidecar.
	if got := sharedModeBriefDelivery("dsh"); got != sharedBriefInline {
		t.Errorf("dsh = %v, want sharedBriefInline (execute prompt prepend)", got)
	}
	if err := sharedModeProviderSupported("dsh"); err != nil {
		t.Errorf("sharedModeProviderSupported(dsh) = %v, want nil", err)
	}
	if got := sharedModeBriefDelivery("grok"); got != sharedBriefInline {
		t.Errorf("grok = %v, want sharedBriefInline", got)
	}
	// These stdin-prompt backends prepend SystemPrompt (withSystemPrompt);
	// omp shares the pi backend.
	for _, p := range []string{"pi", "omp", "codearts"} {
		if got := sharedModeBriefDelivery(p); got != sharedBriefInline {
			t.Errorf("%s = %v, want sharedBriefInline (stdin prompt prepend)", p, got)
		}
	}
	// Qwen Code reads QWEN.md only from the cwd; its backend prepends
	// SystemPrompt onto the stdin prompt, so shared mode rides that route.
	if got := sharedModeBriefDelivery("qwen"); got != sharedBriefInline {
		t.Errorf("qwen = %v, want sharedBriefInline (stdin prompt prepend)", got)
	}
	if err := sharedModeProviderSupported("qwen"); err != nil {
		t.Errorf("sharedModeProviderSupported(qwen) = %v, want nil", err)
	}
	if got := sharedModeBriefDelivery("cursor"); got != sharedBriefViaCursorAddDir {
		t.Errorf("cursor = %v, want sharedBriefViaCursorAddDir (--add-dir skills + stdin brief)", got)
	}
	if err := sharedModeProviderSupported("cursor"); err != nil {
		t.Errorf("sharedModeProviderSupported(cursor) = %v, want nil", err)
	}
	if got := sharedModeBriefDelivery("antigravity"); got != sharedBriefViaAntigravityAddDir {
		t.Errorf("antigravity = %v, want sharedBriefViaAntigravityAddDir (--add-dir AGENTS.md + skills)", got)
	}
	if err := sharedModeProviderSupported("antigravity"); err != nil {
		t.Errorf("sharedModeProviderSupported(antigravity) = %v, want nil", err)
	}
	// Every provider that already runs on the inline brief in production must
	// keep working in shared mode, since inline delivery needs no cwd file.
	for _, p := range []string{"openclaw", "kimi", "traecli", "qwenpaw"} {
		if !providerNeedsInlineSystemPrompt(p) {
			t.Fatalf("%s no longer needs an inline brief; update this test's premise", p)
		}
		if got := sharedModeBriefDelivery(p); got != sharedBriefInline {
			t.Errorf("%s = %v, want sharedBriefInline", p, got)
		}
		if err := sharedModeProviderSupported(p); err != nil {
			t.Errorf("sharedModeProviderSupported(%s) = %v, want nil", p, err)
		}
	}
	// Disk-only readers stay refused until their own route is verified.
	// mcode ignores ExecOptions.SystemPrompt and only reads cwd AGENTS.md,
	// so it must not pass the shared-mode gate (DENE-125).
	for _, p := range []string{"hermes", "copilot", "mcode", "reasonix", "deveco", "", "made-up"} {
		if got := sharedModeBriefDelivery(p); got != sharedBriefUnsupported {
			t.Errorf("%q = %v, want sharedBriefUnsupported", p, got)
		}
		err := sharedModeProviderSupported(p)
		if err == nil {
			t.Errorf("sharedModeProviderSupported(%q) = nil, want a refusal", p)
			continue
		}
		if !strings.Contains(err.Error(), "shared") || !strings.Contains(err.Error(), "in_place") {
			t.Errorf("refusal for %q should name the mode and an alternative, got %q", p, err)
		}
	}
}

func TestSharedModeBriefRoot(t *testing.T) {
	t.Parallel()

	workDir := "/user/project"
	sidecar := "/env/sidecar"
	codexHome := "/env/codex-home"

	got, err := sharedModeBriefRoot("claude", sidecar, "", workDir)
	if err != nil {
		t.Fatalf("claude shared: %v", err)
	}
	if got != sidecar {
		t.Errorf("claude shared brief root = %q, want sidecar %q", got, sidecar)
	}

	got, err = sharedModeBriefRoot("codex", sidecar, codexHome, workDir)
	if err != nil {
		t.Fatalf("codex shared: %v", err)
	}
	if got != codexHome {
		t.Errorf("codex shared brief root = %q, want CODEX_HOME %q", got, codexHome)
	}

	if _, err := sharedModeBriefRoot("codex", sidecar, "", workDir); err == nil {
		t.Fatal("codex shared with empty CODEX_HOME: want an error, got nil")
	} else if !strings.Contains(err.Error(), "CODEX_HOME") {
		t.Errorf("empty CODEX_HOME error = %q, want it to name CODEX_HOME", err)
	}

	got, err = sharedModeBriefRoot("codex", "", codexHome, workDir)
	if err != nil {
		t.Fatalf("codex non-shared: %v", err)
	}
	if got != workDir {
		t.Errorf("codex non-shared brief root = %q, want cwd %q (MUL-5392)", got, workDir)
	}

	got, err = sharedModeBriefRoot("opencode", sidecar, "", workDir)
	if err != nil {
		t.Fatalf("opencode shared: %v", err)
	}
	if got != sidecar {
		t.Errorf("opencode shared brief root = %q, want sidecar %q", got, sidecar)
	}

	got, err = sharedModeBriefRoot("opencode", "", "", workDir)
	if err != nil {
		t.Fatalf("opencode non-shared: %v", err)
	}
	if got != workDir {
		t.Errorf("opencode non-shared brief root = %q, want cwd %q", got, workDir)
	}

	got, err = sharedModeBriefRoot("cursor", sidecar, "", workDir)
	if err != nil {
		t.Fatalf("cursor shared: %v", err)
	}
	if got != sidecar {
		t.Errorf("cursor shared brief root = %q, want sidecar %q", got, sidecar)
	}

	got, err = sharedModeBriefRoot("antigravity", sidecar, "", workDir)
	if err != nil {
		t.Fatalf("antigravity shared: %v", err)
	}
	if got != sidecar {
		t.Errorf("antigravity shared brief root = %q, want sidecar %q", got, sidecar)
	}
}

func TestSharedModeSkillsDir(t *testing.T) {
	t.Parallel()

	sidecar := "/env/sidecar"
	codexHome := "/env/codex-home"

	if got := sharedModeSkillsDir("codex", sidecar, codexHome); got != filepath.Join(codexHome, "skills") {
		t.Errorf("codex shared skills dir = %q, want CODEX_HOME/skills", got)
	}
	if got := sharedModeSkillsDir("claude", sidecar, ""); !strings.HasSuffix(got, filepath.Join(".claude", "skills")) {
		t.Errorf("claude shared skills dir = %q, want sidecar .claude/skills", got)
	}
	if got := sharedModeSkillsDir("codex", "", codexHome); got != "" {
		t.Errorf("codex non-shared skills dir = %q, want empty (native discovery)", got)
	}
	if got := sharedModeSkillsDir("opencode", sidecar, ""); got != filepath.Join(sidecar, ".opencode", "skills") {
		t.Errorf("opencode shared skills dir = %q, want sidecar .opencode/skills", got)
	}
	if got := sharedModeSkillsDir("opencode", "", ""); got != "" {
		t.Errorf("opencode non-shared skills dir = %q, want empty (native discovery)", got)
	}
	if got := sharedModeSkillsDir("cursor", sidecar, ""); got != filepath.Join(sidecar, ".cursor", "skills") {
		t.Errorf("cursor shared skills dir = %q, want sidecar .cursor/skills", got)
	}
	if got := sharedModeSkillsDir("antigravity", sidecar, ""); got != filepath.Join(sidecar, ".agents", "skills") {
		t.Errorf("antigravity shared skills dir = %q, want sidecar .agents/skills", got)
	}
}

func TestSharedModeOpencodeConfigDir(t *testing.T) {
	t.Parallel()

	sidecar := "/env/sidecar"
	if got := sharedModeOpencodeConfigDir("opencode", sidecar); got != sidecar {
		t.Errorf("opencode shared OPENCODE_CONFIG_DIR = %q, want sidecar %q", got, sidecar)
	}
	if got := sharedModeOpencodeConfigDir("opencode", ""); got != "" {
		t.Errorf("opencode non-shared OPENCODE_CONFIG_DIR = %q, want empty", got)
	}
	if got := sharedModeOpencodeConfigDir("claude", sidecar); got != "" {
		t.Errorf("claude OPENCODE_CONFIG_DIR = %q, want empty", got)
	}
	if got := sharedModeOpencodeConfigDir("codex", sidecar); got != "" {
		t.Errorf("codex OPENCODE_CONFIG_DIR = %q, want empty", got)
	}
}

func TestSharedModeBriefOverlay(t *testing.T) {
	t.Parallel()

	sidecar := "/env/sidecar"

	claude := sharedModeBriefOverlayFor("claude", sidecar)
	if claude.BriefInline {
		t.Error("claude overlay BriefInline = true, want false (--append-system-prompt-file)")
	}
	if !containsPair(claude.ExtraArgs, "--add-dir", sidecar) {
		t.Errorf("claude ExtraArgs = %v, want --add-dir sidecar", claude.ExtraArgs)
	}
	if !containsPair(claude.ExtraArgs, "--append-system-prompt-file", filepath.Join(sidecar, "CLAUDE.md")) {
		t.Errorf("claude ExtraArgs = %v, want --append-system-prompt-file sidecar/CLAUDE.md", claude.ExtraArgs)
	}

	cursor := sharedModeBriefOverlayFor("cursor", sidecar)
	if !cursor.BriefInline {
		t.Error("cursor overlay BriefInline = false, want true (stdin brief; --add-dir does not load AGENTS.md)")
	}
	if !containsPair(cursor.ExtraArgs, "--add-dir", sidecar) {
		t.Errorf("cursor ExtraArgs = %v, want --add-dir sidecar", cursor.ExtraArgs)
	}
	for _, a := range cursor.ExtraArgs {
		if a == "--plugin-dir" || a == "--append-system-prompt-file" {
			t.Errorf("cursor ExtraArgs = %v, must not use %s", cursor.ExtraArgs, a)
		}
	}

	agy := sharedModeBriefOverlayFor("antigravity", sidecar)
	if agy.BriefInline {
		t.Error("antigravity overlay BriefInline = true, want false (--add-dir loads AGENTS.md)")
	}
	if !containsPair(agy.ExtraArgs, "--add-dir", sidecar) {
		t.Errorf("antigravity ExtraArgs = %v, want --add-dir sidecar", agy.ExtraArgs)
	}

	if got := sharedModeBriefOverlayFor("cursor", ""); len(got.ExtraArgs) != 0 || got.BriefInline {
		t.Errorf("non-shared cursor overlay = %+v, want empty", got)
	}
	if got := sharedModeBriefOverlayFor("codex", sidecar); len(got.ExtraArgs) != 0 || got.BriefInline {
		t.Errorf("codex overlay = %+v, want empty (CODEX_HOME/AGENTS.md)", got)
	}
}

func containsPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

// Every supported runtime needs an explicit shared-mode decision: a brief
// route, or a recorded reason for refusing. Without this a new backend falls
// silently into the refusal and users only find out when a task fails.
func TestSharedModeEverySupportedTypeHasADecision(t *testing.T) {
	t.Parallel()

	for _, p := range agent.SupportedTypes {
		routed := sharedModeBriefDelivery(p) != sharedBriefUnsupported
		reason, refused := sharedModeRefusedProviders[p]
		switch {
		case routed && refused:
			t.Errorf("%s has a shared-mode route but is also listed as refused (%q)", p, reason)
		case !routed && !refused:
			t.Errorf("%s has no shared-mode route and no refusal reason; add it to sharedModeBriefDelivery or sharedModeRefusedProviders", p)
		}
	}
	for p := range sharedModeRefusedProviders {
		if !agent.IsSupportedType(p) {
			t.Errorf("sharedModeRefusedProviders lists %q, which is not a supported type", p)
		}
	}
}
