package service

import "testing"

func TestGoalBudgetThresholds(t *testing.T) {
	tests := []struct {
		name        string
		used, limit int64
		warning     bool
		exceeded    bool
	}{
		{"unlimited", 100, 0, false, false},
		{"below warning", 79, 100, false, false},
		{"at warning", 80, 100, true, false},
		{"at limit", 100, 100, true, true},
		{"over limit", 101, 100, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := budgetWarning(tt.used, tt.limit); got != tt.warning {
				t.Fatalf("budgetWarning(%d, %d) = %v, want %v", tt.used, tt.limit, got, tt.warning)
			}
			if got := budgetExceeded(tt.used, tt.limit); got != tt.exceeded {
				t.Fatalf("budgetExceeded(%d, %d) = %v, want %v", tt.used, tt.limit, got, tt.exceeded)
			}
		})
	}
}
