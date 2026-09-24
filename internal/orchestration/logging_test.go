package orchestration

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/withHumanAI/withHuman/loggermdc"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/interceptor"
	temporallog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

func TestMDCReachesWorkflowActivityAndGlobalLogs(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	var output bytes.Buffer
	logger := loggermdc.Configure(slog.NewJSONHandler(&output, nil))
	var suite testsuite.WorkflowTestSuite
	suite.SetLogger(temporallog.NewStructuredLogger(logger))
	environment := suite.NewTestWorkflowEnvironment()
	environment.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{NewMDCInterceptor()}})
	setWorkflowMDC(t, environment, loggermdc.Fields{
		loggermdc.KeyRequestID: "start-request", loggermdc.KeyTraceID: "start-trace",
		loggermdc.KeyOrganizationID: "org-1", loggermdc.KeyAgentInstanceID: "agent-instance",
		loggermdc.Key("private"): "must-not-cross",
	})
	activityFn := func(ctx context.Context) error {
		activity.GetLogger(ctx).Info("activity SDK", "trace_id", "incorrect")
		logger.InfoContext(ctx, "activity injected")
		slog.WarnContext(ctx, "activity global")
		return nil
	}
	environment.RegisterActivity(activityFn)
	environment.ExecuteWorkflow(func(ctx workflow.Context) error {
		workflow.GetLogger(ctx).Info("workflow start")
		// Signals replace the caller identity rather than retaining the
		// initiating agent's fields. Exercise that same context switch.
		ctx = replaceWorkflowMDC(ctx, loggermdc.Fields{
			loggermdc.KeyRequestID: "decision-request", loggermdc.KeyTraceID: "decision-trace",
			loggermdc.KeyOrganizationID: "org-1", loggermdc.KeyActorID: "reviewer-1",
			loggermdc.KeyWorkflowID: workflow.GetInfo(ctx).WorkflowExecution.ID,
		})
		workflow.GetLogger(ctx).Warn("workflow decision")
		ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
		return workflow.ExecuteActivity(ctx, activityFn).Get(ctx, nil)
	})
	if err := environment.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{"workflow start": false, "workflow decision": false, "activity SDK": false, "activity injected": false, "activity global": false}
	decoder := json.NewDecoder(&output)
	for decoder.More() {
		var record map[string]any
		if err := decoder.Decode(&record); err != nil {
			t.Fatal(err)
		}
		message, _ := record["msg"].(string)
		if _, ok := wanted[message]; !ok {
			continue
		}
		wanted[message] = true
		request, trace := "decision-request", "decision-trace"
		if message == "workflow start" {
			request, trace = "start-request", "start-trace"
		} else if _, stale := record["agent_instance_id"]; stale {
			t.Fatalf("initiating agent leaked into decision logs: %#v", record)
		}
		if record["request_id"] != request || record["trace_id"] != trace || record["organization_id"] != "org-1" || record["workflow_id"] == nil {
			t.Fatalf("missing or stale MDC: %#v", record)
		}
		if _, leaked := record["private"]; leaked {
			t.Fatalf("private context crossed Temporal: %#v", record)
		}
	}
	for message, found := range wanted {
		if !found {
			t.Errorf("missing log %q", message)
		}
	}
}
