package com.mongodb.loadtest;

import com.mongodb.MongoBulkWriteException;
import com.mongodb.MongoClientException;
import com.mongodb.MongoCommandException;
import com.mongodb.MongoExecutionTimeoutException;
import com.mongodb.MongoQueryException;
import com.mongodb.MongoSecurityException;
import com.mongodb.MongoServerException;
import com.mongodb.MongoSocketException;
import com.mongodb.MongoSocketReadTimeoutException;
import com.mongodb.MongoSocketWriteTimeoutException;
import com.mongodb.MongoTimeoutException;
import com.mongodb.MongoWriteException;
import com.mongodb.bulk.BulkWriteError;

import java.util.List;

/**
 * Classifies driver exceptions into the spec's eight normalized ErrorCodes.
 * See spec/http-api.md §7.
 *
 * Precedence: server error code → driver exception class → INTERNAL.
 */
final class Errors {

    private Errors() {}

    static String toResultJson(String opName, Throwable t) {
        Classification c = classify(t);
        StringBuilder sb = new StringBuilder();
        sb.append("{\"op\":").append(Ejson.jsonString(opName));
        sb.append(",\"ok\":false,\"error\":{");
        sb.append("\"code\":").append(Ejson.jsonString(c.code));
        sb.append(",\"message\":").append(Ejson.jsonString(c.message));
        if (c.serverCode != null) {
            sb.append(",\"server_code\":").append(c.serverCode);
        }
        sb.append("}}");
        return sb.toString();
    }

    static Classification classify(Throwable t) {
        String message = t.getMessage() == null ? t.getClass().getSimpleName() : t.getMessage();

        // 1. Server-coded errors.
        if (t instanceof MongoWriteException mwe) {
            int sc = mwe.getError().getCode();
            return new Classification(codeFromServer(sc), message, sc);
        }
        if (t instanceof MongoBulkWriteException mbwe) {
            List<BulkWriteError> ws = mbwe.getWriteErrors();
            if (!ws.isEmpty()) {
                int sc = ws.get(0).getCode();
                return new Classification(codeFromServer(sc), message, sc);
            }
            if (mbwe.getWriteConcernError() != null) {
                int sc = mbwe.getWriteConcernError().getCode();
                return new Classification(codeFromServer(sc), message, sc);
            }
            return new Classification("INTERNAL", message, null);
        }
        if (t instanceof MongoExecutionTimeoutException mete) {
            return new Classification("TIMEOUT", message, mete.getCode());
        }
        if (t instanceof MongoQueryException mqe) {
            int sc = mqe.getCode();
            return new Classification(codeFromServer(sc), message, sc);
        }
        if (t instanceof MongoCommandException mce) {
            int sc = mce.getErrorCode();
            return new Classification(codeFromServer(sc), message, sc);
        }
        if (t instanceof MongoServerException mse) {
            int sc = mse.getCode();
            return new Classification(codeFromServer(sc), message, sc);
        }

        // 2. Driver exception classes.
        if (t instanceof MongoSecurityException) {
            return new Classification("AUTH", message, null);
        }
        if (t instanceof MongoSocketReadTimeoutException
                || t instanceof MongoSocketWriteTimeoutException
                || t instanceof MongoTimeoutException) {
            return new Classification("TIMEOUT", message, null);
        }
        if (t instanceof MongoSocketException) {
            return new Classification("NETWORK", message, null);
        }

        // 3. Bad-argument-looking client exceptions.
        if (t instanceof IllegalArgumentException || t instanceof MongoClientException) {
            return new Classification("INVALID_ARGUMENT", message, null);
        }

        // 4. Default.
        return new Classification("INTERNAL", message, null);
    }

    private static String codeFromServer(int code) {
        return switch (code) {
            case 11000, 11001, 12582 -> "DUPLICATE_KEY";
            case 112 -> "WRITE_CONFLICT";
            case 50, 89, 262 -> "TIMEOUT";
            case 13, 18 -> "AUTH";
            case 26, 27 -> "NOT_FOUND";
            case 2, 9, 14, 40, 51, 72, 73, 4, 16, 17, 30, 31, 52, 66 -> "INVALID_ARGUMENT";
            default -> {
                if (code == 8000) yield "AUTH";
                yield "INTERNAL";
            }
        };
    }

    record Classification(String code, String message, Integer serverCode) {}
}
