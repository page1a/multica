package handler

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/statecard"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestHandoffSummaryFromComment(t *testing.T) {
	got := handoffSummaryFromComment("[@孙悟饭](mention://agent/abc) 接着做\n\n  前端部分")
	if got != "@孙悟饭 接着做 前端部分" {
		t.Fatalf("summary = %q", got)
	}
	long := handoffSummaryFromComment(strings.Repeat("字", statecard.MaxBatonLen*2))
	if len([]rune(long)) > statecard.MaxBatonLen+1 {
		t.Fatalf("summary not clipped: %d runes", len([]rune(long)))
	}
}

func TestHandoffNamesAgent(t *testing.T) {
	var id pgtype.UUID
	_ = id.Scan("11111111-1111-1111-1111-111111111111")
	agent := db.Agent{ID: id, Name: "Gohan"}
	for to, want := range map[string]bool{
		"gohan":                                true,
		"孙悟天、Gohan":                            true,
		"11111111-1111-1111-1111-111111111111": true,
		"Goten":                                false,
		"":                                     false,
	} {
		if got := handoffNamesAgent(to, agent); got != want {
			t.Fatalf("handoffNamesAgent(%q) = %v, want %v", to, got, want)
		}
	}
}

func TestBuildChatHandoffOpening(t *testing.T) {
	rows := []db.ChatMessage{ // newest first
		{Role: "assistant", Content: "改好了"},
		{Role: "user", Content: "把按钮改成蓝色"},
	}
	out := buildChatHandoffOpening("按钮配色", "孙悟空", "sess-1", 12, "帮我看看首页", rows)
	for _, want := range []string{
		"接手聊天「按钮配色」，原来是 孙悟空 在聊，共 12 条。",
		"multica chat history --session sess-1",
		"开头：帮我看看首页",
		"最近 2 条：\n- 我：把按钮改成蓝色\n- 孙悟空：改好了",
		"先读上面的摘要，接着往下做。",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("opening missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(buildChatHandoffOpening("", "", "s", 0, "", nil), "接手聊天「未命名」，原来是 上一个智能体 在聊") {
		t.Fatal("empty title and agent name need fallbacks")
	}
	if strings.Contains(buildChatHandoffOpening("t", "a", "s", 2, "把按钮改成蓝色", rows), "开头：") {
		t.Fatal("the opening line repeats only when the recent messages do not reach it")
	}
}
