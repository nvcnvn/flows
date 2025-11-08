package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nvcnvn/flows"
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
		orderID := ctx.UUIDv7().String()

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

func main() {
	ctx := context.Background()

	// Connect to PostgreSQL
	connString := "postgres://postgres:postgres@localhost:5432/flows?sslmode=disable"
	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer pool.Close()

	// Create engine
	engine := flows.NewEngine(pool)
	flows.SetEngine(engine)

	// Set tenant ID
	tenantID := uuid.New()
	ctx = flows.WithTenantID(ctx, tenantID)

	fmt.Printf("Using tenant ID: %s\n\n", tenantID)

	// Example 1: Start a simple workflow
	fmt.Println("=== Example 1: Start Workflow ===")
	exec, err := flows.Start(ctx, OrderWorkflow, &OrderInput{
		CustomerID: "cust-123",
		Items:      []string{"item-1", "item-2"},
		TotalPrice: 99.99,
	})
	if err != nil {
		log.Fatalf("Failed to start workflow: %v", err)
	}

	fmt.Printf("Workflow started: id=%s\n\n", exec.WorkflowID())

	// Example 2: Start workflow with transaction (atomic with app state)
	fmt.Println("=== Example 2: Start Workflow with Transaction ===")
	tx, err := pool.Begin(ctx)
	if err != nil {
		log.Fatalf("Failed to begin transaction: %v", err)
	}

	// Simulate inserting app data
	fmt.Println("Inserting order into app database...")
	// _, err = tx.Exec(ctx, "INSERT INTO orders ...")

	// Start workflow atomically
	exec2, err := flows.Start(ctx, OrderWorkflow, &OrderInput{
		CustomerID: "cust-456",
		Items:      []string{"item-3"},
		TotalPrice: 49.99,
	}, flows.WithTx(tx))
	if err != nil {
		tx.Rollback(ctx)
		log.Fatalf("Failed to start workflow: %v", err)
	}

	if err := tx.Commit(ctx); err != nil {
		log.Fatalf("Failed to commit transaction: %v", err)
	}

	fmt.Printf("Workflow started atomically: id=%s\n\n", exec2.WorkflowID())

	// Example 3: Send signal to workflow
	fmt.Println("=== Example 3: Send Signal ===")
	time.Sleep(2 * time.Second) // Wait a bit for workflow to start

	err = flows.SendSignal(ctx, exec.WorkflowID(), "warehouse-ready", &WarehouseConfirmation{
		OrderID:   exec.WorkflowID().String(),
		Available: true,
	})
	if err != nil {
		log.Printf("Failed to send signal: %v", err)
	} else {
		fmt.Println("Signal sent successfully")
	}

	// Example 4: Query workflow status
	fmt.Println("=== Example 4: Query Workflow Status ===")
	status, err := flows.Query(ctx, exec.WorkflowID())
	if err != nil {
		log.Printf("Failed to query workflow: %v", err)
	} else {
		fmt.Printf("Workflow status: %+v\n\n", status)
	}

	// Example 5: Start worker
	fmt.Println("=== Example 5: Start Worker ===")
	worker := flows.NewWorker(pool, flows.WorkerConfig{
		Concurrency:   5,
		WorkflowNames: []string{"order-workflow"},
		PollInterval:  1 * time.Second,
		TenantID:      tenantID,
	})

	fmt.Println("Worker started, processing tasks...")
	fmt.Println("Press Ctrl+C to stop")

	// Run worker (in production, run in separate process)
	go func() {
		if err := worker.Run(ctx); err != nil {
			log.Printf("Worker error: %v", err)
		}
	}()

	// Keep running for demo
	time.Sleep(30 * time.Second)
	worker.Stop()

	fmt.Println("\nExample completed!")
}
