<?php
declare(strict_types=1);

namespace MongoDriversBenchmark;

use MongoDB\Driver\Exception as DriverException;

/**
 * Maps driver exceptions to the spec's normalized ErrorCode values.
 * See spec/http-api.md §7.
 */
final class Errors
{
    public const CODE_DUPLICATE_KEY    = 'DUPLICATE_KEY';
    public const CODE_WRITE_CONFLICT   = 'WRITE_CONFLICT';
    public const CODE_TIMEOUT          = 'TIMEOUT';
    public const CODE_NETWORK          = 'NETWORK';
    public const CODE_AUTH             = 'AUTH';
    public const CODE_NOT_FOUND        = 'NOT_FOUND';
    public const CODE_INVALID_ARGUMENT = 'INVALID_ARGUMENT';
    public const CODE_INTERNAL         = 'INTERNAL';

    /**
     * @return array{code: string, message: string, server_code?: int}
     */
    public static function classify(\Throwable $e): array
    {
        $message = $e->getMessage();
        $result = [
            'code' => self::CODE_INTERNAL,
            'message' => $message,
        ];

        // 1. Try to extract a server code from BulkWriteException (covers both
        //    InsertMany and BulkWrite — including duplicate-key on inserts).
        if ($e instanceof DriverException\BulkWriteException) {
            $writeResult = $e->getWriteResult();
            if ($writeResult !== null) {
                foreach ($writeResult->getWriteErrors() as $we) {
                    $serverCode = (int) $we->getCode();
                    return [
                        'code' => self::codeFromServer($serverCode),
                        'message' => $message,
                        'server_code' => $serverCode,
                    ];
                }
                $wce = $writeResult->getWriteConcernError();
                if ($wce !== null) {
                    $serverCode = (int) $wce->getCode();
                    return [
                        'code' => self::codeFromServer($serverCode),
                        'message' => $message,
                        'server_code' => $serverCode,
                    ];
                }
            }
            // Fall through to generic handling.
        }

        // 2. WriteException (single-op writes that fail server-side).
        if ($e instanceof DriverException\WriteException) {
            $writeResult = $e->getWriteResult();
            if ($writeResult !== null) {
                foreach ($writeResult->getWriteErrors() as $we) {
                    $serverCode = (int) $we->getCode();
                    return [
                        'code' => self::codeFromServer($serverCode),
                        'message' => $message,
                        'server_code' => $serverCode,
                    ];
                }
                $wce = $writeResult->getWriteConcernError();
                if ($wce !== null) {
                    $serverCode = (int) $wce->getCode();
                    return [
                        'code' => self::codeFromServer($serverCode),
                        'message' => $message,
                        'server_code' => $serverCode,
                    ];
                }
            }
        }

        // 3. CommandException carries a server code on getCode().
        if ($e instanceof DriverException\CommandException) {
            $serverCode = (int) $e->getCode();
            if ($serverCode > 0) {
                return [
                    'code' => self::codeFromServer($serverCode),
                    'message' => $message,
                    'server_code' => $serverCode,
                ];
            }
        }

        // 4. Authentication / connection / timeout exception classes.
        if ($e instanceof DriverException\AuthenticationException) {
            return ['code' => self::CODE_AUTH, 'message' => $message];
        }
        if ($e instanceof DriverException\ConnectionTimeoutException
            || $e instanceof DriverException\ExecutionTimeoutException
        ) {
            return ['code' => self::CODE_TIMEOUT, 'message' => $message];
        }
        if ($e instanceof DriverException\ConnectionException) {
            return ['code' => self::CODE_NETWORK, 'message' => $message];
        }

        // 5. Userland library InvalidArgumentException (bad BSON / args).
        if ($e instanceof \MongoDB\Exception\InvalidArgumentException
            || $e instanceof DriverException\InvalidArgumentException
            || $e instanceof \InvalidArgumentException
        ) {
            return ['code' => self::CODE_INVALID_ARGUMENT, 'message' => $message];
        }

        // 6. Generic ServerException — getCode() may have a server code.
        if ($e instanceof DriverException\ServerException) {
            $serverCode = (int) $e->getCode();
            if ($serverCode > 0) {
                return [
                    'code' => self::codeFromServer($serverCode),
                    'message' => $message,
                    'server_code' => $serverCode,
                ];
            }
        }

        return $result;
    }

    /** Map a MongoDB server error code to a normalized ErrorCode. */
    public static function codeFromServer(int $code): string
    {
        return match ($code) {
            11000, 11001, 12582 => self::CODE_DUPLICATE_KEY,
            112 => self::CODE_WRITE_CONFLICT,
            50, 89, 262 => self::CODE_TIMEOUT,
            13, 18, 8000 => self::CODE_AUTH,
            26, 27 => self::CODE_NOT_FOUND,
            2, 9, 14, 40, 51, 72, 73, 4, 16, 17, 30, 31, 52, 66 => self::CODE_INVALID_ARGUMENT,
            default => self::CODE_INTERNAL,
        };
    }
}
