package rpc

import (
	"github.com/google/uuid"

	"github.com/gt-tech-ai/knowledge-engine/go/core/errors"
)

// ParseUUID parses a UUID request field, returning CodeInvalidInput when it is
// missing or malformed (field names the offending request field, so the caller
// error points at the exact input).
func ParseUUID(s, field string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, errors.InvalidInput(field + " must be a valid UUID")
	}
	return id, nil
}
