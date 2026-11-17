package main

import (
	"errors"
	"testing"

	"github.com/Elysia4319/envlift/internal/plan"
)

func TestPlanErrorExitCode(t *testing.T) {
	derr := &plan.DecisionErrors{Issues: []plan.DecisionIssue{{
		Index: 1, Code: plan.CodeInvalidDecisionParam, Reason: "bad",
	}}}
	if got := planErrorExitCode(derr); got != exitRefused {
		t.Fatalf("DecisionErrors 应返回 exitRefused=%d，实际 %d", exitRefused, got)
	}
	if got := planErrorExitCode(errors.New("other")); got != exitError {
		t.Fatalf("普通错误应返回 exitError=%d，实际 %d", exitError, got)
	}
}
