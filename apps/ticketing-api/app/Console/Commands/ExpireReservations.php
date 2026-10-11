<?php

namespace App\Console\Commands;

use App\Services\ReservationExpiry;
use Illuminate\Console\Command;
use Illuminate\Support\Facades\Log;
use Throwable;

/**
 * Background expiry. Runs on every application instance (supervisord); a PostgreSQL advisory lock
 * makes sure only one sweep runs at a time across the fleet.
 */
class ExpireReservations extends Command
{
    protected $signature = 'reservations:expire {--loop : Keep sweeping} {--interval=2 : Seconds between sweeps}';

    protected $description = 'Expire reservations past their hold time and release held inventory';

    private bool $running = true;

    public function handle(ReservationExpiry $expiry): int
    {
        if (! $this->option('loop')) {
            $result = $expiry->sweep();
            $this->info("Expired {$result['expired']} reservation(s)".($result['skipped'] ? ' (another sweeper held the lock)' : ''));

            return self::SUCCESS;
        }

        pcntl_async_signals(true);
        pcntl_signal(SIGTERM, fn () => $this->running = false);
        pcntl_signal(SIGINT, fn () => $this->running = false);

        $interval = max(1, (int) $this->option('interval'));
        while ($this->running) {
            try {
                do {
                    $result = $expiry->sweep();
                    if ($result['expired'] > 0) {
                        Log::info('reservations expired', ['count' => $result['expired']]);
                    }
                } while ($this->running && $result['expired'] >= 500);
            } catch (Throwable $e) {
                Log::warning('reservation sweep failed', ['error' => $e->getMessage()]);
            }
            sleep($interval);
        }

        return self::SUCCESS;
    }
}
