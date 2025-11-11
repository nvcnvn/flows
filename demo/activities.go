package main

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/google/uuid"
	"github.com/nvcnvn/flows"
)

// Activity Input/Output types

type CreditScoreInput struct {
	ApplicantName string `json:"applicant_name"`
}

type CreditScoreOutput struct {
	Score     int       `json:"score"`
	Provider  string    `json:"provider"`
	CheckedAt time.Time `json:"checked_at"`
}

type DocumentVerifyInput struct {
	DocumentType string `json:"document_type"`
	DocumentID   string `json:"document_id"`
}

type DocumentVerifyOutput struct {
	Valid  bool   `json:"valid"`
	Reason string `json:"reason,omitempty"`
}

type UnderwriterInput struct {
	ApplicationID string  `json:"application_id"`
	Amount        float64 `json:"amount"`
	CreditScore   int     `json:"credit_score"`
}

type UnderwriterOutput struct {
	Approved bool   `json:"approved"`
	Reason   string `json:"reason"`
}

type DisburseLoanInput struct {
	ApplicationID string  `json:"application_id"`
	ApplicantName string  `json:"applicant_name"`
	Amount        float64 `json:"amount"`
}

type DisburseLoanOutput struct {
	TransactionID string    `json:"transaction_id"`
	DisbursedAt   time.Time `json:"disbursed_at"`
	Amount        float64   `json:"amount"`
}

// Activities

// CheckCreditScoreActivity simulates calling an external credit bureau
// In a real system, this would make an API call to Experian, Equifax, etc.
var CheckCreditScoreActivity = flows.NewActivity(
	"check-credit-score",
	func(ctx context.Context, input *CreditScoreInput) (*CreditScoreOutput, error) {
		fmt.Printf("🏦 Checking credit score for: %s\n", input.ApplicantName)

		// Simulate external API call delay
		time.Sleep(500 * time.Millisecond)

		// Generate a deterministic score based on name (for demo purposes)
		// In production, this would be a real API call
		score := generateCreditScore(input.ApplicantName)

		fmt.Printf("📊 Credit score result: %d\n", score)

		return &CreditScoreOutput{
			Score:     score,
			Provider:  "Demo Credit Bureau",
			CheckedAt: time.Now(),
		}, nil
	},
	flows.RetryPolicy{
		InitialInterval: 1 * time.Second,
		MaxInterval:     30 * time.Second,
		BackoffFactor:   2.0,
		MaxAttempts:     3,
		Jitter:          0.1,
	},
)

// VerifyDocumentActivity simulates document verification
// In a real system, this would use OCR, identity verification services, etc.
var VerifyDocumentActivity = flows.NewActivity(
	"verify-document",
	func(ctx context.Context, input *DocumentVerifyInput) (*DocumentVerifyOutput, error) {
		fmt.Printf("📄 Verifying document: %s (ID: %s)\n", input.DocumentType, input.DocumentID)

		// Simulate verification process
		time.Sleep(300 * time.Millisecond)

		// Simple validation: document ID should not be empty and have reasonable format
		// In production, this would do real verification (OCR, database check, etc.)
		if input.DocumentID == "" {
			return &DocumentVerifyOutput{
				Valid:  false,
				Reason: "Document ID is empty",
			}, nil
		}

		if len(input.DocumentID) < 5 {
			return &DocumentVerifyOutput{
				Valid:  false,
				Reason: "Document ID format invalid",
			}, nil
		}

		// 95% success rate for demo purposes
		r := rand.New(rand.NewSource(time.Now().UnixNano()))
		if r.Intn(100) < 95 {
			fmt.Printf("✅ Document verification passed: %s\n", input.DocumentType)
			return &DocumentVerifyOutput{
				Valid: true,
			}, nil
		}

		return &DocumentVerifyOutput{
			Valid:  false,
			Reason: "Document appears to be tampered",
		}, nil
	},
	flows.RetryPolicy{
		InitialInterval: 500 * time.Millisecond,
		MaxInterval:     10 * time.Second,
		BackoffFactor:   2.0,
		MaxAttempts:     5,
		Jitter:          0.1,
	},
)

// GetUnderwriterApprovalActivity simulates underwriter review
// For large loans, a human underwriter or advanced system reviews the application
var GetUnderwriterApprovalActivity = flows.NewActivity(
	"get-underwriter-approval",
	func(ctx context.Context, input *UnderwriterInput) (*UnderwriterOutput, error) {
		fmt.Printf("🔍 Underwriter reviewing application %s (Amount: $%.2f, Score: %d)\n",
			input.ApplicationID, input.Amount, input.CreditScore)

		// Simulate underwriter review time
		time.Sleep(1 * time.Second)

		// Decision logic based on credit score and amount
		// For demo: approve if credit score >= 700 or amount < 150k with score >= 650
		if input.CreditScore >= 700 {
			fmt.Println("✅ Underwriter approved - Excellent credit score")
			return &UnderwriterOutput{
				Approved: true,
				Reason:   "Excellent credit history and acceptable debt-to-income ratio",
			}, nil
		}

		if input.Amount < 150000 && input.CreditScore >= 650 {
			fmt.Println("✅ Underwriter approved - Good credit for this amount")
			return &UnderwriterOutput{
				Approved: true,
				Reason:   "Good credit score for requested amount",
			}, nil
		}

		fmt.Println("❌ Underwriter declined")
		return &UnderwriterOutput{
			Approved: false,
			Reason:   fmt.Sprintf("Credit score %d is insufficient for amount $%.2f", input.CreditScore, input.Amount),
		}, nil
	},
	flows.RetryPolicy{
		InitialInterval: 2 * time.Second,
		MaxInterval:     1 * time.Minute,
		BackoffFactor:   2.0,
		MaxAttempts:     3,
		Jitter:          0.1,
	},
)

// DisburseLoansActivity simulates loan disbursement
// In a real system, this would integrate with payment systems, accounting, etc.
var DisburseLoansActivity = flows.NewActivity(
	"disburse-loan",
	func(ctx context.Context, input *DisburseLoanInput) (*DisburseLoanOutput, error) {
		fmt.Printf("💰 Disbursing loan: $%.2f to %s\n", input.Amount, input.ApplicantName)

		// Simulate disbursement process
		time.Sleep(800 * time.Millisecond)

		// Generate transaction ID
		transactionID := uuid.New().String()

		fmt.Printf("✅ Loan disbursed successfully. Transaction ID: %s\n", transactionID)

		return &DisburseLoanOutput{
			TransactionID: transactionID,
			DisbursedAt:   time.Now(),
			Amount:        input.Amount,
		}, nil
	},
	flows.RetryPolicy{
		InitialInterval: 1 * time.Second,
		MaxInterval:     30 * time.Second,
		BackoffFactor:   2.0,
		MaxAttempts:     5,
		Jitter:          0.1,
	},
)

// Helper functions

// generateCreditScore creates a deterministic credit score based on name
// This is for demo purposes only - real systems would call credit bureaus
func generateCreditScore(name string) int {
	// Use name as seed for deterministic scores
	seed := int64(0)
	for _, c := range name {
		seed += int64(c)
	}

	r := rand.New(rand.NewSource(seed))

	// Generate score between 550 and 850 (FICO range)
	baseScore := 550
	varianceScore := r.Intn(300)

	return baseScore + varianceScore
}
