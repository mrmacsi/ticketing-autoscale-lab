<?php

namespace App\Services;

use App\Exceptions\BusinessRuleException;
use App\Models\Reservation;
use App\Models\TicketCategory;
use Illuminate\Database\Eloquent\ModelNotFoundException;
use Illuminate\Database\UniqueConstraintViolationException;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Str;

class ReservationService
{
    private const QUEUE_ID_INDEX = 'reservations_queue_id_unique';

    public function __construct(
        private readonly InventoryLedger $ledger,
        private readonly LabSettings $settings,
        private readonly FaultInjector $faults,
    ) {}

    /**
     * Hold tickets for a customer. Idempotent per Idempotency-Key: a successful reservation is
     * returned again for the same key; a sold-out answer is not stored, so a retry may succeed
     * once inventory has been released.
     *
     * $queueId is the waiting-room admission pass (token `q`) while the waiting room is in force. It is
     * stored on the reservation in the same transaction as the hold; the unique index on queue_id
     * lets one pass produce at most one reservation, even for concurrent requests. A rejected or
     * sold-out attempt does not use the pass up (its transaction is rolled back).
     *
     * @return array{0: Reservation, 1: bool} reservation and whether it was replayed
     *
     * @throws BusinessRuleException sold_out, idempotency_key_reused, queue_token_used
     */
    public function reserve(string $categoryId, int $quantity, string $customerRef, string $idempotencyKey, ?string $queueId = null): array
    {
        $fingerprint = hash('sha256', json_encode([$categoryId, $quantity, $customerRef]));

        if ($existing = Reservation::where('idempotency_key', $idempotencyKey)->first()) {
            return [$this->assertSameRequest($existing, $fingerprint), true];
        }

        try {
            $reservation = DB::transaction(function () use ($categoryId, $quantity, $customerRef, $idempotencyKey, $fingerprint, $queueId) {
                $category = $this->ledger->hold($categoryId, $quantity);
                if ($category === null) {
                    if (! TicketCategory::whereKey($categoryId)->exists()) {
                        throw (new ModelNotFoundException)->setModel(TicketCategory::class, [$categoryId]);
                    }
                    throw BusinessRuleException::soldOut();
                }

                $row = DB::selectOne(
                    "INSERT INTO reservations
                        (id, event_id, category_id, customer_ref, quantity, status, idempotency_key,
                         request_fingerprint, queue_id, expires_at, created_at, updated_at)
                     VALUES (?, ?, ?, ?, ?, 'active', ?, ?, ?, now() + make_interval(secs => ?), now(), now())
                     RETURNING *",
                    [(string) Str::uuid7(), $category->event_id, $categoryId, $customerRef, $quantity, $idempotencyKey,
                        $fingerprint, $queueId, $this->holdSeconds((int) $category->available)],
                );

                $this->faults->trip(FaultInjector::BEFORE_RESERVATION_COMMIT);

                return (new Reservation)->newFromBuilder($row);
            });
        } catch (UniqueConstraintViolationException $e) {
            // Our transaction (and with it the inventory hold) was rolled back. Either a concurrent
            // request with the same Idempotency-Key committed first (replay it), or this admission
            // pass already produced another reservation.
            if ($existing = Reservation::where('idempotency_key', $idempotencyKey)->first()) {
                return [$this->assertSameRequest($existing, $fingerprint), true];
            }
            if ($queueId !== null && str_contains($e->getMessage(), self::QUEUE_ID_INDEX)) {
                throw BusinessRuleException::queueTokenUsed();
            }
            throw $e;
        }

        return [$reservation, false];
    }

    /**
     * Hold duration for a new reservation. Near sell-out (at most `near_sellout_threshold` tickets of
     * the category left after this hold) a shorter TTL, when configured, recycles abandoned holds
     * faster so the last tickets reach the customers still trying to buy them.
     */
    private function holdSeconds(int $availableAfterHold): int
    {
        $settings = $this->settings->all();
        $ttl = (int) $settings['reservation_ttl_seconds'];
        $short = (int) $settings['reservation_ttl_near_sellout_seconds'];

        if ($short > 0 && $availableAfterHold <= (int) $settings['near_sellout_threshold']) {
            return min($short, $ttl);
        }

        return $ttl;
    }

    private function assertSameRequest(Reservation $reservation, string $fingerprint): Reservation
    {
        if (! hash_equals($reservation->request_fingerprint, $fingerprint)) {
            throw BusinessRuleException::idempotencyKeyReused();
        }

        return $reservation;
    }
}
