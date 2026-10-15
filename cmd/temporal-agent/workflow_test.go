package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"

	"github.com/yedamao/mini-agent/internal/llm"
)

// The test environment runs the workflow in memory with mocked activities,
// so we can check the loop's logic without a server or a model.
func TestAgentWorkflowRunsToolThenAnswers(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()
	var a *Activities

	toolUse := &llm.Response{StopReason: "tool_use", Content: []llm.Block{
		{Type: "tool_use", ID: "t1", Name: "get_time", Input: json.RawMessage(`{}`)},
	}}
	final := &llm.Response{StopReason: "end_turn", Content: []llm.Block{{Type: "text", Text: "It is noon."}}}
	env.OnActivity(a.CallModel, mock.Anything, mock.Anything, mock.Anything).Return(toolUse, nil).Once()
	env.OnActivity(a.CallModel, mock.Anything, mock.Anything, mock.Anything).Return(final, nil).Once()
	env.OnActivity(a.RunTool, mock.Anything, ToolCall{Dir: "/tmp", Name: "get_time", Input: json.RawMessage(`{}`)}).
		Return("Thu, 15 Oct 2026 12:00:00 CST", nil).Once()

	env.ExecuteWorkflow(AgentWorkflow, Input{Task: "what time is it?", Dir: "/tmp"})
	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatalf("workflow did not complete: %v", env.GetWorkflowError())
	}
	var answer string
	env.GetWorkflowResult(&answer)
	if answer != "It is noon." {
		t.Errorf("answer = %q", answer)
	}
	env.AssertExpectations(t)
}

func TestAgentWorkflowStepLimit(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()
	var a *Activities
	loop := &llm.Response{StopReason: "tool_use", Content: []llm.Block{
		{Type: "tool_use", ID: "t", Name: "get_time", Input: json.RawMessage(`{}`)},
	}}
	env.OnActivity(a.CallModel, mock.Anything, mock.Anything, mock.Anything).Return(loop, nil)
	env.OnActivity(a.RunTool, mock.Anything, mock.Anything).Return("noon", nil)

	env.ExecuteWorkflow(AgentWorkflow, Input{Task: "loop", MaxSteps: 3})
	if env.GetWorkflowError() == nil {
		t.Fatal("want a step-limit error")
	}
}
