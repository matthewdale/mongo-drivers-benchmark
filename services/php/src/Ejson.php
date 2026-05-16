<?php
declare(strict_types=1);

namespace MongoDriversBenchmark;

use MongoDB\BSON\Document;
use MongoDB\BSON\PackedArray;
use MongoDB\BSON\ObjectId;

/**
 * Extended JSON v2 <-> driver-BSON translation.
 *
 * Decoding strategy (HTTP JSON → driver values):
 *   - Parse the incoming JSON.
 *   - Re-emit a single field (key="v") through Document::fromJSON which
 *     accepts Extended JSON v2 in canonical or relaxed form, then read it
 *     back out via toPHP(). For scalar / id values this recovers a real
 *     ObjectId / Int64 / Decimal128 / etc.
 *
 * Encoding strategy (driver values → HTTP JSON):
 *   - Wrap in {v: value}, run Document::toRelaxedExtendedJSON, then slice the
 *     wrapper. The result is relaxed Extended JSON v2: numerics emit as JSON
 *     numbers, non-numeric BSON types use the {$...} envelope.
 *
 * The relaxed form is exactly what spec §5.1 (amended) requires for both
 * request and response bodies.
 */
final class Ejson
{
    /**
     * Encode an arbitrary value as relaxed Extended JSON v2 (string of JSON).
     *
     * Accepts native PHP scalars/arrays and driver BSON types.
     */
    public static function encodeValue(mixed $value): string
    {
        // Quick paths for primitives that need no driver round-trip.
        if ($value === null) {
            return 'null';
        }
        if (is_bool($value)) {
            return $value ? 'true' : 'false';
        }
        if (is_int($value)) {
            // PHP ints: relaxed form is the plain decimal.
            return (string) $value;
        }
        if (is_float($value)) {
            // Special floats need canonical envelopes.
            if (is_nan($value)) {
                return '{"$numberDouble":"NaN"}';
            }
            if (is_infinite($value)) {
                return $value > 0 ? '{"$numberDouble":"Infinity"}' : '{"$numberDouble":"-Infinity"}';
            }
            $s = json_encode($value, JSON_PRESERVE_ZERO_FRACTION);
            if ($s === false) {
                throw new \RuntimeException('encodeValue: json_encode failed for float');
            }
            return $s;
        }
        if (is_string($value)) {
            $s = json_encode($value, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES);
            if ($s === false) {
                throw new \RuntimeException('encodeValue: json_encode failed for string');
            }
            return $s;
        }

        // For driver BSON values and documents/arrays, round-trip via Document.
        // Wrap as { v: <value> } and slice out the value.
        $doc = Document::fromPHP(['v' => $value]);
        $json = $doc->toRelaxedExtendedJSON();
        return self::sliceWrapper($json);
    }

    /**
     * Encode a whole document/array (associative or list) as a relaxed EJSON
     * JSON object/array string.
     */
    public static function encodeDocument(mixed $value): string
    {
        if ($value === null) {
            return 'null';
        }
        if ($value instanceof Document || $value instanceof PackedArray) {
            return $value->toRelaxedExtendedJSON();
        }
        $doc = Document::fromPHP(['v' => $value]);
        return self::sliceWrapper($doc->toRelaxedExtendedJSON());
    }

    /**
     * Decode raw JSON bytes (possibly containing Extended JSON v2 in canonical
     * or relaxed form) into a driver-friendly PHP value via the BSON parser.
     *
     * Returns whatever Document::toPHP() yields. Accepts JSON objects or
     * arrays at the root.
     */
    public static function decodeDocument(string $json): mixed
    {
        $trimmed = ltrim($json);
        if ($trimmed === '' || $trimmed === 'null') {
            // Treat empty / null inputs as empty document (matches Go service).
            return (object) [];
        }
        // Wrap so we can use Document::fromJSON on either object or array roots.
        $wrapped = '{"v":' . $json . '}';
        $doc = Document::fromJSON($wrapped);
        $arr = $doc->toPHP([
            'root' => 'array',
            'document' => 'array',
            'array' => 'array',
        ]);
        return $arr['v'] ?? (object) [];
    }

    /**
     * Decode raw JSON value bytes (possibly Extended JSON v2) into a driver
     * value suitable for use as a filter/update component.
     */
    public static function decodeValue(string $json): mixed
    {
        return self::decodeDocument($json);
    }

    /**
     * Wrap a driver value so it can be used as a filter / replacement / update
     * doc. If a JSON-decoded array has integer keys treat it as a list.
     */
    public static function ensureArrayShape(mixed $value): array|object
    {
        if (is_array($value)) {
            return $value;
        }
        if (is_object($value)) {
            return (array) $value;
        }
        if ($value === null) {
            return [];
        }
        throw new \InvalidArgumentException('expected array/object, got ' . gettype($value));
    }

    /**
     * Slice the {"v": <encoded>} wrapper produced by Document::toRelaxedExtendedJSON.
     * Returns the JSON-encoded value portion.
     */
    private static function sliceWrapper(string $wrapped): string
    {
        // wrapped is of form {"v":<value>}. Use a JSON decode to extract the
        // value portion verbatim — safest against whitespace variation.
        // Use JSON_THROW_ON_ERROR to surface anomalies.
        // We re-encode in a streaming-friendly way: parse and walk to "v",
        // then re-serialize the matching value via json_encode on the decoded
        // structure. That re-encode can change numeric formatting for floats
        // but the only place we use sliceWrapper is to extract values from a
        // round-trip — the values themselves are produced by toRelaxedExtendedJSON
        // and re-encode via json_encode is fine.
        //
        // Alternative: parse with a manual scan. Simpler: decode then json_encode.
        $decoded = json_decode($wrapped, true, 512, JSON_THROW_ON_ERROR);
        if (!is_array($decoded) || !array_key_exists('v', $decoded)) {
            throw new \RuntimeException('sliceWrapper: missing v in ' . $wrapped);
        }
        $enc = json_encode($decoded['v'], JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE | JSON_PRESERVE_ZERO_FRACTION);
        if ($enc === false) {
            throw new \RuntimeException('sliceWrapper: re-encode failed');
        }
        return $enc;
    }

    /**
     * Whether a PHP associative array (or stdClass) has a top-level "_id".
     */
    public static function hasId(mixed $doc): bool
    {
        if (is_array($doc)) {
            return array_key_exists('_id', $doc);
        }
        if (is_object($doc)) {
            return property_exists($doc, '_id') || isset(((array) $doc)['_id']);
        }
        return false;
    }

    /**
     * Return [docWithId, id] — assigning a fresh ObjectId if doc lacked one.
     */
    public static function ensureId(mixed $doc): array
    {
        if (self::hasId($doc)) {
            $id = is_array($doc) ? $doc['_id'] : ((array) $doc)['_id'];
            return [$doc, $id];
        }
        $oid = new ObjectId();
        if (is_array($doc)) {
            $out = ['_id' => $oid] + $doc;
            return [$out, $oid];
        }
        // For object (stdClass), convert to array-with-_id-first.
        $out = ['_id' => $oid] + (array) $doc;
        return [$out, $oid];
    }
}
