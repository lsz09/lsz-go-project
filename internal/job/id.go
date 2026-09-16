package job

import (
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
)

// ValidateID verifies that an external Job ID is a canonical hyphenated UUID.
func ValidateID(id string) error {
	_, err := parseJobUUID("validate job id", id)
	return err
}

// parseJobUUID validates an external ID and converts it to a PostgreSQL UUID argument.
func parseJobUUID(operation string, id string) (pgtype.UUID, error) {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return pgtype.UUID{}, fmt.Errorf("%s: %w: id must be a hyphenated UUID", operation, ErrInvalidInput)
	}

	var uuid pgtype.UUID
	if err := uuid.Scan(id); err != nil || !uuid.Valid {
		return pgtype.UUID{}, fmt.Errorf("%s: %w: id must be a UUID", operation, ErrInvalidInput)
	}
	return uuid, nil
}
