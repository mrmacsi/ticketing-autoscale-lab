<?php

namespace App\Services;

use App\Models\Reservation;
use Illuminate\Support\Facades\DB;

class ReservationExpiry
{
    /** Serialises sweepers across all instances so their multi-category updates cannot deadlock. */
    private const SWEEPER_LOCK_KEY = 74_210_001;

    public function __construct(private readonly InventoryLedger $ledger) {}

    /** Expire a reservation the caller has locked FOR UPDATE and that still holds inventory. */
    public function expireLocked(Reservation $reservation): void
    {
        $this->ledger->release($reservation->category_id, $reservation->quantity);
        DB::update(
            "UPDATE reservations SET status = 'expired', expired_at = now(), updated_at = now() WHERE id = ?",
            [$reservation->id],
        );
        $reservation->status = Reservation::EXPIRED;
    }

    /**
     * Expire due reservations in one transaction. Active reservations expire at expires_at;
     * payment_processing ones only after the payment grace period, which covers a process that
     * died between the two payment transactions.
     *
     * @return array{expired: int, skipped: bool}
     */
    public function sweep(int $batchSize = 500): array
    {
        return DB::transaction(function () use ($batchSize) {
            $locked = DB::selectOne('SELECT pg_try_advisory_xact_lock(?) AS ok', [self::SWEEPER_LOCK_KEY])->ok;
            if (! $locked) {
                return ['expired' => 0, 'skipped' => true];
            }

            $rows = DB::select(
                "WITH due AS (
                    SELECT id FROM reservations
                     WHERE (status = 'active' AND expires_at <= now())
                        OR (status = 'payment_processing' AND expires_at <= now() - make_interval(secs => ?))
                     ORDER BY expires_at
                     LIMIT ?
                     FOR UPDATE SKIP LOCKED
                 ), expired AS (
                    UPDATE reservations r
                       SET status = 'expired', expired_at = now(), updated_at = now()
                      FROM due
                     WHERE r.id = due.id
                 RETURNING r.category_id, r.quantity
                 ), released AS (
                    SELECT category_id, sum(quantity)::int AS qty, count(*)::int AS reservations
                      FROM expired GROUP BY category_id
                 )
                 UPDATE ticket_categories c
                    SET held = c.held - released.qty, updated_at = now()
                   FROM released
                  WHERE c.id = released.category_id
              RETURNING released.reservations",
                [config('lab.payment_grace_seconds'), $batchSize],
            );

            return ['expired' => array_sum(array_map(fn ($r) => $r->reservations, $rows)), 'skipped' => false];
        });
    }
}
