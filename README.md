# Flows - Simple Durable Workflow Orchestration for Go

[![Go Version](https://img.shields.io/badge/go-1.25+-blue.svg)](https://golang.org/dl/)
[![PostgreSQL](https://img.shields.io/badge/postgresql-18+-blue.svg)](https://www.postgresql.org/)
[![Status](https://img.shields.io/badge/status-implementation-yellow.svg)](https://github.com/nvcnvn/flows)

> A lightweight, type-safe workflow orchestration library for Go, inspired by Temporal but designed to be "far more simple"

## 🎯 Key Features

- **Type-Safe Generics**: Compile-time type checking for workflows and activities
- **Deterministic Replay**: Fault-tolerant execution with automatic state recovery
- **PostgreSQL Native**: Leverages PostgreSQL 18's UUIDv7 and JSONB features
- **Multi-Tenant Isolation**: Built-in tenant isolation at the database level
- **Transaction Support**: Atomic workflow starts with application state using `WithTx()`
- **Single Package API**: Clean, discoverable API with one import
- **Simple**: Library-based, no separate cluster or infrastructure

## 🚀 Quick Start

### Installation

```bash
go get github.com/nvcnvn/flows
```

**Requirements**: PostgreSQL 18+ (for UUIDv7 support)

### Basic Example

```go
import (
    "github.com/nvcnvn/flows"
    "github.com/jackc/pgx/v5/pgxpool"
)

// 1. Setup engine
pool, _ := pgxpool.New(ctx, "postgres://localhost/mydb")
engine := flows.NewEngine(pool)
flows.SetEngine(engine)

// 2. Define workflow
var OrderWorkflow = flows.New(
    "order-workflow", 1,
    func(ctx *flows.Context[OrderInput]) (*OrderOutput, error) {
        // Execute activities
        payment, err := flows.ExecuteActivity(ctx, ChargePayment, &ChargeInput{...})
        if err != nil {
            return nil, err
        }
        
        // Wait for signals
        conf, err := flows.WaitForSignal[OrderInput, Confirmation](ctx, "ready")
        if err != nil {
            return nil, err
        }
        
        return &OrderOutput{Status: "completed"}, nil
    },
)

// 3. Start workflow
ctx = flows.WithTenantID(ctx, tenantID)
exec, err := flows.Start(ctx, OrderWorkflow, &OrderInput{...})
```

## 🏗️ Architecture

```
┌─────────────────────────────────┐
│     Your Application            │
│  flows.Start() → Execution      │
│  flows.WithTx(tx) → atomic ops  │
└─────────────────────────────────┘
              ↓
┌─────────────────────────────────┐
│       Flows Engine              │
│  • Workflow Registry            │
│  • Task Queue Management        │
│  • Multi-Tenant Context         │
└─────────────────────────────────┘
              ↓
┌─────────────────────────────────┐
│         Workers                 │
│  • Workflow Executor (replay)   │
│  • Activity Executor (retry)    │
│  • Timer & Signal Processors    │
└─────────────────────────────────┘
              ↓
┌─────────────────────────────────┐
│     PostgreSQL Storage          │
│  • UUIDv7 time-sortable IDs     │
│  • JSONB flexible storage       │
│  • ACID transactions            │
└─────────────────────────────────┘
```
