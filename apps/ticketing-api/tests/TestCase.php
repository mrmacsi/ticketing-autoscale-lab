<?php

namespace Tests;

use Illuminate\Foundation\Testing\TestCase as BaseTestCase;
use Illuminate\Support\Facades\Artisan;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\DB;

/**
 * Runs against a real PostgreSQL server (see README: `docker compose up -d postgres-test`).
 * Data is committed, not wrapped in a transaction, so concurrency tests can use separate processes.
 */
abstract class TestCase extends BaseTestCase
{
    private static bool $migrated = false;

    protected function setUp(): void
    {
        parent::setUp();

        if (! self::$migrated) {
            Artisan::call('migrate:fresh', ['--force' => true]);
            self::$migrated = true;
        }
        DB::statement('TRUNCATE tickets, payment_attempts, orders, reservations, ticket_categories, events, lab_settings CASCADE');
        Cache::flush();
    }
}
