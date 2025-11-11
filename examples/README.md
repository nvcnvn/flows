```go
package main

import (
    "context"
    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/nvcnvn/flows"
)

func main() {
    // 1. Connect to PostgreSQL 18+
    pool, err := pgxpool.New(context.Background(), 
        "postgres://user:pass@localhost:5432/mydb?sslmode=disable")
    if err != nil {
        panic(err)
    }
    defer pool.Close()

    // 2. Create engine and set as global
    engine := flows.NewEngine(pool)
    flows.SetEngine(engine)

    // 3. Start worker
    worker := flows.NewWorker(pool, flows.WorkerConfig{
        Concurrency:   5,
        WorkflowNames: []string{"my-workflow"},
        PollInterval:  500 * time.Millisecond,
        TenantID:      myTenantID,
    })
    defer worker.Stop()

    go worker.Run(context.Background())

    // 4. Start workflow
    ctx := flows.WithTenantID(context.Background(), myTenantID)
    exec, err := flows.Start(ctx, MyWorkflow, &MyInput{...})
    if err != nil {
        panic(err)
    }

    // 5. Wait for result
    result, err := exec.Get(ctx)
}
```

## 📚 Real-World Example: Loan Approval System

Let's build a complete loan approval workflow with all features: activities, loops, conditionals, signals, sleep, and HTTP API.

### Step 1: Define Domain Types

```go
package main

import (
    "context"
    "fmt"
    "time"
    "github.com/google/uuid"
    "github.com/nvcnvn/flows"
)

// Workflow Input/Output
type LoanApplicationInput struct {
    ApplicantID     string  `json:"applicant_id"`
    Amount          float64 `json:"amount"`
    Purpose         string  `json:"purpose"`
    CreditScore     int     `json:"credit_score"`
}

type LoanApplicationOutput struct {
    ApplicationID   string    `json:"application_id"`
    Status          string    `json:"status"` // approved, rejected, pending
    ApprovedAmount  float64   `json:"approved_amount"`
    InterestRate    float64   `json:"interest_rate"`
    ApprovalSteps   []string  `json:"approval_steps"`
    ProcessingTime  time.Duration `json:"processing_time"`
    DecisionReason  string    `json:"decision_reason"`
}

// Activity Types
type CreditCheckInput struct {
    ApplicantID string `json:"applicant_id"`
    CreditScore int    `json:"credit_score"`
}

type CreditCheckOutput struct {
    Score      int    `json:"score"`
    RiskLevel  string `json:"risk_level"` // low, medium, high
    ReportID   string `json:"report_id"`
}

type DocumentVerificationInput struct {
    ApplicantID  string   `json:"applicant_id"`
    DocumentIDs  []string `json:"document_ids"`
}

type DocumentVerificationOutput struct {
    AllValid     bool     `json:"all_valid"`
    InvalidDocs  []string `json:"invalid_docs"`
    VerifiedBy   string   `json:"verified_by"`
}

type RiskAssessmentInput struct {
    Amount      float64 `json:"amount"`
    RiskLevel   string  `json:"risk_level"`
    Purpose     string  `json:"purpose"`
}

type RiskAssessmentOutput struct {
    Approved        bool    `json:"approved"`
    MaxAmount       float64 `json:"max_amount"`
    InterestRate    float64 `json:"interest_rate"`
    RequiresManager bool    `json:"requires_manager"`
}

type FraudCheckInput struct {
    ApplicantID string  `json:"applicant_id"`
    Amount      float64 `json:"amount"`
}

type FraudCheckOutput struct {
    Suspicious bool   `json:"suspicious"`
    Score      int    `json:"score"`
    Reason     string `json:"reason"`
}

type NotificationInput struct {
    ApplicantID string `json:"applicant_id"`
    Message     string `json:"message"`
    Channel     string `json:"channel"` // email, sms
}

type NotificationOutput struct {
    Sent      bool   `json:"sent"`
    MessageID string `json:"message_id"`
}

// Signal Types
type ManagerApprovalSignal struct {
    Approved   bool   `json:"approved"`
    ManagerID  string `json:"manager_id"`
    Comments   string `json:"comments"`
}
```

### Step 2: Define Activities

Activities are the building blocks that perform actual work. Each activity can be retried independently.

```go
// Activity 1: Credit Check (might fail temporarily)
var CreditCheckActivity = flows.NewActivity(
    "credit-check",
    func(ctx context.Context, input *CreditCheckInput) (*CreditCheckOutput, error) {
        fmt.Printf("[CreditCheck] Checking credit for applicant: %s\n", input.ApplicantID)
        
        // Simulate external API call that might fail
        time.Sleep(500 * time.Millisecond)
        
        // Determine risk level based on credit score
        var riskLevel string
        switch {
        case input.CreditScore >= 750:
            riskLevel = "low"
        case input.CreditScore >= 650:
            riskLevel = "medium"
        default:
            riskLevel = "high"
        }

        return &CreditCheckOutput{
            Score:     input.CreditScore,
            RiskLevel: riskLevel,
            ReportID:  fmt.Sprintf("CREDIT-%s", uuid.New().String()[:8]),
        }, nil
    },
    flows.RetryPolicy{
        InitialInterval: 1 * time.Second,
        MaxInterval:     10 * time.Second,
        BackoffFactor:   2.0,
        MaxAttempts:     3,
        Jitter:          0.1,
    },
)

// Activity 2: Document Verification (loops through documents)
var DocumentVerificationActivity = flows.NewActivity(
    "document-verification",
    func(ctx context.Context, input *DocumentVerificationInput) (*DocumentVerificationOutput, error) {
        fmt.Printf("[DocVerification] Verifying documents for: %s\n", input.ApplicantID)
        
        time.Sleep(300 * time.Millisecond)
        
        // Simulate document validation
        invalidDocs := []string{}
        for _, docID := range input.DocumentIDs {
            // Random validation logic
            if len(docID) < 5 {
                invalidDocs = append(invalidDocs, docID)
            }
        }

        return &DocumentVerificationOutput{
            AllValid:    len(invalidDocs) == 0,
            InvalidDocs: invalidDocs,
            VerifiedBy:  fmt.Sprintf("VERIFIER-%d", time.Now().Unix()%1000),
        }, nil
    },
    flows.DefaultRetryPolicy,
)

// Activity 3: Risk Assessment
var RiskAssessmentActivity = flows.NewActivity(
    "risk-assessment",
    func(ctx context.Context, input *RiskAssessmentInput) (*RiskAssessmentOutput, error) {
        fmt.Printf("[RiskAssessment] Assessing risk for amount: $%.2f\n", input.Amount)
        
        time.Sleep(400 * time.Millisecond)

        // Business logic for risk assessment
        requiresManager := false
        approved := true
        maxAmount := input.Amount
        interestRate := 5.0

        switch input.RiskLevel {
        case "low":
            interestRate = 4.5
            if input.Amount > 500000 {
                requiresManager = true
            }
        case "medium":
            interestRate = 6.5
            if input.Amount > 300000 {
                requiresManager = true
            }
        case "high":
            interestRate = 9.5
            if input.Amount > 100000 {
                requiresManager = true
                maxAmount = 100000 // Cap the amount
            } else {
                approved = false
            }
        }

        return &RiskAssessmentOutput{
            Approved:        approved,
            MaxAmount:       maxAmount,
            InterestRate:    interestRate,
            RequiresManager: requiresManager,
        }, nil
    },
    flows.DefaultRetryPolicy,
)

// Activity 4: Fraud Check
var FraudCheckActivity = flows.NewActivity(
    "fraud-check",
    func(ctx context.Context, input *FraudCheckInput) (*FraudCheckOutput, error) {
        fmt.Printf("[FraudCheck] Checking fraud for applicant: %s\n", input.ApplicantID)
        
        time.Sleep(600 * time.Millisecond)

        // Simple fraud detection logic
        suspicious := false
        score := 100
        reason := "Clean record"

        // Suspicious if amount is too high
        if input.Amount > 1000000 {
            suspicious = true
            score = 45
            reason = "Unusually high loan amount"
        }

        return &FraudCheckOutput{
            Suspicious: suspicious,
            Score:      score,
            Reason:     reason,
        }, nil
    },
    flows.RetryPolicy{
        InitialInterval: 2 * time.Second,
        MaxInterval:     20 * time.Second,
        BackoffFactor:   2.0,
        MaxAttempts:     5,
        Jitter:          0.2,
    },
)

// Activity 5: Send Notification
var SendNotificationActivity = flows.NewActivity(
    "send-notification",
    func(ctx context.Context, input *NotificationInput) (*NotificationOutput, error) {
        fmt.Printf("[Notification] Sending %s to %s: %s\n", 
            input.Channel, input.ApplicantID, input.Message)
        
        time.Sleep(200 * time.Millisecond)

        return &NotificationOutput{
            Sent:      true,
            MessageID: fmt.Sprintf("MSG-%s", uuid.New().String()[:8]),
        }, nil
    },
    flows.DefaultRetryPolicy,
)
```

### Step 3: Define the Workflow

The workflow orchestrates activities with business logic, loops, conditionals, and signals.

```go
var LoanApplicationWorkflow = flows.New(
    "loan-application-workflow",
    1,
    func(ctx *flows.Context[LoanApplicationInput]) (*LoanApplicationOutput, error) {
        input := ctx.Input()
        
        // Generate deterministic application ID using UUIDv7
        applicationID := ctx.UUIDv7()
        fmt.Printf("\n=== Starting Loan Application: %s ===\n", applicationID)
        
        // Track processing steps
        approvalSteps := []string{}
        startTime := ctx.Time()
        
        // Step 1: Run fraud check and credit check in parallel concept
        // (In reality, we run sequentially but demonstrate multiple activities)
        
        // 1a. Fraud Check
        approvalSteps = append(approvalSteps, "fraud-check-initiated")
        fraudResult, err := flows.ExecuteActivity(ctx, FraudCheckActivity, &FraudCheckInput{
            ApplicantID: input.ApplicantID,
            Amount:      input.Amount,
        })
        if err != nil {
            return nil, fmt.Errorf("fraud check failed: %w", err)
        }
        
        if fraudResult.Suspicious {
            approvalSteps = append(approvalSteps, "fraud-detected")
            
            // Send rejection notification
            _, _ = flows.ExecuteActivity(ctx, SendNotificationActivity, &NotificationInput{
                ApplicantID: input.ApplicantID,
                Message:     fmt.Sprintf("Application rejected due to: %s", fraudResult.Reason),
                Channel:     "email",
            })
            
            return &LoanApplicationOutput{
                ApplicationID:  applicationID.String(),
                Status:         "rejected",
                DecisionReason: fraudResult.Reason,
                ApprovalSteps:  approvalSteps,
                ProcessingTime: ctx.Time().Sub(startTime),
            }, nil
        }
        approvalSteps = append(approvalSteps, "fraud-check-passed")
        
        // 1b. Credit Check
        approvalSteps = append(approvalSteps, "credit-check-initiated")
        creditResult, err := flows.ExecuteActivity(ctx, CreditCheckActivity, &CreditCheckInput{
            ApplicantID: input.ApplicantID,
            CreditScore: input.CreditScore,
        })
        if err != nil {
            return nil, fmt.Errorf("credit check failed: %w", err)
        }
        approvalSteps = append(approvalSteps, 
            fmt.Sprintf("credit-check-completed: risk=%s", creditResult.RiskLevel))
        
        // Step 2: Document Verification Loop
        // Simulate checking multiple documents
        documentIDs := []string{
            fmt.Sprintf("DOC-%s-1", input.ApplicantID),
            fmt.Sprintf("DOC-%s-2", input.ApplicantID),
            fmt.Sprintf("DOC-%s-3", input.ApplicantID),
        }
        
        // Loop through document batches (demonstrating loop in workflow)
        allDocsValid := true
        for i := 0; i < len(documentIDs); i += 2 {
            batchEnd := i + 2
            if batchEnd > len(documentIDs) {
                batchEnd = len(documentIDs)
            }
            batch := documentIDs[i:batchEnd]
            
            approvalSteps = append(approvalSteps, 
                fmt.Sprintf("verifying-documents-batch-%d", i/2+1))
            
            docResult, err := flows.ExecuteActivity(ctx, DocumentVerificationActivity, 
                &DocumentVerificationInput{
                    ApplicantID:  input.ApplicantID,
                    DocumentIDs:  batch,
                })
            if err != nil {
                return nil, fmt.Errorf("document verification failed: %w", err)
            }
            
            if !docResult.AllValid {
                allDocsValid = false
                approvalSteps = append(approvalSteps, "document-verification-failed")
                break
            }
            
            // Small delay between batches using deterministic Sleep
            if err := ctx.Sleep(500 * time.Millisecond); err != nil {
                return nil, err
            }
        }
        
        if !allDocsValid {
            _, _ = flows.ExecuteActivity(ctx, SendNotificationActivity, &NotificationInput{
                ApplicantID: input.ApplicantID,
                Message:     "Application rejected: Invalid documents",
                Channel:     "email",
            })
            
            return &LoanApplicationOutput{
                ApplicationID:  applicationID.String(),
                Status:         "rejected",
                DecisionReason: "Document verification failed",
                ApprovalSteps:  approvalSteps,
                ProcessingTime: ctx.Time().Sub(startTime),
            }, nil
        }
        approvalSteps = append(approvalSteps, "all-documents-verified")
        
        // Step 3: Risk Assessment
        approvalSteps = append(approvalSteps, "risk-assessment-initiated")
        riskResult, err := flows.ExecuteActivity(ctx, RiskAssessmentActivity, &RiskAssessmentInput{
            Amount:    input.Amount,
            RiskLevel: creditResult.RiskLevel,
            Purpose:   input.Purpose,
        })
        if err != nil {
            return nil, fmt.Errorf("risk assessment failed: %w", err)
        }
        
        // Conditional branching based on risk assessment
        if !riskResult.Approved {
            approvalSteps = append(approvalSteps, "auto-rejected-by-risk-assessment")
            
            _, _ = flows.ExecuteActivity(ctx, SendNotificationActivity, &NotificationInput{
                ApplicantID: input.ApplicantID,
                Message:     "Application rejected: Risk assessment failed",
                Channel:     "email",
            })
            
            return &LoanApplicationOutput{
                ApplicationID:  applicationID.String(),
                Status:         "rejected",
                DecisionReason: "High risk profile",
                ApprovalSteps:  approvalSteps,
                ProcessingTime: ctx.Time().Sub(startTime),
            }, nil
        }
        approvalSteps = append(approvalSteps, "risk-assessment-passed")
        
        // Step 4: Manager Approval (if required) - Uses Signal
        if riskResult.RequiresManager {
            approvalSteps = append(approvalSteps, "manager-approval-required")
            
            // Send notification to applicant
            _, _ = flows.ExecuteActivity(ctx, SendNotificationActivity, &NotificationInput{
                ApplicantID: input.ApplicantID,
                Message:     "Your application requires manager approval. Please wait.",
                Channel:     "email",
            })
            
            // Generate approval request ID
            approvalRequestID := ctx.UUIDv7()
            fmt.Printf("[Workflow] Waiting for manager approval: %s\n", approvalRequestID)
            
            // Wait for manager signal (this pauses the workflow)
            approvalSteps = append(approvalSteps, "waiting-for-manager-signal")
            managerDecision, err := flows.WaitForSignal[LoanApplicationInput, ManagerApprovalSignal](
                ctx, "manager-approval")
            if err != nil {
                return nil, fmt.Errorf("manager approval signal failed: %w", err)
            }
            
            if !managerDecision.Approved {
                approvalSteps = append(approvalSteps, 
                    fmt.Sprintf("manager-rejected-by-%s", managerDecision.ManagerID))
                
                _, _ = flows.ExecuteActivity(ctx, SendNotificationActivity, &NotificationInput{
                    ApplicantID: input.ApplicantID,
                    Message:     fmt.Sprintf("Application rejected by manager: %s", 
                        managerDecision.Comments),
                    Channel:     "email",
                })
                
                return &LoanApplicationOutput{
                    ApplicationID:  applicationID.String(),
                    Status:         "rejected",
                    DecisionReason: fmt.Sprintf("Manager rejection: %s", managerDecision.Comments),
                    ApprovalSteps:  approvalSteps,
                    ProcessingTime: ctx.Time().Sub(startTime),
                }, nil
            }
            approvalSteps = append(approvalSteps, 
                fmt.Sprintf("manager-approved-by-%s", managerDecision.ManagerID))
        }
        
        // Step 5: Final Processing with Random Assignment
        // Use deterministic random to assign loan officer
        loanOfficerID := ctx.Random().Intn(5) + 1
        approvalSteps = append(approvalSteps, 
            fmt.Sprintf("assigned-to-officer-%d", loanOfficerID))
        
        // Simulate final processing delay
        fmt.Println("[Workflow] Finalizing loan approval...")
        if err := ctx.Sleep(2 * time.Second); err != nil {
            return nil, err
        }
        
        // Step 6: Send approval notification
        approvalSteps = append(approvalSteps, "sending-approval-notification")
        _, err = flows.ExecuteActivity(ctx, SendNotificationActivity, &NotificationInput{
            ApplicantID: input.ApplicantID,
            Message: fmt.Sprintf(
                "Congratulations! Your loan of $%.2f has been approved at %.2f%% interest rate.",
                riskResult.MaxAmount, riskResult.InterestRate),
            Channel: "email",
        })
        if err != nil {
            // Notification failure shouldn't fail the workflow
            fmt.Printf("[Warning] Failed to send notification: %v\n", err)
        }
        
        // Record completion time
        completionTime := ctx.Time()
        approvalSteps = append(approvalSteps, "loan-approved")
        
        fmt.Printf("=== Loan Application Completed: %s ===\n\n", applicationID)
        
        return &LoanApplicationOutput{
            ApplicationID:  applicationID.String(),
            Status:         "approved",
            ApprovedAmount: riskResult.MaxAmount,
            InterestRate:   riskResult.InterestRate,
            ApprovalSteps:  approvalSteps,
            ProcessingTime: completionTime.Sub(startTime),
            DecisionReason: fmt.Sprintf("Approved by system with %s risk level", 
                creditResult.RiskLevel),
        }, nil
    },
)
```

### Step 4: HTTP REST API

Now let's create a REST API to interact with the workflow:

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "time"
    
    "github.com/google/uuid"
    "github.com/gorilla/mux"
    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/nvcnvn/flows"
)

type Server struct {
    pool     *pgxpool.Pool
    tenantID uuid.UUID
}

// POST /api/loans/applications
func (s *Server) StartLoanApplication(w http.ResponseWriter, r *http.Request) {
    var input LoanApplicationInput
    if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }
    
    // Set tenant context
    ctx := flows.WithTenantID(context.Background(), s.tenantID)
    
    // Start workflow
    exec, err := flows.Start(ctx, LoanApplicationWorkflow, &input)
    if err != nil {
        http.Error(w, fmt.Sprintf("Failed to start workflow: %v", err), 
            http.StatusInternalServerError)
        return
    }
    
    // Return workflow ID immediately (async processing)
    response := map[string]string{
        "workflow_id": exec.WorkflowID().String(),
        "status":      "processing",
        "message":     "Loan application submitted successfully",
    }
    
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(response)
    
    fmt.Printf("[API] Started loan application workflow: %s\n", exec.WorkflowID())
}

// GET /api/loans/applications/:workflow_id
func (s *Server) GetLoanApplicationStatus(w http.ResponseWriter, r *http.Request) {
    vars := mux.Vars(r)
    workflowIDStr := vars["workflow_id"]
    
    workflowID, err := uuid.Parse(workflowIDStr)
    if err != nil {
        http.Error(w, "Invalid workflow ID", http.StatusBadRequest)
        return
    }
    
    // Query workflow status
    ctx := flows.WithTenantID(context.Background(), s.tenantID)
    status, err := flows.Query(ctx, workflowID)
    if err != nil {
        http.Error(w, fmt.Sprintf("Failed to query workflow: %v", err), 
            http.StatusInternalServerError)
        return
    }
    
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(status)
    
    fmt.Printf("[API] Queried workflow status: %s - %s\n", workflowID, status.Status)
}

// POST /api/loans/applications/:workflow_id/manager-approval
func (s *Server) SubmitManagerApproval(w http.ResponseWriter, r *http.Request) {
    vars := mux.Vars(r)
    workflowIDStr := vars["workflow_id"]
    
    workflowID, err := uuid.Parse(workflowIDStr)
    if err != nil {
        http.Error(w, "Invalid workflow ID", http.StatusBadRequest)
        return
    }
    
    var signal ManagerApprovalSignal
    if err := json.NewDecoder(r.Body).Decode(&signal); err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }
    
    // Send signal to workflow
    ctx := flows.WithTenantID(context.Background(), s.tenantID)
    err = flows.SendSignal(ctx, workflowID, "manager-approval", &signal)
    if err != nil {
        http.Error(w, fmt.Sprintf("Failed to send signal: %v", err), 
            http.StatusInternalServerError)
        return
    }
    
    response := map[string]string{
        "workflow_id": workflowID.String(),
        "status":      "signal_sent",
        "message":     "Manager decision recorded successfully",
    }
    
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(response)
    
    fmt.Printf("[API] Manager approval signal sent: %s - approved=%v\n", 
        workflowID, signal.Approved)
}

// GET /api/loans/applications/:workflow_id/result
func (s *Server) GetLoanApplicationResult(w http.ResponseWriter, r *http.Request) {
    vars := mux.Vars(r)
    workflowIDStr := vars["workflow_id"]
    
    workflowID, err := uuid.Parse(workflowIDStr)
    if err != nil {
        http.Error(w, "Invalid workflow ID", http.StatusBadRequest)
        return
    }
    
    // Create execution handle
    ctx := flows.WithTenantID(context.Background(), s.tenantID)
    
    // This is a trick: we need to recreate the execution handle
    // In a real app, you'd store this or query differently
    store := flows.NewEngine(s.pool).Store()
    exec := &flows.Execution[LoanApplicationOutput]{
        // Note: This is simplified - in production you'd need proper exec handle management
    }
    
    // Get result (waits if not complete)
    timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
    defer cancel()
    
    result, err := exec.Get(timeoutCtx)
    if err != nil {
        http.Error(w, fmt.Sprintf("Failed to get result: %v", err), 
            http.StatusInternalServerError)
        return
    }
    
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(result)
}

func main() {
    // Database connection
    pool, err := pgxpool.New(context.Background(),
        "postgres://postgres:postgres@localhost:5432/loans?sslmode=disable")
    if err != nil {
        panic(err)
    }
    defer pool.Close()
    
    // Initialize engine
    engine := flows.NewEngine(pool)
    flows.SetEngine(engine)
    
    // Create tenant (in production, this comes from auth)
    tenantID := uuid.New()
    
    // Start worker
    worker := flows.NewWorker(pool, flows.WorkerConfig{
        Concurrency:   5,
        WorkflowNames: []string{"loan-application-workflow"},
        PollInterval:  500 * time.Millisecond,
        TenantID:      tenantID,
    })
    defer worker.Stop()
    
    go func() {
        if err := worker.Run(context.Background()); err != nil {
            fmt.Printf("Worker error: %v\n", err)
        }
    }()
    
    // Setup HTTP server
    server := &Server{
        pool:     pool,
        tenantID: tenantID,
    }
    
    router := mux.NewRouter()
    router.HandleFunc("/api/loans/applications", server.StartLoanApplication).Methods("POST")
    router.HandleFunc("/api/loans/applications/{workflow_id}", 
        server.GetLoanApplicationStatus).Methods("GET")
    router.HandleFunc("/api/loans/applications/{workflow_id}/manager-approval", 
        server.SubmitManagerApproval).Methods("POST")
    router.HandleFunc("/api/loans/applications/{workflow_id}/result", 
        server.GetLoanApplicationResult).Methods("GET")
    
    fmt.Println("🚀 Loan Application Service started on :8080")
    fmt.Printf("   Tenant ID: %s\n", tenantID)
    fmt.Println("\n📝 Example API calls:")
    fmt.Println("   curl -X POST http://localhost:8080/api/loans/applications \\")
    fmt.Println("     -H 'Content-Type: application/json' \\")
    fmt.Println("     -d '{\"applicant_id\":\"APP123\",\"amount\":250000,\"purpose\":\"home\",\"credit_score\":720}'")
    fmt.Println()
    
    http.ListenAndServe(":8080", router)
}
```

### Step 5: API Usage Examples

```bash
# 1. Submit a loan application
curl -X POST http://localhost:8080/api/loans/applications \
  -H 'Content-Type: application/json' \
  -d '{
    "applicant_id": "APP-12345",
    "amount": 250000,
    "purpose": "home_purchase",
    "credit_score": 720
  }'

# Response:
# {
#   "workflow_id": "018c7e9a-5b2f-7890-abcd-123456789abc",
#   "status": "processing",
#   "message": "Loan application submitted successfully"
# }

# 2. Check application status
curl http://localhost:8080/api/loans/applications/018c7e9a-5b2f-7890-abcd-123456789abc

# Response:
# {
#   "workflow_id": "018c7e9a-5b2f-7890-abcd-123456789abc",
#   "tenant_id": "...",
#   "name": "loan-application-workflow",
#   "version": 1,
#   "status": "running"
# }

# 3. Submit manager approval (if required)
curl -X POST http://localhost:8080/api/loans/applications/018c7e9a-5b2f-7890-abcd-123456789abc/manager-approval \
  -H 'Content-Type: application/json' \
  -d '{
    "approved": true,
    "manager_id": "MGR-789",
    "comments": "Applicant has strong financial history"
  }'

# Response:
# {
#   "workflow_id": "018c7e9a-5b2f-7890-abcd-123456789abc",
#   "status": "signal_sent",
#   "message": "Manager decision recorded successfully"
# }

# 4. Get final result
curl http://localhost:8080/api/loans/applications/018c7e9a-5b2f-7890-abcd-123456789abc/result

# Response:
# {
#   "application_id": "018c7e9a-...",
#   "status": "approved",
#   "approved_amount": 250000,
#   "interest_rate": 4.5,
#   "approval_steps": [
#     "fraud-check-initiated",
#     "fraud-check-passed",
#     "credit-check-initiated",
#     "credit-check-completed: risk=low",
#     "verifying-documents-batch-1",
#     "verifying-documents-batch-2",
#     "all-documents-verified",
#     "risk-assessment-initiated",
#     "risk-assessment-passed",
#     "manager-approval-required",
#     "waiting-for-manager-signal",
#     "manager-approved-by-MGR-789",
#     "assigned-to-officer-3",
#     "sending-approval-notification",
#     "loan-approved"
#   ],
#   "processing_time": "15.234s",
#   "decision_reason": "Approved by system with low risk level"
# }
```

## 🔍 Understanding Database State

Let's walk through what happens in the database at each step. This helps you understand the internal design and troubleshoot issues.

### Initial State: Application Submission

When you call `flows.Start()`, here's what gets created:

#### 1. `workflows` Table

```sql
SELECT id, tenant_id, name, status, sequence_num, input, activity_results 
FROM workflows 
WHERE id = '018c7e9a-5b2f-7890-abcd-123456789abc';
```

```
id                                   | 018c7e9a-5b2f-7890-abcd-123456789abc
tenant_id                            | 5f9a8b7c-...
name                                 | loan-application-workflow
version                              | 1
status                               | pending
input                                | {"applicant_id":"APP-12345","amount":250000,...}
output                               | null
error                                | null
sequence_num                         | 0
activity_results                     | {}
updated_at                           | 2024-01-15 10:30:00
```

**What this means:**
- Workflow is registered but not yet running
- `sequence_num` is 0 (no activities executed yet)
- `activity_results` is empty (no cached results for replay)

#### 2. `task_queue` Table

```sql
SELECT id, workflow_id, workflow_name, task_type, visibility_timeout
FROM task_queue 
WHERE workflow_id = '018c7e9a-5b2f-7890-abcd-123456789abc'
ORDER BY id DESC LIMIT 5;
```

```
id                                   | 018c7e9b-1a2b-...
tenant_id                            | 5f9a8b7c-...
workflow_id                          | 018c7e9a-5b2f-...
workflow_name                        | loan-application-workflow
task_type                            | workflow
task_data                            | {}
visibility_timeout                   | 2024-01-15 10:30:00
```

**What this means:**
- A workflow task is queued for worker to pick up
- `visibility_timeout` in the past = ready to process
- Worker will poll this table and claim the task

### During Execution: First Activity

When the worker starts processing, it picks up the task and executes the workflow code.

#### 1. Workflow Status Changes

```sql
-- Status updated to 'running'
SELECT status, sequence_num FROM workflows 
WHERE id = '018c7e9a-5b2f-7890-abcd-123456789abc';
```

```
status      | running
sequence_num| 0
```

#### 2. First Activity Created (Fraud Check)

```sql
SELECT id, name, sequence_num, status, input, attempt
FROM activities
WHERE workflow_id = '018c7e9a-5b2f-7890-abcd-123456789abc'
ORDER BY sequence_num;
```

```
id                                   | 018c7e9b-3c4d-...
tenant_id                            | 5f9a8b7c-...
workflow_id                          | 018c7e9a-5b2f-...
name                                 | fraud-check
sequence_num                         | 1
status                               | scheduled
input                                | {"applicant_id":"APP-12345","amount":250000}
output                               | null
error                                | null
attempt                              | 0
next_retry_at                        | null
backoff_ms                           | null
updated_at                           | 2024-01-15 10:30:01
```

**What this means:**
- Activity created with `sequence_num = 1` (first activity)
- Status is `scheduled` (waiting to be executed)
- Workflow is PAUSED at this point (waiting for activity to complete)

#### 3. Activity Task Queued

```sql
SELECT task_type, task_data FROM task_queue
WHERE workflow_id = '018c7e9a-5b2f-7890-abcd-123456789abc'
ORDER BY id DESC LIMIT 1;
```

```
task_type   | activity
task_data   | {"activity_id":"018c7e9b-3c4d-..."}
```

#### 4. History Event Recorded

```sql
SELECT sequence_num, event_type, event_data 
FROM history_events
WHERE workflow_id = '018c7e9a-5b2f-7890-abcd-123456789abc'
ORDER BY sequence_num;
```

```
sequence_num | 1
event_type   | ACTIVITY_SCHEDULED
event_data   | {"activity_name":"fraud-check","activity_id":"018c7e9b-3c4d-..."}
```

**What this means:**
- Every workflow action is logged in history
- This enables deterministic replay
- On replay, workflow will see "activity already scheduled" and skip re-creating it

### Activity Execution

When the worker processes the activity task:

#### 1. Activity Status Updates

```sql
-- First: status changes to 'running'
-- Then: status changes to 'completed' with output
SELECT status, output, updated_at FROM activities
WHERE id = '018c7e9b-3c4d-...';
```

```
status      | completed
output      | {"suspicious":false,"score":100,"reason":"Clean record"}
updated_at  | 2024-01-15 10:30:02
```

#### 2. Workflow Resumes

```sql
-- New workflow task created to resume execution
SELECT task_type, visibility_timeout FROM task_queue
WHERE workflow_id = '018c7e9a-5b2f-7890-abcd-123456789abc'
AND task_type = 'workflow'
ORDER BY id DESC LIMIT 1;
```

```
task_type          | workflow
visibility_timeout | 2024-01-15 10:30:02
```

#### 3. Activity Results Cached

```sql
SELECT sequence_num, activity_results FROM workflows
WHERE id = '018c7e9a-5b2f-7890-abcd-123456789abc';
```

```
sequence_num     | 1
activity_results | {"1":{"suspicious":false,"score":100,"reason":"Clean record"}}
```

**What this means:**
- `activity_results` is a JSON map: `{sequence_num: result}`
- On replay, workflow will use cached result instead of re-executing
- This is the KEY to deterministic replay

### Activity Retry Scenario

If an activity fails (e.g., credit check API timeout):

#### 1. Activity Marked for Retry

```sql
SELECT status, attempt, next_retry_at, backoff_ms, error
FROM activities WHERE id = '018c7e9b-5e6f-...';
```

```
status        | scheduled
attempt       | 1
next_retry_at | 2024-01-15 10:30:03  -- 1 second later
backoff_ms    | 1000
error         | "connection timeout to credit bureau API"
```

**What this means:**
- Activity status back to `scheduled` for retry
- `attempt` incremented (tracks retry count)
- `next_retry_at` set based on retry policy (exponential backoff)
- Worker will pick it up again after this time

#### 2. Worker Polls for Retry

```sql
-- Worker query to find activities ready for retry
SELECT id, workflow_id, name FROM activities
WHERE tenant_id = '5f9a8b7c-...'
  AND status = 'scheduled'
  AND (next_retry_at IS NULL OR next_retry_at <= NOW())
ORDER BY next_retry_at NULLS FIRST
LIMIT 10;
```

### Signal Handling: Manager Approval

When the workflow reaches `WaitForSignal`, it pauses. Let's see the database state:

#### 1. Workflow Sequence Updated

```sql
SELECT sequence_num, activity_results FROM workflows
WHERE id = '018c7e9a-5b2f-7890-abcd-123456789abc';
```

```
sequence_num     | 5  -- After fraud, credit, docs, risk assessment
activity_results | {"1":{...},"2":{...},"3":{...},"4":{...}}
```

**What this means:**
- Workflow has completed 4 activities (sequence 1-4)
- Now waiting at sequence 5 for signal
- Worker sees `ErrWorkflowPaused` and saves state

#### 2. No Signal Yet

```sql
SELECT signal_name, consumed FROM signals
WHERE workflow_id = '018c7e9a-5b2f-7890-abcd-123456789abc'
  AND signal_name = 'manager-approval';
```

```
(empty result - signal not yet received)
```

#### 3. Sending the Signal

When you POST to `/manager-approval` endpoint:

```sql
-- Signal inserted
INSERT INTO signals (id, tenant_id, workflow_id, signal_name, payload, consumed)
VALUES (...);

-- Check the signal
SELECT id, signal_name, payload, consumed FROM signals
WHERE workflow_id = '018c7e9a-5b2f-7890-abcd-123456789abc';
```

```
id           | 018c7e9d-7f8e-...
signal_name  | manager-approval
payload      | {"approved":true,"manager_id":"MGR-789","comments":"..."}
consumed     | false
```

#### 4. Workflow Task Enqueued

```sql
-- Signal sending also creates a workflow task to resume
SELECT task_type, visibility_timeout FROM task_queue
WHERE workflow_id = '018c7e9a-5b2f-7890-abcd-123456789abc'
ORDER BY id DESC LIMIT 1;
```

```
task_type          | workflow
visibility_timeout | 2024-01-15 10:35:00  -- immediately available
```

#### 5. Signal Consumed

After worker picks up and processes the signal:

```sql
SELECT consumed FROM signals
WHERE id = '018c7e9d-7f8e-...';
```

```
consumed | true
```

**What this means:**
- Signal marked as consumed so it won't be processed again
- Workflow continues past `WaitForSignal()`
- Signal payload returned to workflow code

### Sleep/Timer State

When workflow calls `ctx.Sleep(2 * time.Second)`:

#### 1. Timer Created

```sql
SELECT id, sequence_num, fire_at, fired FROM timers
WHERE workflow_id = '018c7e9a-5b2f-7890-abcd-123456789abc'
ORDER BY sequence_num;
```

```
id           | 018c7e9e-1a2b-...
sequence_num | 6
fire_at      | 2024-01-15 10:35:02  -- 2 seconds in the future
fired        | false
```

#### 2. Timer Task Scheduled

```sql
SELECT task_type, task_data FROM task_queue
WHERE workflow_id = '018c7e9a-5b2f-7890-abcd-123456789abc'
AND task_type = 'timer';
```

```
task_type | timer
task_data | {"timer_id":"018c7e9e-1a2b-..."}
```

**What this means:**
- Workflow is paused until timer fires
- Worker has a separate poll for timers:

```sql
-- Worker timer poll query
SELECT t.id, t.workflow_id, t.sequence_num
FROM timers t
WHERE t.tenant_id = '5f9a8b7c-...'
  AND t.fire_at <= NOW()
  AND NOT t.fired
LIMIT 10;
```

#### 3. Timer Fires

When time is reached:

```sql
-- Timer marked as fired
UPDATE timers SET fired = true WHERE id = '018c7e9e-1a2b-...';

-- Workflow task created to resume
INSERT INTO task_queue (workflow_id, task_type, ...) VALUES (...);
```

### Deterministic Replay

This is the magic of Flows. Let's say the worker crashes after completing 3 activities. When it restarts:

#### 1. Worker Picks Up Workflow Task

```sql
SELECT id, status, sequence_num, activity_results FROM workflows
WHERE id = '018c7e9a-5b2f-7890-abcd-123456789abc';
```

```
status           | running
sequence_num     | 3
activity_results | {"1":{...},"2":{...},"3":{...}}
```

#### 2. Workflow Code Executes Again

```go
// Workflow code runs from the beginning
fraudResult, err := flows.ExecuteActivity(ctx, FraudCheckActivity, ...)
```

But this time:
- Workflow sees `sequence_num = 3` in context
- Activity 1 result is in `activity_results` cache
- **Activity is NOT re-executed**
- Cached result is returned immediately

#### 3. History Shows Replay

```sql
SELECT sequence_num, event_type FROM history_events
WHERE workflow_id = '018c7e9a-5b2f-7890-abcd-123456789abc'
ORDER BY sequence_num;
```

```
sequence_num | event_type
-------------|--------------------------
1            | ACTIVITY_SCHEDULED        (fraud check)
2            | ACTIVITY_COMPLETED
3            | ACTIVITY_SCHEDULED        (credit check)
4            | ACTIVITY_COMPLETED
5            | ACTIVITY_SCHEDULED        (doc verification)
6            | ACTIVITY_COMPLETED
7            | ACTIVITY_SCHEDULED        (doc verification batch 2) ← resume here
```

**What this means:**
- Workflow deterministically replays up to sequence 3
- Uses cached results for activities 1-3
- Continues execution from sequence 4
- Time() calls also return cached values from history
- UUIDv7() generates same UUIDs (deterministic random + cached time)

### Final State: Completion

#### 1. Workflow Completed

```sql
SELECT status, output, sequence_num FROM workflows
WHERE id = '018c7e9a-5b2f-7890-abcd-123456789abc';
```

```
status       | completed
output       | {"application_id":"018c7e9a-...","status":"approved",...}
sequence_num | 7
```

#### 2. All Activities Completed

```sql
SELECT sequence_num, name, status FROM activities
WHERE workflow_id = '018c7e9a-5b2f-7890-abcd-123456789abc'
ORDER BY sequence_num;
```

```
sequence_num | name                      | status
-------------|---------------------------|----------
1            | fraud-check               | completed
2            | credit-check              | completed
3            | document-verification     | completed
4            | document-verification     | completed
5            | risk-assessment           | completed
6            | send-notification         | completed
```

#### 3. Complete History

```sql
SELECT COUNT(*) as total_events FROM history_events
WHERE workflow_id = '018c7e9a-5b2f-7890-abcd-123456789abc';
```

```
total_events | 18  -- All workflow decisions recorded
```

## 🐛 Troubleshooting Guide

### Problem: Workflow Stuck in "running" Status

```sql
-- Check workflow state
SELECT status, sequence_num, updated_at FROM workflows
WHERE id = '<workflow_id>';

-- Check for pending tasks
SELECT task_type, visibility_timeout FROM task_queue
WHERE workflow_id = '<workflow_id>';

-- Check last activity
SELECT name, status, attempt, error FROM activities
WHERE workflow_id = '<workflow_id>'
ORDER BY sequence_num DESC LIMIT 1;
```

**Possible causes:**
1. **No worker running**: Check worker logs
2. **Activity failing with max retries**: Check `error` column in activities
3. **Waiting for signal**: Check if workflow is at `WaitForSignal()`
4. **Timer not fired yet**: Check `timers` table

### Problem: Activity Keeps Retrying

```sql
SELECT name, attempt, next_retry_at, error, backoff_ms
FROM activities
WHERE workflow_id = '<workflow_id>'
  AND status = 'scheduled';
```

**Solutions:**
1. Check the error message
2. Verify external service availability
3. Check retry policy (maybe max attempts too low)
4. Consider making error terminal: `flows.NewTerminalError()`

### Problem: Signal Not Received

```sql
-- Check if signal exists
SELECT signal_name, payload, consumed FROM signals
WHERE workflow_id = '<workflow_id>';

-- Check workflow is waiting
SELECT sequence_num FROM workflows WHERE id = '<workflow_id>';
```

**Common issues:**
1. Wrong signal name
2. Signal sent before workflow reached `WaitForSignal()`
3. Tenant ID mismatch
4. Signal already consumed (check `consumed` column)

### Problem: Duplicate Activity Executions

This shouldn't happen with proper replay, but if you see it:

```sql
-- Check for duplicate activities at same sequence
SELECT sequence_num, COUNT(*) FROM activities
WHERE workflow_id = '<workflow_id>'
GROUP BY sequence_num
HAVING COUNT(*) > 1;
```

**Causes:**
1. Bug in workflow code (not using context properly)
2. Database constraint violation
3. Manual database modification

### Monitoring Queries

```sql
-- Active workflows by status
SELECT status, COUNT(*) FROM workflows
WHERE tenant_id = '<tenant_id>'
GROUP BY status;

-- Activities requiring retry
SELECT name, COUNT(*) FROM activities
WHERE tenant_id = '<tenant_id>'
  AND status = 'scheduled'
  AND next_retry_at <= NOW()
GROUP BY name;

-- Workflows waiting for signals
SELECT w.id, w.name, s.signal_name
FROM workflows w
LEFT JOIN signals s ON s.workflow_id = w.id AND NOT s.consumed
WHERE w.tenant_id = '<tenant_id>'
  AND w.status = 'running';

-- Oldest pending workflows
SELECT id, name, updated_at
FROM workflows
WHERE tenant_id = '<tenant_id>'
  AND status = 'running'
ORDER BY updated_at ASC
LIMIT 10;
```

## 📊 Production Best Practices

### 1. Connection Pooling

```go
config, _ := pgxpool.ParseConfig(databaseURL)
config.MaxConns = 20
config.MinConns = 5
config.MaxConnLifetime = time.Hour
config.MaxConnIdleTime = 30 * time.Minute

pool, _ := pgxpool.NewWithConfig(context.Background(), config)
```

### 2. Worker Scaling

```go
// Run multiple workers for high throughput
for i := 0; i < 3; i++ {
    worker := flows.NewWorker(pool, flows.WorkerConfig{
        Concurrency:   10,
        WorkflowNames: []string{"loan-application-workflow"},
        PollInterval:  200 * time.Millisecond,
        TenantID:      tenantID,
    })
    go worker.Run(ctx)
}
```

### 3. Activity Idempotency

Always design activities to be idempotent:

```go
var ChargePaymentActivity = flows.NewActivity(
    "charge-payment",
    func(ctx context.Context, input *ChargePaymentInput) (*ChargePaymentOutput, error) {
        // Check if payment already processed (idempotency)
        activityCtx := ctx.Value("activity_context").(*flows.ActivityContext)
        idempotencyKey := fmt.Sprintf("payment-%s-%s", 
            input.ApplicantID, activityCtx.ActivityID())
        
        if existing := checkExistingPayment(idempotencyKey); existing != nil {
            return existing, nil // Return cached result
        }
        
        // Process payment
        result := processPayment(input)
        savePayment(idempotencyKey, result)
        return result, nil
    },
    flows.RetryPolicy{...},
)
```

### 4. Monitoring & Alerts

```go
// Periodically check for stuck workflows
go func() {
    ticker := time.NewTicker(5 * time.Minute)
    for range ticker.C {
        rows, _ := pool.Query(context.Background(), `
            SELECT id, name, updated_at 
            FROM workflows 
            WHERE status = 'running' 
              AND updated_at < NOW() - INTERVAL '30 minutes'
        `)
        // Alert on stuck workflows
    }
}()
```
