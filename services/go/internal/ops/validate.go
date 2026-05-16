package ops

import (
	"encoding/json"
	"fmt"
)

// ValidationError describes why a request failed structural validation.
// Code maps to the RequestError.code enum from the spec.
type ValidationError struct {
	Code    string
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

func schemaErr(format string, args ...any) *ValidationError {
	return &ValidationError{Code: "SCHEMA_VIOLATION", Message: fmt.Sprintf(format, args...)}
}
func unknownOpErr(name string) *ValidationError {
	return &ValidationError{Code: "UNKNOWN_OP", Message: fmt.Sprintf("unknown op name %q", name)}
}
func emptyOpsErr() *ValidationError {
	return &ValidationError{Code: "EMPTY_OPS", Message: "ops must be a non-empty array"}
}
func missingFieldErr(field string) *ValidationError {
	return &ValidationError{Code: "MISSING_FIELD", Message: fmt.Sprintf("missing required field %q", field)}
}

// DecodeAndValidate parses body into a Request and verifies structural
// requirements that map to a 400 RequestError. Returns the parsed Request and
// decoded ops, or a ValidationError describing the failure.
func DecodeAndValidate(body []byte) (*Request, []Op, *ValidationError) {
	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, nil, schemaErr("invalid JSON: %v", err)
	}
	if req.Database == nil {
		return nil, nil, missingFieldErr("database")
	}
	if *req.Database == "" {
		return nil, nil, schemaErr("`database` must be non-empty")
	}
	if req.Ops == nil {
		return nil, nil, missingFieldErr("ops")
	}
	if len(req.Ops) == 0 {
		return nil, nil, emptyOpsErr()
	}

	ops := make([]Op, len(req.Ops))
	for i, raw := range req.Ops {
		var op Op
		if err := json.Unmarshal(raw, &op); err != nil {
			return nil, nil, schemaErr("ops[%d]: invalid JSON: %v", i, err)
		}
		if verr := validateOp(&op, false); verr != nil {
			verr.Message = fmt.Sprintf("ops[%d]: %s", i, verr.Message)
			return nil, nil, verr
		}
		ops[i] = op
	}
	return &req, ops, nil
}

// validateOp checks that op carries the fields its name requires. inBulk=true
// relaxes the requirement that `collection` be present.
func validateOp(op *Op, inBulk bool) *ValidationError {
	if op.Name == "" {
		return missingFieldErr("name")
	}
	if !inBulk && op.Collection == "" {
		return schemaErr("collection must be non-empty")
	}
	switch op.Name {
	case "insertOne":
		if len(op.Document) == 0 {
			return missingFieldErr("document")
		}
	case "insertMany":
		if len(op.Documents) == 0 {
			return missingFieldErr("documents")
		}
	case "find":
		if len(op.Filter) == 0 {
			return missingFieldErr("filter")
		}
	case "updateOne", "updateMany":
		if len(op.Filter) == 0 {
			return missingFieldErr("filter")
		}
		if len(op.Update) == 0 {
			return missingFieldErr("update")
		}
	case "replaceOne":
		if len(op.Filter) == 0 {
			return missingFieldErr("filter")
		}
		if len(op.Replacement) == 0 {
			return missingFieldErr("replacement")
		}
	case "deleteOne", "deleteMany":
		if len(op.Filter) == 0 {
			return missingFieldErr("filter")
		}
	case "countDocuments":
		if len(op.Filter) == 0 {
			return missingFieldErr("filter")
		}
	case "aggregate":
		if op.Pipeline == nil {
			return missingFieldErr("pipeline")
		}
	case "bulkWrite":
		if inBulk {
			return unknownOpErr("bulkWrite (nested)")
		}
		if len(op.Operations) == 0 {
			return missingFieldErr("operations")
		}
		for i := range op.Operations {
			if verr := validateOp(&op.Operations[i], true); verr != nil {
				verr.Message = fmt.Sprintf("operations[%d]: %s", i, verr.Message)
				return verr
			}
		}
	default:
		return unknownOpErr(op.Name)
	}
	return nil
}

