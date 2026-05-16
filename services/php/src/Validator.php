<?php
declare(strict_types=1);

namespace MongoDriversBenchmark;

/**
 * Decode and validate /v1/ops request bodies.
 *
 * Returns ['database' => string, 'ops' => array] on success, where each op is
 * an associative array that carries the JSON-decoded primitives plus
 * `__*_raw` keys holding the original raw JSON bytes for BSON-typed fields
 * (filter, document, update, etc.). The Ops dispatcher reads the raw bytes
 * and feeds them to the Extended-JSON-aware decoder.
 */
final class Validator
{
    /** Names of BSON-document-valued fields whose raw JSON we must preserve. */
    private const RAW_FIELDS = [
        'document', 'filter', 'projection', 'sort', 'update', 'replacement',
    ];

    /**
     * Decode and validate. On schema failure throws ValidationError.
     *
     * @return array{database: string, ops: list<array>}
     */
    public static function decode(string $body): array
    {
        // Decode twice: once to get scalars (associative), once to extract
        // the raw JSON sub-bytes for BSON-typed fields (find them by scanning
        // the raw body via a streaming tokenizer is complex; instead we
        // re-encode the decoded structure verbatim, which works because the
        // shape is fixed and we only need to feed the Extended-JSON parser
        // representable JSON of the same value).
        try {
            $decoded = json_decode($body, true, 512, JSON_THROW_ON_ERROR);
        } catch (\JsonException $e) {
            throw new ValidationError('SCHEMA_VIOLATION', 'invalid JSON: ' . $e->getMessage());
        }
        if (!is_array($decoded)) {
            throw new ValidationError('SCHEMA_VIOLATION', 'request must be an object');
        }

        if (!array_key_exists('database', $decoded)) {
            throw new ValidationError('MISSING_FIELD', 'missing required field "database"');
        }
        $db = $decoded['database'];
        if (!is_string($db) || $db === '') {
            throw new ValidationError('SCHEMA_VIOLATION', '`database` must be a non-empty string');
        }
        if (!array_key_exists('ops', $decoded)) {
            throw new ValidationError('MISSING_FIELD', 'missing required field "ops"');
        }
        if (!is_array($decoded['ops'])) {
            throw new ValidationError('SCHEMA_VIOLATION', '`ops` must be an array');
        }
        if (count($decoded['ops']) === 0) {
            throw new ValidationError('EMPTY_OPS', 'ops must be a non-empty array');
        }

        $ops = [];
        foreach ($decoded['ops'] as $i => $rawOp) {
            if (!is_array($rawOp)) {
                throw new ValidationError('SCHEMA_VIOLATION', "ops[$i]: must be an object");
            }
            $op = self::validateOp($rawOp, false);
            $ops[] = $op;
        }
        return ['database' => $db, 'ops' => $ops];
    }

    /**
     * Validate one op (top-level or bulk sub-op). Mutates $op to add the
     * `__*_raw` keys for BSON-typed fields, and (for bulkWrite) `__operations`.
     */
    private static function validateOp(array $op, bool $inBulk): array
    {
        $name = $op['name'] ?? null;
        if (!is_string($name) || $name === '') {
            throw new ValidationError('MISSING_FIELD', 'missing required field "name"');
        }
        if (!$inBulk) {
            $coll = $op['collection'] ?? null;
            if (!is_string($coll) || $coll === '') {
                throw new ValidationError('SCHEMA_VIOLATION', 'collection must be non-empty');
            }
        }

        // Stash raw JSON bytes for BSON-typed fields so the dispatcher can
        // pass them to the Extended-JSON-aware decoder.
        foreach (self::RAW_FIELDS as $f) {
            if (array_key_exists($f, $op)) {
                $op['__' . $f . '_raw'] = self::encodeRaw($op[$f]);
            }
        }
        if (array_key_exists('documents', $op) && is_array($op['documents'])) {
            $raws = [];
            foreach ($op['documents'] as $d) {
                $raws[] = self::encodeRaw($d);
            }
            $op['__documents_raw'] = $raws;
        }
        if (array_key_exists('pipeline', $op) && is_array($op['pipeline'])) {
            $raws = [];
            foreach ($op['pipeline'] as $stage) {
                $raws[] = self::encodeRaw($stage);
            }
            $op['__pipeline_raw'] = $raws;
        }
        if (array_key_exists('array_filters', $op) && is_array($op['array_filters'])) {
            $raws = [];
            foreach ($op['array_filters'] as $af) {
                $raws[] = self::encodeRaw($af);
            }
            $op['__array_filters_raw'] = $raws;
        }

        switch ($name) {
            case 'insertOne':
                if (!array_key_exists('document', $op)) {
                    throw new ValidationError('MISSING_FIELD', 'missing required field "document"');
                }
                break;
            case 'insertMany':
                if (!array_key_exists('documents', $op) || !is_array($op['documents']) || count($op['documents']) === 0) {
                    throw new ValidationError('MISSING_FIELD', 'missing required field "documents"');
                }
                break;
            case 'find':
                if (!array_key_exists('filter', $op)) {
                    throw new ValidationError('MISSING_FIELD', 'missing required field "filter"');
                }
                break;
            case 'updateOne':
            case 'updateMany':
                if (!array_key_exists('filter', $op)) {
                    throw new ValidationError('MISSING_FIELD', 'missing required field "filter"');
                }
                if (!array_key_exists('update', $op)) {
                    throw new ValidationError('MISSING_FIELD', 'missing required field "update"');
                }
                break;
            case 'replaceOne':
                if (!array_key_exists('filter', $op)) {
                    throw new ValidationError('MISSING_FIELD', 'missing required field "filter"');
                }
                if (!array_key_exists('replacement', $op)) {
                    throw new ValidationError('MISSING_FIELD', 'missing required field "replacement"');
                }
                break;
            case 'deleteOne':
            case 'deleteMany':
            case 'countDocuments':
                if (!array_key_exists('filter', $op)) {
                    throw new ValidationError('MISSING_FIELD', 'missing required field "filter"');
                }
                break;
            case 'aggregate':
                if (!array_key_exists('pipeline', $op) || !is_array($op['pipeline'])) {
                    throw new ValidationError('MISSING_FIELD', 'missing required field "pipeline"');
                }
                break;
            case 'bulkWrite':
                if ($inBulk) {
                    throw new ValidationError('UNKNOWN_OP', 'bulkWrite cannot be nested');
                }
                if (!array_key_exists('operations', $op) || !is_array($op['operations']) || count($op['operations']) === 0) {
                    throw new ValidationError('MISSING_FIELD', 'missing required field "operations"');
                }
                $subs = [];
                foreach ($op['operations'] as $i => $sub) {
                    if (!is_array($sub)) {
                        throw new ValidationError('SCHEMA_VIOLATION', "operations[$i]: must be an object");
                    }
                    $subs[] = self::validateOp($sub, true);
                }
                $op['__operations'] = $subs;
                break;
            default:
                throw new ValidationError('UNKNOWN_OP', 'unknown op name "' . $name . '"');
        }
        return $op;
    }

    /**
     * Re-encode a decoded JSON value back to its JSON form, suitable for
     * feeding to the Extended-JSON parser.
     *
     * Caveat: the validator decodes the body via json_decode(assoc=true),
     * which collapses empty JSON objects to PHP empty arrays. We can't
     * distinguish `{}` from `[]` after that decode. To preserve the
     * BSON-document shape, we re-walk the value: any associative array that
     * is "empty or non-list" stays as an object on re-encode; lists stay as
     * arrays. Strict list detection uses array_is_list().
     */
    private static function encodeRaw(mixed $v): string
    {
        $normalized = self::normalizeForReEncode($v);
        $enc = json_encode($normalized, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_PRESERVE_ZERO_FRACTION);
        if ($enc === false) {
            throw new ValidationError('SCHEMA_VIOLATION', 'failed to re-encode raw JSON');
        }
        return $enc;
    }

    /**
     * Recursively coerce empty arrays to stdClass so json_encode emits `{}`
     * rather than `[]`. Non-empty list arrays stay arrays. Non-list arrays
     * become stdClass.
     */
    private static function normalizeForReEncode(mixed $v): mixed
    {
        if (is_array($v)) {
            if (count($v) === 0) {
                return new \stdClass();
            }
            if (array_is_list($v)) {
                $out = [];
                foreach ($v as $item) {
                    $out[] = self::normalizeForReEncode($item);
                }
                return $out;
            }
            $obj = new \stdClass();
            foreach ($v as $k => $item) {
                $obj->{$k} = self::normalizeForReEncode($item);
            }
            return $obj;
        }
        return $v;
    }
}

