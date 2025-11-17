package examples_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nvcnvn/flows"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Order domain types
type OrderInput struct {
	CustomerID string   `json:"customer_id"`
	Items      []string `json:"items"`
	TotalPrice float64  `json:"total_price"`
}

type OrderOutput struct {
	OrderID        string `json:"order_id"`
	TrackingNumber string `json:"tracking_number"`
	Status         string `json:"status"`
}

// Activity inputs/outputs
type ChargePaymentInput struct {
	CustomerID string  `json:"customer_id"`
	Amount     float64 `json:"amount"`
}

type ChargePaymentOutput struct {
	TransactionID string `json:"transaction_id"`
	Success       bool   `json:"success"`
}

type ShipOrderInput struct {
	OrderID string   `json:"order_id"`
	Items   []string `json:"items"`
}

type ShipOrderOutput struct {
	TrackingNumber string `json:"tracking_number"`
}

type WarehouseConfirmation struct {
	OrderID   string `json:"order_id"`
	Available bool   `json:"available"`
}

// Define activities
var ChargePaymentActivity = flows.NewActivity(
	"charge-payment",
	func(ctx context.Context, input *ChargePaymentInput) (*ChargePaymentOutput, error) {
		fmt.Printf("Charging payment: customer=%s, amount=%.2f\n", input.CustomerID, input.Amount)

		// Simulate payment processing
		time.Sleep(1 * time.Second)

		return &ChargePaymentOutput{
			TransactionID: uuid.New().String(),
			Success:       true,
		}, nil
	},
	flows.DefaultRetryPolicy,
)

var ShipOrderActivity = flows.NewActivity(
	"ship-order",
	func(ctx context.Context, input *ShipOrderInput) (*ShipOrderOutput, error) {
		fmt.Printf("Shipping order: order_id=%s, items=%v\n", input.OrderID, input.Items)

		// Simulate shipping
		time.Sleep(1 * time.Second)

		return &ShipOrderOutput{
			TrackingNumber: "TRACK-" + uuid.New().String()[:8],
		}, nil
	},
	flows.DefaultRetryPolicy,
)

// Define workflow
var OrderWorkflow = flows.New(
	"order-workflow",
	1,
	func(ctx *flows.Context[OrderInput]) (*OrderOutput, error) {
		input := ctx.Input()
		orderUUID, err := ctx.UUIDv7()
		if err != nil {
			return nil, err
		}
		orderID := orderUUID.String()

		fmt.Printf("Starting order workflow: order_id=%s, customer=%s\n", orderID, input.CustomerID)

		// Step 1: Charge payment
		payment, err := flows.ExecuteActivity(ctx, ChargePaymentActivity, &ChargePaymentInput{
			CustomerID: input.CustomerID,
			Amount:     input.TotalPrice,
		})
		if err != nil {
			return nil, fmt.Errorf("payment failed: %w", err)
		}

		if !payment.Success {
			return nil, fmt.Errorf("payment was declined")
		}

		fmt.Printf("Payment successful: transaction_id=%s\n", payment.TransactionID)

		// Step 2: Wait for warehouse confirmation
		fmt.Println("Waiting for warehouse confirmation...")
		confirmation, err := flows.WaitForSignal[OrderInput, WarehouseConfirmation](ctx, "warehouse-ready")
		if err != nil {
			return nil, fmt.Errorf("warehouse confirmation failed: %w", err)
		}

		if !confirmation.Available {
			return nil, fmt.Errorf("items not available in warehouse")
		}

		fmt.Println("Warehouse confirmed items available")

		// Step 3: Ship order
		shipment, err := flows.ExecuteActivity(ctx, ShipOrderActivity, &ShipOrderInput{
			OrderID: orderID,
			Items:   input.Items,
		})
		if err != nil {
			return nil, fmt.Errorf("shipping failed: %w", err)
		}

		fmt.Printf("Order shipped: tracking=%s\n", shipment.TrackingNumber)

		return &OrderOutput{
			OrderID:        orderID,
			TrackingNumber: shipment.TrackingNumber,
			Status:         "shipped",
		}, nil
	},
)

func TestOrderWorkflow_Complete(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Setup database connection
	pool := SetupTestDB(t)

	// Create engine
	engine := flows.NewEngine(pool)

	// Set tenant ID
	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	t.Logf("Using tenant ID: %s", tenantID)

	// Create unique workflow for this test to avoid worker interference
	testWorkflow := flows.New(
		"order-workflow-complete-test",
		1,
		func(ctx *flows.Context[OrderInput]) (*OrderOutput, error) {
			input := ctx.Input()
			orderUUID, err := ctx.UUIDv7()
			if err != nil {
				return nil, err
			}
			orderID := orderUUID.String()

			fmt.Printf("Starting order workflow: order_id=%s, customer=%s\n", orderID, input.CustomerID)

			payment, err := flows.ExecuteActivity(ctx, ChargePaymentActivity, &ChargePaymentInput{
				CustomerID: input.CustomerID,
				Amount:     input.TotalPrice,
			})
			if err != nil {
				return nil, fmt.Errorf("payment failed: %w", err)
			}

			if !payment.Success {
				return nil, fmt.Errorf("payment was declined")
			}

			fmt.Printf("Payment successful: transaction_id=%s\n", payment.TransactionID)

			fmt.Println("Waiting for warehouse confirmation...")
			confirmation, err := flows.WaitForSignal[OrderInput, WarehouseConfirmation](ctx, "warehouse-ready")
			if err != nil {
				return nil, fmt.Errorf("warehouse confirmation failed: %w", err)
			}

			if !confirmation.Available {
				return nil, fmt.Errorf("items not available in warehouse")
			}

			fmt.Println("Warehouse confirmed items available")

			shipment, err := flows.ExecuteActivity(ctx, ShipOrderActivity, &ShipOrderInput{
				OrderID: orderID,
				Items:   input.Items,
			})
			if err != nil {
				return nil, fmt.Errorf("shipping failed: %w", err)
			}

			fmt.Printf("Order shipped: tracking=%s\n", shipment.TrackingNumber)

			return &OrderOutput{
				OrderID:        orderID,
				TrackingNumber: shipment.TrackingNumber,
				Status:         "shipped",
			}, nil
		},
	)

	// Start workflow with explicit engine (avoid global state race)
	exec, err := flows.StartWith(engine, ctx, testWorkflow, &OrderInput{
		CustomerID: "cust-123",
		Items:      []string{"item-1", "item-2"},
		TotalPrice: 99.99,
	})
	require.NoError(t, err, "Failed to start workflow")
	assert.NotEmpty(t, exec.WorkflowID(), "Workflow ID should not be empty")

	t.Logf("Workflow started: id=%s", exec.WorkflowID())

	// Start worker in background
	worker := flows.NewWorker(pool, flows.WorkerConfig{
		Concurrency:   5,
		WorkflowNames: []string{"order-workflow-complete-test"},
		PollInterval:  500 * time.Millisecond,
	})

	workerCtx, cancel := context.WithCancel(ctx)

	// Ensure proper cleanup order: cancel context first, then stop worker
	defer func() {
		cancel()
		worker.Stop()
	}()

	go func() {
		if err := worker.Run(workerCtx); err != nil && err != context.Canceled {
			t.Logf("Worker error: %v", err)
		}
	}()

	// Wait a bit for workflow to start processing
	time.Sleep(2 * time.Second)

	// Send warehouse confirmation signal (use explicit engine)
	err = flows.SendSignalWith(engine, ctx, exec.WorkflowName(), exec.WorkflowID(), "warehouse-ready", &WarehouseConfirmation{
		OrderID:   exec.WorkflowID().String(),
		Available: true,
	})
	require.NoError(t, err, "Failed to send signal")
	t.Log("Signal sent successfully")

	// Query workflow status (use explicit engine)
	status, err := flows.QueryWith(engine, ctx, exec.WorkflowName(), exec.WorkflowID())
	require.NoError(t, err, "Failed to query workflow")
	t.Logf("Workflow status: %+v", status)

	// Wait for workflow completion (with timeout)
	resultChan := make(chan struct {
		result *OrderOutput
		err    error
	})

	go func() {
		result, err := exec.Get(ctx)
		resultChan <- struct {
			result *OrderOutput
			err    error
		}{result, err}
	}()

	select {
	case res := <-resultChan:
		require.NoError(t, res.err, "Workflow execution failed")
		require.NotNil(t, res.result, "Result should not be nil")
		assert.NotEmpty(t, res.result.OrderID, "Order ID should not be empty")
		assert.NotEmpty(t, res.result.TrackingNumber, "Tracking number should not be empty")
		assert.Equal(t, "shipped", res.result.Status, "Status should be shipped")
		t.Logf("Order completed: %+v", res.result)
	case <-time.After(30 * time.Second):
		t.Fatal("Workflow did not complete within timeout")
	}
}

func TestOrderWorkflow_WithTransaction(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Setup database connection
	pool := SetupTestDB(t)

	// Create engine
	engine := flows.NewEngine(pool)

	// Set tenant ID
	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	// Create unique workflow for this test to avoid worker interference
	testWorkflow := flows.New(
		"order-workflow-transaction-test",
		1,
		func(ctx *flows.Context[OrderInput]) (*OrderOutput, error) {
			input := ctx.Input()
			orderUUID, err := ctx.UUIDv7()
			if err != nil {
				return nil, err
			}
			orderID := orderUUID.String()

			fmt.Printf("Starting order workflow: order_id=%s, customer=%s\n", orderID, input.CustomerID)

			payment, err := flows.ExecuteActivity(ctx, ChargePaymentActivity, &ChargePaymentInput{
				CustomerID: input.CustomerID,
				Amount:     input.TotalPrice,
			})
			if err != nil {
				return nil, fmt.Errorf("payment failed: %w", err)
			}

			if !payment.Success {
				return nil, fmt.Errorf("payment was declined")
			}

			fmt.Printf("Payment successful: transaction_id=%s\n", payment.TransactionID)

			fmt.Println("Waiting for warehouse confirmation...")
			confirmation, err := flows.WaitForSignal[OrderInput, WarehouseConfirmation](ctx, "warehouse-ready")
			if err != nil {
				return nil, fmt.Errorf("warehouse confirmation failed: %w", err)
			}

			if !confirmation.Available {
				return nil, fmt.Errorf("items not available in warehouse")
			}

			fmt.Println("Warehouse confirmed items available")

			shipment, err := flows.ExecuteActivity(ctx, ShipOrderActivity, &ShipOrderInput{
				OrderID: orderID,
				Items:   input.Items,
			})
			if err != nil {
				return nil, fmt.Errorf("shipping failed: %w", err)
			}

			fmt.Printf("Order shipped: tracking=%s\n", shipment.TrackingNumber)

			return &OrderOutput{
				OrderID:        orderID,
				TrackingNumber: shipment.TrackingNumber,
				Status:         "shipped",
			}, nil
		},
	)

	// Start worker BEFORE beginning transaction to ensure it's ready
	// This avoids race conditions where the worker might miss the workflow
	worker := flows.NewWorker(pool, flows.WorkerConfig{
		Concurrency:   5,
		WorkflowNames: []string{"order-workflow-transaction-test"},
		PollInterval:  100 * time.Millisecond, // Faster polling for test responsiveness
	})

	workerCtx, cancel := context.WithCancel(ctx)

	// Ensure proper cleanup order: cancel context first, then stop worker
	defer func() {
		cancel()
		worker.Stop()
	}()

	go func() {
		if err := worker.Run(workerCtx); err != nil && err != context.Canceled {
			t.Logf("Worker error: %v", err)
		}
	}()

	// Small delay to ensure worker is polling
	time.Sleep(200 * time.Millisecond)

	// Begin transaction
	tx, err := pool.Begin(ctx)
	require.NoError(t, err, "Failed to begin transaction")

	// Start workflow atomically within transaction (use explicit engine)
	exec, err := flows.StartWith(engine, ctx, testWorkflow, &OrderInput{
		CustomerID: "cust-456",
		Items:      []string{"item-3"},
		TotalPrice: 49.99,
	}, flows.WithTx(tx))
	require.NoError(t, err, "Failed to start workflow")

	// Commit transaction - worker should pick it up immediately
	err = tx.Commit(ctx)
	require.NoError(t, err, "Failed to commit transaction")

	assert.NotEmpty(t, exec.WorkflowID(), "Workflow ID should not be empty")
	t.Logf("Workflow started atomically: id=%s", exec.WorkflowID())

	// Wait for workflow to be picked up and reach the signal-waiting state
	// Poll the workflow status until it's running (which means worker has processed it)
	waitStart := time.Now()
	workflowRunning := false
	for time.Since(waitStart) < 10*time.Second {
		status, err := flows.QueryWith(engine, ctx, exec.WorkflowName(), exec.WorkflowID())
		if err == nil && status.Status == flows.StatusRunning {
			t.Logf("Workflow is running after %v", time.Since(waitStart))
			workflowRunning = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	if !workflowRunning {
		t.Logf("Warning: Workflow may not be running yet after 10s")
	}

	// Additional delay to ensure workflow reaches the signal wait point
	time.Sleep(1 * time.Second)

	err = flows.SendSignalWith(engine, ctx, exec.WorkflowName(), exec.WorkflowID(), "warehouse-ready", &WarehouseConfirmation{
		OrderID:   exec.WorkflowID().String(),
		Available: true,
	})
	require.NoError(t, err, "Failed to send signal")

	// Wait for completion
	resultChan := make(chan struct {
		result *OrderOutput
		err    error
	})

	go func() {
		result, err := exec.Get(ctx)
		resultChan <- struct {
			result *OrderOutput
			err    error
		}{result, err}
	}()

	select {
	case res := <-resultChan:
		require.NoError(t, res.err, "Workflow execution failed")
		require.NotNil(t, res.result, "Result should not be nil")
		t.Logf("Order completed: %+v", res.result)
	case <-time.After(30 * time.Second):
		t.Fatal("Workflow did not complete within timeout")
	}
}

func TestOrderWorkflow_SignalTimeout(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Setup database connection
	pool := SetupTestDB(t)

	// Create engine
	engine := flows.NewEngine(pool)

	// Set tenant ID
	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	// Create unique workflow for this test to avoid worker interference
	testWorkflow := flows.New(
		"order-workflow-timeout-test",
		1,
		func(ctx *flows.Context[OrderInput]) (*OrderOutput, error) {
			input := ctx.Input()
			orderUUID, err := ctx.UUIDv7()
			if err != nil {
				return nil, err
			}
			orderID := orderUUID.String()

			fmt.Printf("Starting order workflow: order_id=%s, customer=%s\n", orderID, input.CustomerID)

			payment, err := flows.ExecuteActivity(ctx, ChargePaymentActivity, &ChargePaymentInput{
				CustomerID: input.CustomerID,
				Amount:     input.TotalPrice,
			})
			if err != nil {
				return nil, fmt.Errorf("payment failed: %w", err)
			}

			if !payment.Success {
				return nil, fmt.Errorf("payment was declined")
			}

			fmt.Printf("Payment successful: transaction_id=%s\n", payment.TransactionID)

			fmt.Println("Waiting for warehouse confirmation...")
			confirmation, err := flows.WaitForSignal[OrderInput, WarehouseConfirmation](ctx, "warehouse-ready")
			if err != nil {
				return nil, fmt.Errorf("warehouse confirmation failed: %w", err)
			}

			if !confirmation.Available {
				return nil, fmt.Errorf("items not available in warehouse")
			}

			fmt.Println("Warehouse confirmed items available")

			shipment, err := flows.ExecuteActivity(ctx, ShipOrderActivity, &ShipOrderInput{
				OrderID: orderID,
				Items:   input.Items,
			})
			if err != nil {
				return nil, fmt.Errorf("shipping failed: %w", err)
			}

			fmt.Printf("Order shipped: tracking=%s\n", shipment.TrackingNumber)

			return &OrderOutput{
				OrderID:        orderID,
				TrackingNumber: shipment.TrackingNumber,
				Status:         "shipped",
			}, nil
		},
	)

	// Start workflow (use explicit engine)
	exec, err := flows.StartWith(engine, ctx, testWorkflow, &OrderInput{
		CustomerID: "cust-789",
		Items:      []string{"item-4"},
		TotalPrice: 29.99,
	})
	require.NoError(t, err, "Failed to start workflow")

	// Start worker
	worker := flows.NewWorker(pool, flows.WorkerConfig{
		Concurrency:   5,
		WorkflowNames: []string{"order-workflow-timeout-test"},
		PollInterval:  500 * time.Millisecond,
	})

	workerCtx, cancel := context.WithCancel(ctx)

	// Ensure proper cleanup order: cancel context first, then stop worker
	defer func() {
		cancel()
		worker.Stop()
	}()

	go func() {
		if err := worker.Run(workerCtx); err != nil && err != context.Canceled {
			t.Logf("Worker error: %v", err)
		}
	}()

	// Wait for workflow to start processing
	time.Sleep(2 * time.Second)

	// Query status - should be waiting for signal (use explicit engine)
	status, err := flows.QueryWith(engine, ctx, exec.WorkflowName(), exec.WorkflowID())
	require.NoError(t, err, "Failed to query workflow")
	t.Logf("Workflow status (waiting for signal): %+v", status)

	// Don't send signal - just verify workflow is waiting
	// In a real test, you might want to test timeout behavior
}

func TestOrderWorkflow_SignalBeforeWorker(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	pool := SetupTestDB(t)

	engine := flows.NewEngine(pool)

	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	// Create unique workflow for this test to avoid worker interference
	testWorkflow := flows.New(
		"order-workflow-presignal-test",
		1,
		func(ctx *flows.Context[OrderInput]) (*OrderOutput, error) {
			input := ctx.Input()
			orderUUID, err := ctx.UUIDv7()
			if err != nil {
				return nil, err
			}
			orderID := orderUUID.String()

			fmt.Printf("Starting order workflow: order_id=%s, customer=%s\n", orderID, input.CustomerID)

			payment, err := flows.ExecuteActivity(ctx, ChargePaymentActivity, &ChargePaymentInput{
				CustomerID: input.CustomerID,
				Amount:     input.TotalPrice,
			})
			if err != nil {
				return nil, fmt.Errorf("payment failed: %w", err)
			}

			if !payment.Success {
				return nil, fmt.Errorf("payment was declined")
			}

			fmt.Printf("Payment successful: transaction_id=%s\n", payment.TransactionID)

			fmt.Println("Waiting for warehouse confirmation...")
			confirmation, err := flows.WaitForSignal[OrderInput, WarehouseConfirmation](ctx, "warehouse-ready")
			if err != nil {
				return nil, fmt.Errorf("warehouse confirmation failed: %w", err)
			}

			if !confirmation.Available {
				return nil, fmt.Errorf("items not available in warehouse")
			}

			fmt.Println("Warehouse confirmed items available")

			shipment, err := flows.ExecuteActivity(ctx, ShipOrderActivity, &ShipOrderInput{
				OrderID: orderID,
				Items:   input.Items,
			})
			if err != nil {
				return nil, fmt.Errorf("shipping failed: %w", err)
			}

			fmt.Printf("Order shipped: tracking=%s\n", shipment.TrackingNumber)

			return &OrderOutput{
				OrderID:        orderID,
				TrackingNumber: shipment.TrackingNumber,
				Status:         "shipped",
			}, nil
		},
	)

	exec, err := flows.StartWith(engine, ctx, testWorkflow, &OrderInput{
		CustomerID: "cust-pre-signal",
		Items:      []string{"item-a", "item-b"},
		TotalPrice: 149.50,
	})
	require.NoError(t, err)

	err = flows.SendSignalWith(engine, ctx, exec.WorkflowName(), exec.WorkflowID(), "warehouse-ready", &WarehouseConfirmation{
		OrderID:   exec.WorkflowID().String(),
		Available: true,
	})
	require.NoError(t, err, "pre-delivered signal should be accepted")

	worker := flows.NewWorker(pool, flows.WorkerConfig{
		Concurrency:   5,
		WorkflowNames: []string{"order-workflow-presignal-test"},
		PollInterval:  100 * time.Millisecond, // Faster polling for quicker pickup
	})

	workerCtx, cancel := context.WithCancel(ctx)

	// Ensure proper cleanup order: cancel context first, then stop worker
	defer func() {
		cancel()
		worker.Stop()
	}()

	go func() {
		if err := worker.Run(workerCtx); err != nil && err != context.Canceled {
			t.Logf("Worker error: %v", err)
		}
	}()

	// Give worker time to start polling before we expect results
	time.Sleep(300 * time.Millisecond)

	resultChan := make(chan struct {
		result *OrderOutput
		err    error
	})

	go func() {
		result, err := exec.Get(ctx)
		resultChan <- struct {
			result *OrderOutput
			err    error
		}{result, err}
	}()

	select {
	case res := <-resultChan:
		require.NoError(t, res.err)
		require.NotNil(t, res.result)
		assert.Equal(t, "shipped", res.result.Status)
	case <-time.After(30 * time.Second):
		t.Fatal("workflow did not complete")
	}

	status, err := flows.QueryWith(engine, ctx, exec.WorkflowName(), exec.WorkflowID())
	require.NoError(t, err)
	assert.Equal(t, flows.StatusCompleted, status.Status)
}
