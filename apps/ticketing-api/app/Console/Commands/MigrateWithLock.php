<?php

namespace App\Console\Commands;

use Illuminate\Console\Command;
use Illuminate\Support\Facades\DB;

/**
 * Every new VM runs this at boot. A session-level advisory lock serialises instances that start
 * together, so exactly one applies pending migrations and the others find nothing to do.
 */
class MigrateWithLock extends Command
{
    private const LOCK_KEY = 74_210_000;

    protected $signature = 'lab:migrate {--timeout=120 : Seconds to wait for the database}';

    protected $description = 'Run migrations once across all instances';

    public function handle(): int
    {
        $deadline = time() + (int) $this->option('timeout');
        while (true) {
            try {
                DB::select('SELECT 1');
                break;
            } catch (\Throwable $e) {
                if (time() >= $deadline) {
                    $this->error('Database unreachable: '.$e->getMessage());

                    return self::FAILURE;
                }
                $this->warn('Waiting for database: '.$e->getMessage());
                sleep(3);
            }
        }

        DB::select('SELECT pg_advisory_lock(?)', [self::LOCK_KEY]);
        try {
            $code = $this->call('migrate', ['--force' => true]);
        } finally {
            DB::select('SELECT pg_advisory_unlock(?)', [self::LOCK_KEY]);
        }

        return $code;
    }
}
