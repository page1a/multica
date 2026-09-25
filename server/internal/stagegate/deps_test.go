package stagegate

import "testing"

func TestClassifyPromotesABlankDescription(t *testing.T) {
	promote, hold := Classify(2, []Item{
		{ID: "a", Stage: 1, HasStage: true, Status: "done"},
		{ID: "b", Title: "做第二阶段", Stage: 2, HasStage: true, Status: "backlog"},
	})
	if len(hold) != 0 || len(promote) != 1 || promote[0].ID != "b" {
		t.Fatalf("promote=%v hold=%v", promote, hold)
	}
}

func TestClassifyHoldsAnUnresolvedDependency(t *testing.T) {
	_, hold := Classify(2, []Item{
		{ID: "a", Identifier: "DENE-1", Stage: 1, HasStage: true, Status: "done"},
		{ID: "b", Description: "依赖 DENE-9 的接口", Stage: 2, HasStage: true, Status: "backlog"},
	})
	if len(hold) != 1 {
		t.Fatalf("hold=%v, want the unknown identifier kept back", hold)
	}
}

func TestClassifyPromotesADependencyThatIsDone(t *testing.T) {
	promote, hold := Classify(2, []Item{
		{ID: "a", Identifier: "DENE-1", Stage: 1, HasStage: true, Status: "done"},
		{ID: "b", Description: "depends on DENE-1", Stage: 2, HasStage: true, Status: "backlog"},
	})
	if len(hold) != 0 || len(promote) != 1 {
		t.Fatalf("promote=%v hold=%v", promote, hold)
	}
}

func TestClassifyHoldsALaterStageAndAConflict(t *testing.T) {
	_, hold := Classify(2, []Item{
		{ID: "a", Description: "等阶段 3 完成后再做", Stage: 2, HasStage: true, Status: "backlog"},
		{ID: "c", Description: "与父票拆分有冲突，先确认", Stage: 2, HasStage: true, Status: "backlog"},
		{ID: "d", Description: "没有额外依赖，按阶段走", Stage: 2, HasStage: true, Status: "backlog"},
	})
	if len(hold) != 2 {
		t.Fatalf("hold=%v, want the later stage and the conflict, not the explicit no-extra line", hold)
	}
}

func TestClassifyIgnoresItemsThatAreNotWaiting(t *testing.T) {
	promote, hold := Classify(2, []Item{
		{ID: "gone", Stage: 2, HasStage: true, Status: "todo", Description: "依赖 DENE-9"},
		{ID: "other", Stage: 3, HasStage: true, Status: "backlog"},
	})
	if len(promote) != 0 || len(hold) != 0 {
		t.Fatalf("promote=%v hold=%v, want neither", promote, hold)
	}
}
