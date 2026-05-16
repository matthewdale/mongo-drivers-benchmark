<?php
declare(strict_types=1);

require __DIR__ . '/../vendor/autoload.php';

use MongoDB\Client;
use MongoDB\Driver\ReadPreference;
use MongoDriversBenchmark\Errors;
use MongoDriversBenchmark\Ops;
use MongoDriversBenchmark\Validator;
use MongoDriversBenchmark\ValidationError;

const SPEC_VERSION = '1.0.0';

/**
 * Driver / library version — emitted in /v1/info.
 */
function detectDriverVersion(): string
{
    // ext-mongodb version is the on-the-wire driver.
    $ext = phpversion('mongodb');
    return $ext !== false ? (string) $ext : 'unknown';
}

function jsonHeader(int $status): void
{
    http_response_code($status);
    header('Content-Type: application/json; charset=utf-8');
}

function sendJson(int $status, string $body): void
{
    jsonHeader($status);
    echo $body;
}

function sendRequestError(int $status, string $code, string $message): void
{
    $body = json_encode([
        'error' => ['code' => $code, 'message' => $message],
    ], JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE);
    sendJson($status, $body !== false ? $body : '{"error":{"code":"INTERNAL","message":"json_encode failure"}}');
}

function readBody(): string
{
    $body = file_get_contents('php://input');
    return $body === false ? '' : $body;
}

/** @return Client */
function client(): Client
{
    static $client = null;
    if ($client !== null) {
        return $client;
    }
    $uri = getenv('MONGODB_URI');
    if ($uri === false || $uri === '') {
        throw new RuntimeException('MONGODB_URI is required');
    }
    $client = new Client($uri, [], [
        'typeMap' => [
            'root' => 'array',
            'document' => 'array',
            'array' => 'array',
        ],
    ]);
    return $client;
}

// ---- Routing ----

$method = $_SERVER['REQUEST_METHOD'] ?? 'GET';
$path = parse_url($_SERVER['REQUEST_URI'] ?? '/', PHP_URL_PATH) ?? '/';

try {
    switch ($method . ' ' . $path) {
        case 'POST /v1/ops':
            handleOps();
            break;
        case 'POST /v1/admin/reset':
            handleReset();
            break;
        case 'GET /v1/info':
            handleInfo();
            break;
        case 'GET /v1/health':
            handleHealth();
            break;
        default:
            sendRequestError(404, 'BAD_REQUEST', 'not found: ' . $method . ' ' . $path);
    }
} catch (\Throwable $e) {
    // Last-ditch safety net: anything escaping the handler becomes a 500.
    sendRequestError(500, 'INTERNAL', $e->getMessage());
}

function handleOps(): void
{
    $body = readBody();
    try {
        $decoded = Validator::decode($body);
    } catch (ValidationError $ve) {
        sendRequestError(400, $ve->errorCode, $ve->getMessage());
        return;
    }

    $ops = new Ops(client());
    $resultStrings = $ops->run($decoded['database'], $decoded['ops']);
    $body = '{"results":[' . implode(',', $resultStrings) . ']}';
    sendJson(200, $body);
}

function handleReset(): void
{
    $body = readBody();
    $decoded = json_decode($body, true);
    if (!is_array($decoded)) {
        sendRequestError(400, 'SCHEMA_VIOLATION', 'invalid JSON');
        return;
    }
    if (!array_key_exists('databases', $decoded)) {
        sendRequestError(400, 'MISSING_FIELD', 'missing required field "databases"');
        return;
    }
    $dbs = $decoded['databases'];
    if (!is_array($dbs) || count($dbs) === 0) {
        sendRequestError(400, 'EMPTY_OPS', 'databases must be a non-empty array');
        return;
    }
    foreach ($dbs as $name) {
        if (!is_string($name) || $name === '') {
            sendRequestError(400, 'SCHEMA_VIOLATION', 'database name must be a non-empty string');
            return;
        }
        if (in_array($name, ['admin', 'local', 'config'], true)) {
            sendRequestError(400, 'BAD_REQUEST', 'refusing to drop "' . $name . '"');
            return;
        }
    }

    $dropped = [];
    $client = client();
    foreach ($dbs as $name) {
        try {
            $client->dropDatabase($name);
            $dropped[] = $name;
        } catch (\Throwable $e) {
            sendRequestError(500, 'INTERNAL', 'drop ' . $name . ': ' . $e->getMessage());
            return;
        }
    }
    $out = json_encode(['dropped' => $dropped], JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE);
    sendJson(200, $out !== false ? $out : '{"dropped":[]}');
}

function handleInfo(): void
{
    $info = [
        'driver' => 'mongodb-php-driver',
        'driver_version' => detectDriverVersion(),
        'language' => 'php',
        'language_version' => PHP_VERSION,
        'spec_version' => SPEC_VERSION,
    ];
    $out = json_encode($info, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE);
    sendJson(200, $out !== false ? $out : '{}');
}

function handleHealth(): void
{
    try {
        $client = client();
        // Run a ping against admin.
        $client->getManager()->executeCommand('admin', new \MongoDB\Driver\Command(['ping' => 1]), [
            'readPreference' => new ReadPreference(ReadPreference::PRIMARY),
        ]);
    } catch (\Throwable $e) {
        $out = json_encode(['ok' => false, 'detail' => $e->getMessage()], JSON_UNESCAPED_SLASHES);
        sendJson(503, $out !== false ? $out : '{"ok":false}');
        return;
    }
    sendJson(200, '{"ok":true}');
}
