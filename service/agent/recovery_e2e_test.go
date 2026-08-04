//go:build integration

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	hub "GopherAI/common/mcphub"
	agentdao "GopherAI/dao/agent"
	"GopherAI/model"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

const (
	agentE2EDSNEvironment         = "GOPHERAI_AGENT_E2E_MYSQL_DSN"
	agentE2EHelperEnvironment     = "GOPHERAI_AGENT_E2E_HELPER"
	agentE2ETaskIDEnvironment     = "GOPHERAI_AGENT_E2E_TASK_ID"
	agentE2EModeEnvironment       = "GOPHERAI_AGENT_E2E_MODE"
	agentE2EExpectedEnvironment   = "GOPHERAI_AGENT_E2E_EXPECTED_STATUS"
	agentE2ECallbackEnvironment   = "GOPHERAI_AGENT_E2E_CALLBACK_URL"
	agentE2EIdempotentEnvironment = "GOPHERAI_AGENT_E2E_IDEMPOTENT"
)

// TestAgentProcessCrashRecovery verifies the durable recovery path across a
// real OS process boundary. It is intentionally opt-in because it needs a
// dedicated, disposable MySQL database; it never runs as part of the normal
// unit-test suite.
//
// Run it with:
//
//	GOPHERAI_AGENT_E2E_MYSQL_DSN='user:pass@tcp(127.0.0.1:13306)/gopherai_agent_e2e?parseTime=true&loc=UTC' \
//	  go test -tags integration ./service/agent -run TestAgentProcessCrashRecovery -count=1
func TestAgentProcessCrashRecovery(t *testing.T) {
	if os.Getenv(agentE2EHelperEnvironment) == "1" {
		t.Skip("helper process is executed by TestAgentCrashRecoveryHelper")
	}
	dsn := strings.TrimSpace(os.Getenv(agentE2EDSNEvironment))
	if dsn == "" {
		t.Skipf("set %s to a dedicated disposable MySQL database to run process-recovery E2E tests", agentE2EDSNEvironment)
	}

	database := openAgentE2EDatabase(t, dsn)
	if err := database.AutoMigrate(&model.AgentTask{}, &model.AgentStep{}); err != nil {
		t.Fatalf("migrate Agent recovery test tables: %v", err)
	}

	for _, test := range []struct {
		name           string
		idempotent     bool
		expectedStatus string
		expectedStep   string
	}{
		{
			name:           "idempotent tool is replayed after a killed worker",
			idempotent:     true,
			expectedStatus: model.AgentTaskStatusSucceeded,
			expectedStep:   model.AgentStepStatusSucceeded,
		},
		{
			name:           "non idempotent tool is paused for review after a killed worker",
			idempotent:     false,
			expectedStatus: model.AgentTaskStatusRequiresReview,
			expectedStep:   model.AgentStepStatusExecutionUnknown,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			taskID := uuid.NewString()
			createAgentE2ETask(t, database, taskID)
			t.Cleanup(func() {
				_ = database.Where("task_id = ?", taskID).Delete(&model.AgentStep{}).Error
				_ = database.Where("id = ?", taskID).Delete(&model.AgentTask{}).Error
			})

			toolStarted := make(chan struct{}, 1)
			callback := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Query().Get("task_id") == taskID {
					select {
					case toolStarted <- struct{}{}:
					default:
					}
				}
				writer.WriteHeader(http.StatusNoContent)
			}))
			defer callback.Close()

			first := startAgentE2EHelper(t, dsn, taskID, "crash", callback.URL, test.idempotent, "")
			select {
			case <-toolStarted:
			case <-time.After(15 * time.Second):
				terminateAgentE2EHelper(first)
				t.Fatalf("first worker never entered the durable running tool state; output:\n%s", first.output.String())
			}

			runningTool := waitForAgentE2ERunningTool(t, database, taskID)
			if err := first.command.Process.Kill(); err != nil {
				t.Fatalf("kill first worker: %v", err)
			}
			_ = first.command.Wait() // A forced process kill is the expected outcome.

			// Let the persisted lease expire before starting the new worker. The
			// second process calls StartWorker, whose first poll recovers stale
			// tasks from MySQL before it claims newly pending work.
			time.Sleep(250 * time.Millisecond)
			second := startAgentE2EHelper(t, dsn, taskID, "recover", "", test.idempotent, test.expectedStatus)
			if err := second.command.Wait(); err != nil {
				t.Fatalf("recovery worker failed: %v\noutput:\n%s", err, second.output.String())
			}

			assertAgentE2ERecovered(t, database, taskID, test.expectedStatus, test.expectedStep, test.idempotent, runningTool)
		})
	}
}

// TestAgentCrashRecoveryHelper runs in a separate test-binary process. The
// crash mode blocks only after toolNode has durably marked the step running;
// its parent then sends Process.Kill, which exercises the same abrupt process
// loss as an actual worker crash.
func TestAgentCrashRecoveryHelper(t *testing.T) {
	if os.Getenv(agentE2EHelperEnvironment) != "1" {
		return
	}
	dsn := strings.TrimSpace(os.Getenv(agentE2EDSNEvironment))
	taskID := strings.TrimSpace(os.Getenv(agentE2ETaskIDEnvironment))
	mode := strings.TrimSpace(os.Getenv(agentE2EModeEnvironment))
	if dsn == "" || taskID == "" || (mode != "crash" && mode != "recover") {
		t.Fatalf("invalid Agent recovery helper configuration")
	}

	database := openAgentE2EDatabase(t, dsn)
	store := agentdao.NewStore(database)
	idempotent := os.Getenv(agentE2EIdempotentEnvironment) == "true"
	callbackURL := strings.TrimSpace(os.Getenv(agentE2ECallbackEnvironment))
	expected := strings.TrimSpace(os.Getenv(agentE2EExpectedEnvironment))
	service := newAgentE2EService(t, store, taskID, mode, callbackURL, idempotent)

	if mode == "crash" {
		// This call intentionally never returns under normal operation: the
		// parent kills this process after the gateway signals tool entry.
		if err := service.ProcessTask(context.Background(), taskID); err != nil {
			t.Fatalf("crash worker returned before being killed: %v", err)
		}
		t.Fatal("crash worker completed before being killed")
	}

	workerCtx, cancel := context.WithCancel(context.Background())
	if err := service.StartWorker(workerCtx); err != nil {
		t.Fatalf("start recovery worker: %v", err)
	}
	defer func() {
		cancel()
		waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer waitCancel()
		_ = service.WaitWorker(waitCtx)
	}()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		task, err := store.GetTaskByID(context.Background(), taskID)
		if err == nil && task.Status == expected {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	task, err := store.GetTaskByID(context.Background(), taskID)
	if err != nil {
		t.Fatalf("read recovered task: %v", err)
	}
	t.Fatalf("recovered task status = %q, want %q", task.Status, expected)
}

type agentE2EHelper struct {
	command *exec.Cmd
	output  *bytes.Buffer
}

func startAgentE2EHelper(t *testing.T, dsn, taskID, mode, callbackURL string, idempotent bool, expectedStatus string) *agentE2EHelper {
	t.Helper()
	output := new(bytes.Buffer)
	command := exec.Command(os.Args[0], "-test.run=^TestAgentCrashRecoveryHelper$", "-test.v")
	command.Stdout = output
	command.Stderr = output
	command.Env = append(os.Environ(),
		agentE2EHelperEnvironment+"=1",
		agentE2EDSNEvironment+"="+dsn,
		agentE2ETaskIDEnvironment+"="+taskID,
		agentE2EModeEnvironment+"="+mode,
		agentE2ECallbackEnvironment+"="+callbackURL,
		agentE2EIdempotentEnvironment+"="+fmt.Sprintf("%t", idempotent),
		agentE2EExpectedEnvironment+"="+expectedStatus,
	)
	if err := command.Start(); err != nil {
		t.Fatalf("start %s helper: %v", mode, err)
	}
	return &agentE2EHelper{command: command, output: output}
}

func terminateAgentE2EHelper(helper *agentE2EHelper) {
	if helper == nil || helper.command == nil || helper.command.Process == nil {
		return
	}
	_ = helper.command.Process.Kill()
	_ = helper.command.Wait()
}

func openAgentE2EDatabase(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open Agent E2E MySQL database: %v", err)
	}
	return database
}

func createAgentE2ETask(t *testing.T, database *gorm.DB, taskID string) {
	t.Helper()
	store := agentdao.NewStore(database)
	task := &model.AgentTask{
		ID:       taskID,
		UserName: "agent-e2e",
		Goal:     "exercise a durable Agent recovery",
		ModelID:  "test.chat",
	}
	step := &model.AgentStep{ID: uuid.NewString(), TaskID: taskID, UserName: task.UserName}
	if err := store.CreateTaskWithInitialStep(context.Background(), task, step); err != nil {
		t.Fatalf("create Agent E2E task: %v", err)
	}
}

type agentE2ERunningTool struct {
	checkpointVersion uint64
	operationID       string
}

func waitForAgentE2ERunningTool(t *testing.T, database *gorm.DB, taskID string) agentE2ERunningTool {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var task model.AgentTask
		if err := database.Preload("Steps").Where("id = ?", taskID).First(&task).Error; err == nil {
			for _, step := range task.Steps {
				if step.Kind == model.AgentStepKindTool && step.Status == model.AgentStepStatusRunning {
					if task.Status != model.AgentTaskStatusRunning || task.LeaseUntil == nil {
						t.Fatalf("tool started without a running leased task: %#v", task)
					}
					if step.OperationID == "" {
						t.Fatal("tool entered running state without a durable operation ID")
					}
					return agentE2ERunningTool{checkpointVersion: task.CheckpointVersion, operationID: step.OperationID}
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("tool step was not durably persisted as running before the process kill")
	return agentE2ERunningTool{}
}

func assertAgentE2ERecovered(t *testing.T, database *gorm.DB, taskID, expectedTaskStatus, expectedStepStatus string, idempotent bool, before agentE2ERunningTool) {
	t.Helper()
	var task model.AgentTask
	if err := database.Preload("Steps").Where("id = ?", taskID).First(&task).Error; err != nil {
		t.Fatalf("read Agent E2E result: %v", err)
	}
	var plan, tool *model.AgentStep
	for index := range task.Steps {
		switch task.Steps[index].Kind {
		case model.AgentStepKindPlan:
			plan = &task.Steps[index]
		case model.AgentStepKindTool:
			tool = &task.Steps[index]
		}
	}
	if task.Status != expectedTaskStatus || tool == nil || tool.Status != expectedStepStatus {
		t.Fatalf("recovery result task=%#v tool=%#v", task, tool)
	}
	if tool.OperationID != before.operationID {
		t.Fatalf("operation ID changed across recovery: before=%q after=%q", before.operationID, tool.OperationID)
	}
	if plan == nil || plan.Status != model.AgentStepStatusSucceeded || plan.Attempts != 1 {
		t.Fatalf("completed plan was not preserved exactly once: %#v", plan)
	}
	if idempotent {
		if tool.Attempts != 2 || task.FinalAnswer != "recovered answer" {
			t.Fatalf("idempotent tool was not replayed once after restart: task=%#v tool=%#v", task, tool)
		}
	} else if tool.Attempts != 1 || task.CurrentStepID != tool.ID {
		t.Fatalf("non-idempotent tool was replayed instead of being paused: task=%#v tool=%#v", task, tool)
	}
	if task.CheckpointVersion <= before.checkpointVersion {
		t.Fatalf("stale recovery did not invalidate the abandoned checkpoint: before=%d after=%d", before.checkpointVersion, task.CheckpointVersion)
	}
}

func newAgentE2EService(t *testing.T, store *agentdao.GormStore, taskID, mode, callbackURL string, idempotent bool) *Service {
	t.Helper()
	modelRuntime := &agentE2EModel{mode: mode, toolName: "e2e.create"}
	gateway := &agentE2EGateway{
		taskID:      taskID,
		callbackURL: callbackURL,
		definition: hub.ToolDefinition{
			Name:        "e2e.create",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`),
			Risk:        hub.RiskLow,
			Idempotent:  idempotent,
			Destructive: false,
			ReadOnly:    false,
		},
	}
	service, err := NewService(
		store,
		modelRuntime,
		gateway,
		realClock{},
		WithRegistry(graphTestRegistry(t)),
		WithWorkerOptions(WorkerOptions{
			ID:           "agent-e2e-" + mode,
			PollInterval: 10 * time.Millisecond,
			Lease:        100 * time.Millisecond,
			StaleAfter:   20 * time.Millisecond,
			BatchSize:    4,
		}),
	)
	if err != nil {
		t.Fatalf("create Agent E2E service: %v", err)
	}
	return service
}

type agentE2EModel struct {
	mode     string
	toolName string
}

func (runtime *agentE2EModel) Generate(context.Context, string, string, []*schema.Message) (*schema.Message, error) {
	if runtime.mode == "crash" {
		return &schema.Message{Role: schema.Assistant, Content: graphTestToolPlan(runtime.toolName)}, nil
	}
	return &schema.Message{Role: schema.Assistant, Content: "recovered answer"}, nil
}

type agentE2EGateway struct {
	taskID      string
	callbackURL string
	definition  hub.ToolDefinition
}

func (gateway *agentE2EGateway) Tools(context.Context) ([]hub.ToolDefinition, error) {
	return []hub.ToolDefinition{gateway.definition}, nil
}

func (gateway *agentE2EGateway) Call(ctx context.Context, request hub.InvokeRequest) (*hub.InvocationResult, error) {
	if gateway.callbackURL != "" {
		response, err := http.Post(gateway.callbackURL+"?task_id="+gateway.taskID, "text/plain", nil)
		if err != nil {
			return nil, err
		}
		_ = response.Body.Close()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(30 * time.Second):
			return nil, errors.New("Agent E2E crash helper was not killed")
		}
	}
	return &hub.InvocationResult{
		RequestID: "e2e-request-" + gateway.taskID,
		ToolName:  request.ToolName,
		Result:    &mcp.CallToolResult{},
	}, nil
}

func (*agentE2EGateway) CreateApproval(context.Context, string, string, map[string]any) (*hub.ApprovalChallenge, error) {
	return nil, errors.New("E2E tool does not require approval")
}

func (*agentE2EGateway) Approve(string, string) (*hub.IssuedApproval, error) {
	return nil, errors.New("E2E tool does not require approval")
}
