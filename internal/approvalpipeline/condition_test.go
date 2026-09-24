package approvalpipeline

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/withHumanAI/withHuman/approvalblock"
	"github.com/withHumanAI/withHuman/blockruntime"
	"github.com/withHumanAI/withHuman/domain"
)

func conditionRequest(arguments string) domain.ApprovalRequest {
	return domain.ApprovalRequest{
		ID:        "request-1",
		AgentID:   "agent-1",
		AgentSlug: "billing",
		Tool:      "stripe.refund",
		Arguments: json.RawMessage(arguments),
	}
}

func TestEvaluateRequestCondition(t *testing.T) {
	agent := domain.Agent{ID: "agent-1", Name: "billing-agent", AgentSlug: "billing"}
	tests := []struct {
		name      string
		condition approvalblock.Condition
		server    string
		arguments string
		want      bool
	}{
		{
			name:      "tool matches",
			condition: approvalblock.Field("/request/tool").Equals("stripe.refund"),
			arguments: `{}`,
			want:      true,
		},
		{
			name: "arguments match",
			condition: approvalblock.All(
				approvalblock.Field("/request/arguments/amount_cents").GreaterThan(1000),
				approvalblock.Field("/request/arguments/customer/labels").Contains("priority"),
			),
			arguments: `{"amount_cents":2500,"customer":{"labels":["priority"]}}`,
			want:      true,
		},
		{
			name: "agent matches",
			condition: approvalblock.All(
				approvalblock.Field("/agent/slug").Equals("billing"),
				approvalblock.Field("/agent/name").StartsWith("billing"),
			),
			arguments: `{}`,
			want:      true,
		},
		{
			name:      "server matches",
			condition: approvalblock.Field("/request/server").Equals("stripe"),
			server:    "stripe",
			arguments: `{}`,
			want:      true,
		},
		{
			name:      "built-in tool has no server",
			condition: approvalblock.Field("/request/server").Equals("stripe"),
			arguments: `{}`,
			want:      false,
		},
		{
			name:      "not exists names a built-in tool",
			condition: approvalblock.Field("/request/server").NotExists(),
			arguments: `{}`,
			want:      true,
		},
		{
			name:      "false result",
			condition: approvalblock.Field("/request/arguments/amount_cents").GreaterThan(1000),
			arguments: `{"amount_cents":500}`,
			want:      false,
		},
		{
			name:      "missing field is false",
			condition: approvalblock.Field("/request/arguments/missing").Equals("x"),
			arguments: `{}`,
			want:      false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := conditionRequest(test.arguments)
			request.Server = test.server
			got, err := EvaluateRequestCondition(context.Background(), test.condition, request, agent)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestEvaluateRequestConditionSurfacesErrors(t *testing.T) {
	agent := domain.Agent{ID: "agent-1", Name: "billing-agent", AgentSlug: "billing"}

	t.Run("runtime type error", func(t *testing.T) {
		condition := approvalblock.Field("/request/arguments/amount_cents").LessThan(1000)
		_, err := EvaluateRequestCondition(context.Background(), condition, conditionRequest(`{"amount_cents":"900"}`), agent)
		if code, ok := blockruntime.ErrorCodeOf(err); !ok || code != ErrorEvaluation {
			t.Fatalf("got %v, want an evaluation error", err)
		}
	})

	t.Run("malformed arguments", func(t *testing.T) {
		condition := approvalblock.Field("/request/tool").Equals("stripe.refund")
		if _, err := EvaluateRequestCondition(context.Background(), condition, conditionRequest(`[]`), agent); err == nil {
			t.Fatal("malformed arguments did not error")
		}
	})

	t.Run("invalid condition", func(t *testing.T) {
		condition := approvalblock.Condition{ID: "bad", Field: "/request/tool", Operator: "no_such_operator"}
		if _, err := EvaluateRequestCondition(context.Background(), condition, conditionRequest(`{}`), agent); err == nil {
			t.Fatal("invalid condition did not error")
		}
	})
}
