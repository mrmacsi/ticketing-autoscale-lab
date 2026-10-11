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
     * @return array{0: Reservation, 1: bool} reservation and whether it was replayed
     */
    public function reserve(string $categoryId, int $quantity, string $customerRef, string $idempotencyKey): array
    {
        $fingerprint = hash('sha256', json_encode([$categoryId, $quantity, $customerRef]));

        if ($existing = Reservation::where('idempotency_key', $idempotencyKey)->first()) {
            return [$this->assertSameRequest($existing, $fingerprint), true];
        }

        try {
            $reservation = DB::transaction(function () use ($categoryId, $quantity, $customerRef, $idempotencyKey, $fingerprint) {
                $category = $this->ledger->hold($categoryId, $quantity);
                if ($category === null) {
                    if (! TicketCategory::whereKey($categoryId)->exists()) {
                        throw (new ModelNotFoundException)->setModel(TicketCategory::class, [$categoryId]);
                    }
                    throw BusinessRuleException::soldOut();
                }

                $id = (string) Str::uuid7();
                DB::insert(
                    "INSERT INTO reservations
                        (id, event_id, category_id, customer_ref, quantity, status, idempotency_key,
                         request_fingerprint, expires_at, created_at, updated_at)
                     VALUES (?, ?, ?, ?, ?, 'active', ?, ?, now() + make_interval(secs => ?), now(), now())",
                    [$id, $category->event_id, $categoryId, $customerRef, $quantity, $idempotencyKey,
                        $fingerprint, (int) $this->settings->get('reservation_ttl_seconds')],
                );

                $this->faults->trip(FaultInjector::BEFORE_RESERVATION_COMMIT);

                return Reservation::findOrFail($id);
            });
        } catch (UniqueConstraintViolationException) {
            // A concurrent request with the same key committed first; its transaction already
            // holds the inventory and ours was rolled back.
            $existing = Reservation::where('idempotency_key', $idempotencyKey)->firstOrFail();

            return [$this->assertSameRequest($existing, $fingerprint), true];
        }

        return [$reservation, false];
    }

    private function assertSameRequest(Reservation $reservation, string $fingerprint): Reservation
    {
        if (! hash_equals($reservation->request_fingerprint, $fingerprint)) {
            throw BusinessRuleException::idempotencyKeyReused();
        }

        return $reservation;
    }
}
