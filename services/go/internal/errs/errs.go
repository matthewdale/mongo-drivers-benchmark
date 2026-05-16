// Package errs maps Go driver errors to the spec's normalized ErrorCode
// values. See spec/http-api.md §7 for the mapping table.
package errs

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/v2/mongo"
)

// Normalized error codes (matches openapi ErrorCode enum).
const (
	CodeDuplicateKey    = "DUPLICATE_KEY"
	CodeWriteConflict   = "WRITE_CONFLICT"
	CodeTimeout         = "TIMEOUT"
	CodeNetwork         = "NETWORK"
	CodeAuth            = "AUTH"
	CodeNotFound        = "NOT_FOUND"
	CodeInvalidArgument = "INVALID_ARGUMENT"
	CodeInternal        = "INTERNAL"
)

// Classified is the result of mapping a driver error to a normalized form.
type Classified struct {
	Code       string
	Message    string
	ServerCode *int // nil when the driver did not expose one
}

// Classify maps err to a Classified value following the spec table. It is
// deterministic: same error in, same code out.
//
// Precedence (per spec §7 rule 1):
//  1. Server error code (where exposed by the driver)
//  2. Driver exception class
//  3. INTERNAL
func Classify(err error) Classified {
	if err == nil {
		return Classified{Code: CodeInternal, Message: "nil error classified"}
	}
	msg := err.Error()

	// 1. Server-coded errors that the driver exposes via WriteException /
	//    BulkWriteException / CommandError. Inspect those first so we can
	//    pick the most specific normalized code.
	if code, scode, ok := classifyServerCoded(err); ok {
		return Classified{Code: code, Message: msg, ServerCode: scode}
	}

	// 2. Driver helper predicates and special sentinel errors.
	if mongo.IsDuplicateKeyError(err) {
		return Classified{Code: CodeDuplicateKey, Message: msg}
	}
	if mongo.IsTimeout(err) || errors.Is(err, context.DeadlineExceeded) {
		return Classified{Code: CodeTimeout, Message: msg}
	}
	if mongo.IsNetworkError(err) {
		return Classified{Code: CodeNetwork, Message: msg}
	}
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Classified{Code: CodeNotFound, Message: msg}
	}

	// 3. Default.
	return Classified{Code: CodeInternal, Message: msg}
}

// classifyServerCoded inspects err for a known driver error type that carries
// a server error code. Returns the normalized code, the server code, and ok
// when a classification was made.
func classifyServerCoded(err error) (string, *int, bool) {
	// CommandError: top-level command failure.
	var cmdErr mongo.CommandError
	if errors.As(err, &cmdErr) {
		code := int(cmdErr.Code)
		return codeFromServer(code), intPtr(code), true
	}

	// WriteException: returned from InsertOne/UpdateX/DeleteX/ReplaceOne on
	// write failure. Aggregate over write errors + write-concern error.
	var we mongo.WriteException
	if errors.As(err, &we) {
		if c, sc, ok := classifyWriteException(we.WriteErrors, we.WriteConcernError); ok {
			return c, sc, true
		}
	}

	// BulkWriteException: returned from BulkWrite and InsertMany.
	var bwe mongo.BulkWriteException
	if errors.As(err, &bwe) {
		// Convert []BulkWriteError to []WriteError for shared handling.
		ws := make(mongo.WriteErrors, 0, len(bwe.WriteErrors))
		for _, e := range bwe.WriteErrors {
			ws = append(ws, e.WriteError)
		}
		if c, sc, ok := classifyWriteException(ws, bwe.WriteConcernError); ok {
			return c, sc, true
		}
	}

	return "", nil, false
}

func classifyWriteException(ws mongo.WriteErrors, wce *mongo.WriteConcernError) (string, *int, bool) {
	// Prefer the first write error's code; mapping should be deterministic
	// and stable.
	if len(ws) > 0 {
		first := ws[0]
		return codeFromServer(first.Code), intPtr(first.Code), true
	}
	if wce != nil {
		return codeFromServer(wce.Code), intPtr(wce.Code), true
	}
	return "", nil, false
}

// codeFromServer maps a MongoDB server error code to a normalized error code.
// The table mirrors spec §7.
func codeFromServer(code int) string {
	switch code {
	case 11000, 11001, 12582:
		return CodeDuplicateKey
	case 112:
		return CodeWriteConflict
	case 50, 89, 262:
		return CodeTimeout
	case 13, 18, 8000:
		return CodeAuth
	case 26, 27:
		return CodeNotFound
	case 2, 9, 14, 40, 51, 72, 73, 4, 16, 17, 30, 31, 52, 66:
		// Common "bad argument" / "malformed input" codes.
		return CodeInvalidArgument
	default:
		return CodeInternal
	}
}

func intPtr(i int) *int { return &i }
