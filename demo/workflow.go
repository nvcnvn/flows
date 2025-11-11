package main

import (
	"fmt"
	"time"

	"github.com/nvcnvn/flows"
)

// LoanApplicationInput represents the initial loan request
type LoanApplicationInput struct {
	ApplicantName     string  `json:"applicant_name"`
	Amount            float64 `json:"amount"`
	Purpose           string  `json:"purpose"`
	RequestedTenantID string  `json:"tenant_id,omitempty"` // For multi-tenant scenarios
}

// LoanApplicationOutput represents the final decision
type LoanApplicationOutput struct {
	ApplicationID  string    `json:"application_id"`
	Decision       string    `json:"decision"` // approved, rejected
	ApprovedAmount float64   `json:"approved_amount,omitempty"`
	Reason         string    `json:"reason,omitempty"`
	ProcessedAt    time.Time `json:"processed_at"`
}

// DocumentSubmission represents a document upload signal
type DocumentSubmission struct {
	DocumentType string    `json:"document_type"` // identity, income, address
	DocumentID   string    `json:"document_id"`
	UploadedAt   time.Time `json:"uploaded_at"`
}

// ApprovalSignal represents manager/underwriter approval
type ApprovalSignal struct {
	ApproverRole string    `json:"approver_role"` // manager, underwriter
	Approved     bool      `json:"approved"`
	Comments     string    `json:"comments"`
	ApprovedAt   time.Time `json:"approved_at"`
}

// LoanApplicationWorkflow demonstrates all flows features:
// - UUIDv7 generation (time-ordered UUIDs)
// - Activities (external service calls)
// - Signals (wait for external events)
// - Sleep (timers)
// - Random (deterministic randomness)
// - Branching (conditional logic)
// - Loops (document collection)
var LoanApplicationWorkflow = flows.New(
	"loan-application",
	1,
	func(ctx *flows.Context[LoanApplicationInput]) (*LoanApplicationOutput, error) {
		input := ctx.Input()

		// Generate unique application ID using UUIDv7 (time-ordered)
		// DB State: This creates a workflow record in `workflows` table
		// - name: "loan-application-shard-X" (based on hash)
		// - id: UUIDv7 (time-ordered UUID)
		// - tenant_id: from context
		// - status: "running"
		// - input: JSON of LoanApplicationInput
		applicationID, err := ctx.UUIDv7()
		if err != nil {
			return nil, fmt.Errorf("failed to generate UUID: %w", err)
		}

		fmt.Printf("📋 Loan Application Started: %s for %s ($%.2f)\n",
			applicationID.String(), input.ApplicantName, input.Amount,
		)

		// Step 1: Check Credit Score (async activity)
		// DB State: Creates record in `activities` table
		// - workflow_name: "loan-application-shard-X"
		// - workflow_id: current workflow UUID
		// - name: "check-credit-score"
		// - status: "scheduled"
		// - input: JSON with applicant name
		// Also creates task in `task_queue` table for worker to pick up
		fmt.Println("🔍 Checking credit score...")
		creditScore, err := flows.ExecuteActivity(
			ctx,
			CheckCreditScoreActivity,
			&CreditScoreInput{ApplicantName: input.ApplicantName},
		)
		if err != nil {
			return nil, fmt.Errorf("credit score check failed: %w", err)
		}
		// DB State After: `activities` record updated to status: "completed", output: JSON with score

		fmt.Printf("✅ Credit Score Retrieved: %d\n", creditScore.Score)

		// Branching: Reject if credit score too low
		if creditScore.Score < 600 {
			fmt.Printf("❌ Application Rejected - Low Credit Score: %d\n", creditScore.Score)
			return &LoanApplicationOutput{
				ApplicationID: applicationID.String(),
				Decision:      "rejected",
				Reason:        fmt.Sprintf("Credit score %d is below minimum requirement of 600", creditScore.Score),
				ProcessedAt:   time.Now(),
			}, nil
		}

		// Step 2: Request required documents (signals)
		// For high loan amounts, we need more documents
		requiredDocs := []string{"identity", "income"}
		if input.Amount > 50000 {
			requiredDocs = append(requiredDocs, "address")
			fmt.Println("📄 High amount loan - Additional documentation required")
		}

		fmt.Printf("📄 Waiting for documents: %v\n", requiredDocs)

		// Wait for each document (loop with signals)
		// DB State: Workflow pauses, status remains "running"
		// When signal received, creates record in `signals` table
		// - workflow_name: "loan-application-shard-X"
		// - workflow_id: current workflow UUID
		// - signal_name: "document-identity" (or income, address)
		// - payload: JSON of DocumentSubmission
		// - consumed: false (set to true after processing)
		receivedDocs := make(map[string]*DocumentSubmission)
		for _, docType := range requiredDocs {
			signalName := fmt.Sprintf("document-%s", docType)
			fmt.Printf("⏳ Waiting for signal: %s\n", signalName)

			// WaitForSignal will pause workflow until signal is received
			docSubmission, err := flows.WaitForSignal[LoanApplicationInput, DocumentSubmission](ctx, signalName)
			if err != nil {
				return nil, fmt.Errorf("failed to receive %s document: %w", docType, err)
			}

			receivedDocs[docType] = docSubmission
			fmt.Printf("✅ Document Received: %s (ID: %s)\n", docType, docSubmission.DocumentID)

			// Verify document asynchronously
			// DB State: Another `activities` record created for verification
			verifyResult, err := flows.ExecuteActivity(
				ctx,
				VerifyDocumentActivity,
				&DocumentVerifyInput{
					DocumentType: docType,
					DocumentID:   docSubmission.DocumentID,
				},
			)
			if err != nil {
				return nil, fmt.Errorf("document verification failed: %w", err)
			}

			if !verifyResult.Valid {
				fmt.Printf("❌ Application Rejected - Invalid Document: %s\n", docType)
				return &LoanApplicationOutput{
					ApplicationID: applicationID.String(),
					Decision:      "rejected",
					Reason:        fmt.Sprintf("Document %s is invalid: %s", docType, verifyResult.Reason),
					ProcessedAt:   time.Now(),
				}, nil
			}

			fmt.Printf("✅ Document Verified: %s\n", docType)
		}

		// Step 3: Sleep for compliance check (demonstrates timer)
		// DB State: Creates record in `timers` table
		// - workflow_name: "loan-application-shard-X"
		// - workflow_id: current workflow UUID
		// - fire_at: current time + 5 seconds
		// - fired: false
		// Worker monitors timers and resumes workflow when timer fires
		fmt.Println("⏰ Compliance check - waiting 5 seconds...")
		err = ctx.Sleep(5 * time.Second)
		if err != nil {
			return nil, err
		}
		fmt.Println("✅ Compliance check complete")

		// Step 4: Random decision for additional review
		// Use Random() for deterministic randomness (same result on replay)
		randomBytes, err := ctx.Random(8)
		if err != nil {
			return nil, fmt.Errorf("failed to generate random: %w", err)
		}
		randomValue := int(randomBytes[0])
		needsAdditionalReview := randomValue%100 < 30 // 30% chance

		if needsAdditionalReview {
			fmt.Printf("🎲 Random check triggered additional review (value: %d)\n", randomValue)

			// Wait for manager approval
			fmt.Println("⏳ Waiting for manager approval...")
			managerApproval, err := flows.WaitForSignal[LoanApplicationInput, ApprovalSignal](ctx, "manager-approval")
			if err != nil {
				return nil, fmt.Errorf("failed to receive manager approval: %w", err)
			}

			if !managerApproval.Approved {
				fmt.Println("❌ Application Rejected - Manager Declined")
				return &LoanApplicationOutput{
					ApplicationID: applicationID.String(),
					Decision:      "rejected",
					Reason:        fmt.Sprintf("Manager declined: %s", managerApproval.Comments),
					ProcessedAt:   time.Now(),
				}, nil
			}

			fmt.Printf("✅ Manager Approved: %s\n", managerApproval.Comments)
		} else {
			fmt.Printf("✅ No additional review required (random value: %d)\n", randomValue)
		}

		// Step 5: For large amounts, need underwriter approval
		if input.Amount > 100000 {
			fmt.Println("💰 Large amount - Underwriter approval required")

			// Call external underwriter system
			underwriterResult, err := flows.ExecuteActivity(
				ctx,
				GetUnderwriterApprovalActivity,
				&UnderwriterInput{
					ApplicationID: applicationID.String(),
					Amount:        input.Amount,
					CreditScore:   creditScore.Score,
				},
			)
			if err != nil {
				return nil, fmt.Errorf("underwriter check failed: %w", err)
			}

			if !underwriterResult.Approved {
				fmt.Println("❌ Application Rejected - Underwriter Declined")
				return &LoanApplicationOutput{
					ApplicationID: applicationID.String(),
					Decision:      "rejected",
					Reason:        underwriterResult.Reason,
					ProcessedAt:   time.Now(),
				}, nil
			}

			fmt.Println("✅ Underwriter Approved")
		}

		// Step 6: Final approval - disburse funds
		fmt.Println("💵 Disbursing loan...")
		disburseResult, err := flows.ExecuteActivity(
			ctx,
			DisburseLoansActivity,
			&DisburseLoanInput{
				ApplicationID: applicationID.String(),
				ApplicantName: input.ApplicantName,
				Amount:        input.Amount,
			},
		)
		if err != nil {
			return nil, fmt.Errorf("loan disbursement failed: %w", err)
		}

		fmt.Printf("🎉 Loan Application Approved! Transaction ID: %s\n", disburseResult.TransactionID)

		// DB State Final: `workflows` table updated
		// - status: "completed"
		// - output: JSON of LoanApplicationOutput
		// - activity_results: JSON map of all activity outputs
		return &LoanApplicationOutput{
			ApplicationID:  applicationID.String(),
			Decision:       "approved",
			ApprovedAmount: input.Amount,
			Reason:         "All checks passed successfully",
			ProcessedAt:    time.Now(),
		}, nil
	},
)
