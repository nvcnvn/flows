package storage

import (
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// UUIDToPgtype converts a google/uuid.UUID to pgtype.UUID.
func UUIDToPgtype(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{
		Bytes: id,
		Valid: true,
	}
}

// PgtypeToUUID converts a pgtype.UUID to google/uuid.UUID.
func PgtypeToUUID(id pgtype.UUID) uuid.UUID {
	if !id.Valid {
		return uuid.Nil
	}
	return id.Bytes
}

// UUIDPtrToPgtype converts a *google/uuid.UUID to *pgtype.UUID.
func UUIDPtrToPgtype(id *uuid.UUID) *pgtype.UUID {
	if id == nil {
		return nil
	}
	result := UUIDToPgtype(*id)
	return &result
}

// PgtypeToUUIDPtr converts a *pgtype.UUID to *google/uuid.UUID.
func PgtypeToUUIDPtr(id *pgtype.UUID) *uuid.UUID {
	if id == nil || !id.Valid {
		return nil
	}
	result := PgtypeToUUID(*id)
	return &result
}
