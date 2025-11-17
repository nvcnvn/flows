package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/nvcnvn/flows"
)

// API Request/Response types

type CreateLoanRequest struct {
	ApplicantName string  `json:"applicant_name"`
	Amount        float64 `json:"amount"`
	Purpose       string  `json:"purpose"`
}

type CreateLoanResponse struct {
	WorkflowID   string `json:"workflow_id"`
	WorkflowName string `json:"workflow_name"`
	Message      string `json:"message"`
}

type SubmitDocumentRequest struct {
	DocumentType string `json:"document_type"` // identity, income, address
	DocumentID   string `json:"document_id"`
}

type SubmitApprovalRequest struct {
	ApproverRole string `json:"approver_role"` // manager, underwriter
	Approved     bool   `json:"approved"`
	Comments     string `json:"comments"`
}

// HTTP Handlers

// createLoanHandler starts a new loan application workflow
// POST /api/loans
// Example:
//
//	curl -X POST http://localhost:8181/api/loans \
//	  -H "Content-Type: application/json" \
//	  -d '{"applicant_name":"John Doe","amount":75000,"purpose":"home renovation"}'
func (s *Server) createLoanHandler(w http.ResponseWriter, r *http.Request) {
	var req CreateLoanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Validate input
	if req.ApplicantName == "" {
		writeError(w, http.StatusBadRequest, "applicant_name is required")
		return
	}
	if req.Amount <= 0 {
		writeError(w, http.StatusBadRequest, "amount must be positive")
		return
	}

	ctx := context.Background()

	// Set tenant ID (in multi-tenant systems, this would come from auth)
	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	// Start workflow using explicit engine instance
	// No need to create a new engine or set global state
	input := &LoanApplicationInput{
		ApplicantName: req.ApplicantName,
		Amount:        req.Amount,
		Purpose:       req.Purpose,
	}

	exec, err := flows.StartWith(s.engine, ctx, LoanApplicationWorkflow, input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to start workflow: %v", err))
		return
	}

	writeJSON(w, http.StatusCreated, CreateLoanResponse{
		WorkflowID:   exec.WorkflowID().String(),
		WorkflowName: exec.WorkflowName(), // Returns base name (e.g., "loan-application")
		Message:      fmt.Sprintf("Loan application started successfully (shard: %s)", exec.WorkflowNameSharded()),
	})
}

// getLoanStatusHandler retrieves the status of a loan application
// GET /api/loans/{workflowName}/{id}
//
// Example:
//
//	curl "http://localhost:8181/api/loans/loan-application/123e4567-e89b-12d3-a456-426614174000"
func (s *Server) getLoanStatusHandler(w http.ResponseWriter, r *http.Request) {
	// Extract workflow name from path (base name, e.g., "loan-application")
	workflowName := r.PathValue("workflowName")
	if workflowName == "" {
		writeError(w, http.StatusBadRequest, "workflow_name path parameter is required")
		return
	}

	workflowIDStr := r.PathValue("id")
	workflowID, err := uuid.Parse(workflowIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid workflow ID")
		return
	}

	ctx := context.Background()

	// Set tenant ID (in production, extract from auth)
	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	// Query workflow status using explicit engine instance
	// Sharding is handled internally using the engine's hash ring
	status, err := flows.QueryWith(s.engine, ctx, workflowName, workflowID)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Sprintf("Workflow not found: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, status)
}

// submitDocumentHandler sends a document submission signal
// POST /api/loans/{workflowName}/{id}/documents
//
// Example:
//
//	curl -X POST "http://localhost:8181/api/loans/loan-application/123e4567-e89b-12d3-a456-426614174000/documents" \
//	  -H "Content-Type: application/json" \
//	  -d '{"document_type":"identity","document_id":"DL-123456789"}'
func (s *Server) submitDocumentHandler(w http.ResponseWriter, r *http.Request) {
	// Extract workflow name from path (base name)
	workflowName := r.PathValue("workflowName")
	if workflowName == "" {
		writeError(w, http.StatusBadRequest, "workflow_name path parameter is required")
		return
	}

	workflowIDStr := r.PathValue("id")
	workflowID, err := uuid.Parse(workflowIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid workflow ID")
		return
	}

	var req SubmitDocumentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.DocumentType == "" || req.DocumentID == "" {
		writeError(w, http.StatusBadRequest, "document_type and document_id are required")
		return
	}

	ctx := context.Background()

	// Set tenant ID (in production, extract from auth)
	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	// Send signal using explicit engine instance
	// Sharding is handled internally using the engine's hash ring
	signalName := fmt.Sprintf("document-%s", req.DocumentType)
	payload := &DocumentSubmission{
		DocumentType: req.DocumentType,
		DocumentID:   req.DocumentID,
		UploadedAt:   time.Now(),
	}

	err = flows.SendSignalWith(s.engine, ctx, workflowName, workflowID, signalName, payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to send signal: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"message": fmt.Sprintf("Document %s submitted successfully", req.DocumentType),
	})
}

// submitApprovalHandler sends a manager/underwriter approval signal
// POST /api/loans/{workflowName}/{id}/approve
//
// Example:
//
//	curl -X POST "http://localhost:8181/api/loans/loan-application/123e4567-e89b-12d3-a456-426614174000/approve" \
//	  -H "Content-Type: application/json" \
//	  -d '{"approver_role":"manager","approved":true,"comments":"Looks good"}'
func (s *Server) submitApprovalHandler(w http.ResponseWriter, r *http.Request) {
	// Extract workflow name from path (base name)
	workflowName := r.PathValue("workflowName")
	if workflowName == "" {
		writeError(w, http.StatusBadRequest, "workflow_name path parameter is required")
		return
	}

	workflowIDStr := r.PathValue("id")
	workflowID, err := uuid.Parse(workflowIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid workflow ID")
		return
	}

	var req SubmitApprovalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	ctx := context.Background()

	// Set tenant ID (in production, extract from auth)
	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	// Send signal using explicit engine instance
	// Sharding is handled internally using the engine's hash ring
	signalName := "manager-approval"
	payload := &ApprovalSignal{
		ApproverRole: req.ApproverRole,
		Approved:     req.Approved,
		Comments:     req.Comments,
		ApprovedAt:   time.Now(),
	}

	err = flows.SendSignalWith(s.engine, ctx, workflowName, workflowID, signalName, payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to send approval signal: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"message": "Approval signal sent successfully",
	})
}

// getLoanResultHandler waits for and returns the final result of a loan application
// GET /api/loans/{workflowName}/{id}/result
//
// Example:
//
//	curl "http://localhost:8181/api/loans/loan-application/123e4567-e89b-12d3-a456-426614174000/result"
func (s *Server) getLoanResultHandler(w http.ResponseWriter, r *http.Request) {
	// Extract workflow name from path (base name)
	workflowName := r.PathValue("workflowName")
	if workflowName == "" {
		writeError(w, http.StatusBadRequest, "workflow_name path parameter is required")
		return
	}

	workflowIDStr := r.PathValue("id")
	workflowID, err := uuid.Parse(workflowIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid workflow ID")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Set tenant ID (in production, extract from auth)
	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	// Get result using explicit engine instance
	// Sharding is handled internally using the engine's hash ring
	result, err := flows.GetResultWith[LoanApplicationOutput](s.engine, ctx, workflowName, workflowID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to get result: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, result)
}
