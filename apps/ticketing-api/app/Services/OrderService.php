<?php

namespace App\Services;

use App\Exceptions\BusinessRuleException;
use App\Models\Order;
use App\Models\Reservation;
use Illuminate\Database\UniqueConstraintViolationException;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Str;

class OrderService
{
    public function __construct(private readonly ReservationExpiry $expiry) {}

    /**
     * Create the (single) order for an active reservation. Duplicate submissions — same key or a
     * different key for the same reservation — return the existing order.
     *
     * @return array{0: Order, 1: bool} order and whether it already existed
     */
    public function create(string $reservationId, string $customerRef, string $idempotencyKey): array
    {
        if ($existing = Order::where('idempotency_key', $idempotencyKey)->first()) {
            if ($existing->reservation_id !== $reservationId) {
                throw BusinessRuleException::idempotencyKeyReused();
            }

            return [$existing, true];
        }

        try {
            [$outcome, $order] = DB::transaction(function () use ($reservationId, $customerRef, $idempotencyKey) {
                $reservation = $this->lockReservation($reservationId);

                if ($reservation->customer_ref !== $customerRef) {
                    throw new BusinessRuleException('reservation_owner_mismatch', 403, 'The reservation belongs to another customer.');
                }
                if ($order = Order::where('reservation_id', $reservationId)->first()) {
                    return ['existing', $order];
                }
                if ($reservation->status === Reservation::ACTIVE && $reservation->past_expiry) {
                    $this->expiry->expireLocked($reservation);

                    return ['expired', null];
                }
                if ($reservation->status === Reservation::EXPIRED) {
                    return ['expired', null];
                }
                if ($reservation->status !== Reservation::ACTIVE) {
                    throw BusinessRuleException::reservationNotActive($reservation->status);
                }

                $price = DB::selectOne('SELECT price_cents FROM ticket_categories WHERE id = ?', [$reservation->category_id])->price_cents;

                return ['created', Order::create([
                    'id' => (string) Str::uuid7(),
                    'reservation_id' => $reservation->id,
                    'event_id' => $reservation->event_id,
                    'customer_ref' => $customerRef,
                    'status' => Order::PENDING,
                    'amount_cents' => $price * $reservation->quantity,
                    'idempotency_key' => $idempotencyKey,
                ])];
            });
        } catch (UniqueConstraintViolationException) {
            $order = Order::where('idempotency_key', $idempotencyKey)->first()
                ?? Order::where('reservation_id', $reservationId)->firstOrFail();

            return [$order, true];
        }

        // Thrown after commit so the expiry (and the released tickets) is persisted.
        if ($outcome === 'expired') {
            throw BusinessRuleException::reservationExpired();
        }

        return [$order, $outcome === 'existing'];
    }

    private function lockReservation(string $id): Reservation
    {
        return Reservation::query()
            ->select('reservations.*')
            ->selectRaw('(expires_at <= now()) AS past_expiry')
            ->whereKey($id)
            ->lockForUpdate()
            ->firstOrFail();
    }
}
