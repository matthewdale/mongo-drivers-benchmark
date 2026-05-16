<?php
declare(strict_types=1);

namespace MongoDriversBenchmark;

use MongoDB\BSON\Document;
use MongoDB\BSON\ObjectId;
use MongoDB\Client;
use MongoDB\Collection;
use MongoDB\Database;
use MongoDB\InsertOneResult;
use MongoDB\InsertManyResult;
use MongoDB\UpdateResult;
use MongoDB\DeleteResult;
use MongoDB\BulkWriteResult;

/**
 * Dispatches one OpsRequest to the driver. Each op produces a raw JSON
 * fragment (the per-op `data` payload, already relaxed-EJSON-encoded) or an
 * error envelope. The HTTP handler assembles the full response.
 */
final class Ops
{
    public function __construct(private readonly Client $client) {}

    /**
     * Run the ops. Returns an array of pre-encoded JSON strings, one per op.
     * Each string is a complete {"op": "...", "ok": ..., ...} object.
     */
    public function run(string $dbName, array $ops): array
    {
        $db = $this->client->selectDatabase($dbName);
        $out = [];
        foreach ($ops as $op) {
            $out[] = $this->runOne($db, $op);
        }
        return $out;
    }

    private function runOne(Database $db, array $op): string
    {
        $name = $op['name'] ?? '';
        try {
            $data = $this->execute($db, $op);
        } catch (\Throwable $e) {
            $cl = Errors::classify($e);
            $err = ['code' => $cl['code'], 'message' => $cl['message']];
            if (isset($cl['server_code'])) {
                $err['server_code'] = $cl['server_code'];
            }
            return self::buildJson([
                ['op', self::jsonString($name)],
                ['ok', 'false'],
                ['error', self::encodePlainJson($err)],
            ]);
        }
        return self::buildJson([
            ['op', self::jsonString($name)],
            ['ok', 'true'],
            ['data', $data],
        ]);
    }

    /** @return string raw JSON fragment for the success data payload */
    private function execute(Database $db, array $op): string
    {
        $name = $op['name'] ?? '';
        $coll = $op['collection'] ?? null;
        $collection = $coll !== null ? $db->selectCollection($coll) : null;

        return match ($name) {
            'insertOne'      => $this->insertOne($collection, $op),
            'insertMany'     => $this->insertMany($collection, $op),
            'find'           => $this->find($collection, $op),
            'updateOne'      => $this->updateOne($collection, $op),
            'updateMany'     => $this->updateMany($collection, $op),
            'replaceOne'     => $this->replaceOne($collection, $op),
            'deleteOne'      => $this->deleteOne($collection, $op),
            'deleteMany'     => $this->deleteMany($collection, $op),
            'countDocuments' => $this->countDocuments($collection, $op),
            'aggregate'      => $this->aggregate($collection, $op),
            'bulkWrite'      => $this->bulkWrite($collection, $op),
            default          => throw new \InvalidArgumentException('unknown op: ' . $name),
        };
    }

    // ---- per-op implementations ----

    private function insertOne(Collection $c, array $op): string
    {
        $doc = Ejson::decodeDocument($op['__document_raw'] ?? '{}');
        /** @var InsertOneResult $res */
        $res = $c->insertOne($doc);
        $id = $res->getInsertedId();
        return self::buildJson([
            ['inserted_id', Ejson::encodeValue($id)],
        ]);
    }

    private function insertMany(Collection $c, array $op): string
    {
        $rawDocs = $op['__documents_raw'] ?? [];
        $docs = [];
        foreach ($rawDocs as $rd) {
            $docs[] = Ejson::decodeDocument($rd);
        }
        $opts = [];
        if (array_key_exists('ordered', $op)) {
            $opts['ordered'] = (bool) $op['ordered'];
        }
        /** @var InsertManyResult $res */
        $res = $c->insertMany($docs, $opts);
        $ids = $res->getInsertedIds();
        // Preserve numeric index order.
        ksort($ids, SORT_NUMERIC);
        $idJsons = [];
        foreach ($ids as $id) {
            $idJsons[] = Ejson::encodeValue($id);
        }
        return self::buildJson([
            ['inserted_ids', '[' . implode(',', $idJsons) . ']'],
            ['inserted_count', (string) count($idJsons)],
        ]);
    }

    private function find(Collection $c, array $op): string
    {
        $filter = Ejson::decodeDocument($op['__filter_raw'] ?? '{}');
        $opts = [];
        if (isset($op['__projection_raw'])) {
            $opts['projection'] = Ejson::decodeDocument($op['__projection_raw']);
        }
        if (isset($op['__sort_raw'])) {
            $opts['sort'] = Ejson::decodeDocument($op['__sort_raw']);
        }
        if (array_key_exists('skip', $op)) {
            $opts['skip'] = (int) $op['skip'];
        }
        if (array_key_exists('limit', $op)) {
            $opts['limit'] = (int) $op['limit'];
        }
        // Configure cursor to yield BSON Documents so we can emit relaxed EJSON
        // directly without losing types.
        $opts['typeMap'] = [
            'root' => 'bson',
            'document' => 'bson',
            'array' => 'bson',
        ];
        $cursor = $c->find($filter, $opts);
        $docs = [];
        foreach ($cursor as $doc) {
            /** @var Document $doc */
            $docs[] = $doc->toRelaxedExtendedJSON();
        }
        return self::buildJson([
            ['documents', '[' . implode(',', $docs) . ']'],
            ['count', (string) count($docs)],
        ]);
    }

    private function updateOne(Collection $c, array $op): string
    {
        $filter = Ejson::decodeDocument($op['__filter_raw'] ?? '{}');
        $update = $this->decodeUpdate($op['__update_raw'] ?? '{}');
        $opts = $this->updateOptions($op);
        /** @var UpdateResult $res */
        $res = $c->updateOne($filter, $update, $opts);
        return $this->encodeUpdateResult($res);
    }

    private function updateMany(Collection $c, array $op): string
    {
        $filter = Ejson::decodeDocument($op['__filter_raw'] ?? '{}');
        $update = $this->decodeUpdate($op['__update_raw'] ?? '{}');
        $opts = $this->updateOptions($op);
        /** @var UpdateResult $res */
        $res = $c->updateMany($filter, $update, $opts);
        return $this->encodeUpdateResult($res);
    }

    private function replaceOne(Collection $c, array $op): string
    {
        $filter = Ejson::decodeDocument($op['__filter_raw'] ?? '{}');
        $replacement = Ejson::decodeDocument($op['__replacement_raw'] ?? '{}');
        $opts = [];
        if (array_key_exists('upsert', $op)) {
            $opts['upsert'] = (bool) $op['upsert'];
        }
        /** @var UpdateResult $res */
        $res = $c->replaceOne($filter, $replacement, $opts);
        return $this->encodeUpdateResult($res);
    }

    private function deleteOne(Collection $c, array $op): string
    {
        $filter = Ejson::decodeDocument($op['__filter_raw'] ?? '{}');
        /** @var DeleteResult $res */
        $res = $c->deleteOne($filter);
        return self::buildJson([
            ['deleted_count', (string) $res->getDeletedCount()],
        ]);
    }

    private function deleteMany(Collection $c, array $op): string
    {
        $filter = Ejson::decodeDocument($op['__filter_raw'] ?? '{}');
        /** @var DeleteResult $res */
        $res = $c->deleteMany($filter);
        return self::buildJson([
            ['deleted_count', (string) $res->getDeletedCount()],
        ]);
    }

    private function countDocuments(Collection $c, array $op): string
    {
        $filter = Ejson::decodeDocument($op['__filter_raw'] ?? '{}');
        $n = $c->countDocuments($filter);
        return self::buildJson([
            ['count', (string) $n],
        ]);
    }

    private function aggregate(Collection $c, array $op): string
    {
        $stagesRaw = $op['__pipeline_raw'] ?? [];
        $pipeline = [];
        foreach ($stagesRaw as $sr) {
            $pipeline[] = Ejson::decodeDocument($sr);
        }
        $opts = [
            'typeMap' => [
                'root' => 'bson',
                'document' => 'bson',
                'array' => 'bson',
            ],
        ];
        $cursor = $c->aggregate($pipeline, $opts);
        $docs = [];
        foreach ($cursor as $doc) {
            /** @var Document $doc */
            $docs[] = $doc->toRelaxedExtendedJSON();
        }
        return self::buildJson([
            ['documents', '[' . implode(',', $docs) . ']'],
            ['count', (string) count($docs)],
        ]);
    }

    private function bulkWrite(Collection $c, array $op): string
    {
        $subs = $op['__operations'] ?? [];
        $models = [];
        $insertedIds = []; // index → BSON id value

        foreach ($subs as $i => $sub) {
            $name = $sub['name'] ?? '';
            switch ($name) {
                case 'insertOne':
                    $doc = Ejson::decodeDocument($sub['__document_raw'] ?? '{}');
                    [$doc, $id] = Ejson::ensureId($doc);
                    $insertedIds[$i] = $id;
                    $models[] = ['insertOne' => [$doc]];
                    break;
                case 'updateOne':
                    $filter = Ejson::decodeDocument($sub['__filter_raw'] ?? '{}');
                    $update = $this->decodeUpdate($sub['__update_raw'] ?? '{}');
                    $opts = $this->updateOptions($sub);
                    $models[] = ['updateOne' => [$filter, $update, $opts]];
                    break;
                case 'updateMany':
                    $filter = Ejson::decodeDocument($sub['__filter_raw'] ?? '{}');
                    $update = $this->decodeUpdate($sub['__update_raw'] ?? '{}');
                    $opts = $this->updateOptions($sub);
                    $models[] = ['updateMany' => [$filter, $update, $opts]];
                    break;
                case 'replaceOne':
                    $filter = Ejson::decodeDocument($sub['__filter_raw'] ?? '{}');
                    $replacement = Ejson::decodeDocument($sub['__replacement_raw'] ?? '{}');
                    $opts = [];
                    if (array_key_exists('upsert', $sub)) {
                        $opts['upsert'] = (bool) $sub['upsert'];
                    }
                    $models[] = ['replaceOne' => [$filter, $replacement, $opts]];
                    break;
                case 'deleteOne':
                    $filter = Ejson::decodeDocument($sub['__filter_raw'] ?? '{}');
                    $models[] = ['deleteOne' => [$filter]];
                    break;
                case 'deleteMany':
                    $filter = Ejson::decodeDocument($sub['__filter_raw'] ?? '{}');
                    $models[] = ['deleteMany' => [$filter]];
                    break;
                default:
                    throw new \InvalidArgumentException('bulkWrite sub-op unsupported: ' . $name);
            }
        }
        $opts = [];
        if (array_key_exists('ordered', $op)) {
            $opts['ordered'] = (bool) $op['ordered'];
        }
        /** @var BulkWriteResult $res */
        $res = $c->bulkWrite($models, $opts);

        // Merge driver-returned insertedIds (typically integer keys) with our
        // pre-assigned ones. Our pre-assigned take precedence when the doc
        // lacked an _id; the driver's value wins when the doc supplied one.
        $driverInserted = $res->getInsertedIds();
        foreach ($driverInserted as $idx => $val) {
            if (!array_key_exists($idx, $insertedIds)) {
                $insertedIds[$idx] = $val;
            }
            // If we pre-assigned, our value matches what was actually inserted
            // (we wrote it into the doc), so prefer ours.
        }

        $insertedEntries = [];
        ksort($insertedIds, SORT_NUMERIC);
        foreach ($insertedIds as $idx => $id) {
            $insertedEntries[] = self::jsonString((string) $idx) . ':' . Ejson::encodeValue($id);
        }

        $upsertedIds = $res->getUpsertedIds();
        $upsertedEntries = [];
        if (!empty($upsertedIds)) {
            ksort($upsertedIds, SORT_NUMERIC);
            foreach ($upsertedIds as $idx => $id) {
                $upsertedEntries[] = self::jsonString((string) $idx) . ':' . Ejson::encodeValue($id);
            }
        }

        return self::buildJson([
            ['inserted_count', (string) $res->getInsertedCount()],
            ['matched_count', (string) $res->getMatchedCount()],
            ['modified_count', (string) $res->getModifiedCount()],
            ['deleted_count', (string) $res->getDeletedCount()],
            ['upserted_count', (string) $res->getUpsertedCount()],
            ['inserted_ids', '{' . implode(',', $insertedEntries) . '}'],
            ['upserted_ids', '{' . implode(',', $upsertedEntries) . '}'],
        ]);
    }

    // ---- helpers ----

    private function decodeUpdate(string $raw): mixed
    {
        // Update may be a doc or a pipeline (array of stages).
        $trim = ltrim($raw);
        if ($trim === '' || $trim === 'null') {
            return [];
        }
        if ($trim[0] === '[') {
            $stages = json_decode($raw, true);
            if (!is_array($stages)) {
                throw new \InvalidArgumentException('update pipeline must decode to array');
            }
            $out = [];
            foreach ($stages as $stage) {
                $out[] = Ejson::decodeDocument(json_encode($stage));
            }
            return $out;
        }
        return Ejson::decodeDocument($raw);
    }

    private function updateOptions(array $op): array
    {
        $opts = [];
        if (array_key_exists('upsert', $op)) {
            $opts['upsert'] = (bool) $op['upsert'];
        }
        if (isset($op['__array_filters_raw'])) {
            $afs = [];
            foreach ($op['__array_filters_raw'] as $afRaw) {
                $afs[] = Ejson::decodeDocument($afRaw);
            }
            $opts['arrayFilters'] = $afs;
        }
        return $opts;
    }

    private function encodeUpdateResult(UpdateResult $res): string
    {
        $entries = [
            ['matched_count', (string) $res->getMatchedCount()],
            ['modified_count', (string) $res->getModifiedCount()],
        ];
        $upsertedId = $res->getUpsertedId();
        if ($res->getUpsertedCount() > 0 && $upsertedId !== null) {
            $entries[] = ['upserted_id', Ejson::encodeValue($upsertedId)];
        }
        return self::buildJson($entries);
    }

    /** Build a JSON object string from ordered [key, valueJsonFragment] pairs. */
    public static function buildJson(array $entries): string
    {
        $parts = [];
        foreach ($entries as [$k, $v]) {
            $parts[] = self::jsonString($k) . ':' . $v;
        }
        return '{' . implode(',', $parts) . '}';
    }

    public static function jsonString(string $s): string
    {
        $enc = json_encode($s, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES);
        if ($enc === false) {
            throw new \RuntimeException('jsonString: encode failed');
        }
        return $enc;
    }

    /** Encode a plain PHP value (no driver BSON types) to JSON. */
    public static function encodePlainJson(mixed $value): string
    {
        $enc = json_encode($value, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES);
        if ($enc === false) {
            throw new \RuntimeException('encodePlainJson failed');
        }
        return $enc;
    }
}
