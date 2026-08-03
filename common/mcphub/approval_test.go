package mcphub

import (
	"testing"
	"time"
)

func TestApprovalTokenIsBoundAndSingleUse(t *testing.T) {
	manager := NewApprovalManager(time.Minute)
	tool := ToolDefinition{
		Name:             "demo.write",
		Risk:             RiskHigh,
		RequiresApproval: true,
		InputSchema:      []byte(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}`),
	}
	arguments := map[string]any{"value": "approved"}
	challenge, err := manager.Create("alice", tool, arguments)
	if err != nil {
		t.Fatalf("Create(): %v", err)
	}
	issued, err := manager.Approve("alice", challenge.ID)
	if err != nil {
		t.Fatalf("Approve(): %v", err)
	}
	if issued.Token == "" || issued.Token == hashApprovalToken(issued.Token) {
		t.Fatal("approval token was not issued as an opaque raw value")
	}
	if err := manager.Consume("alice", tool.Name, arguments, issued.Token); err != nil {
		t.Fatalf("Consume(first): %v", err)
	}
	if err := manager.Consume("alice", tool.Name, arguments, issued.Token); ErrorCode(err) != ErrorApprovalInvalid {
		t.Fatalf("Consume(second) = %v", err)
	}
}

func TestApprovalTokenMismatchConsumesToken(t *testing.T) {
	manager := NewApprovalManager(time.Minute)
	tool := ToolDefinition{
		Name:             "demo.write",
		Risk:             RiskMedium,
		RequiresApproval: true,
		InputSchema:      []byte(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}`),
	}
	approvedArgs := map[string]any{"value": "approved"}
	challenge, _ := manager.Create("alice", tool, approvedArgs)
	issued, _ := manager.Approve("alice", challenge.ID)
	if err := manager.Consume("alice", tool.Name, map[string]any{"value": "changed"}, issued.Token); ErrorCode(err) != ErrorApprovalInvalid {
		t.Fatalf("mismatched Consume() = %v", err)
	}
	if err := manager.Consume("alice", tool.Name, approvedArgs, issued.Token); ErrorCode(err) != ErrorApprovalInvalid {
		t.Fatalf("reused Consume() = %v", err)
	}
}
