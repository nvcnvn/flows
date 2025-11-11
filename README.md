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
