<?php

namespace App\Services;

use App\Exceptions\BusinessRuleException;
use App\Models\Order;
use App\Models\PaymentAttempt;
use App\Models\Reservation;
use App\Models\Ticket;
use Illuminate\Database\UniqueConstraintViolationException;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Str;
use Throwable;

/**
 * Two short transactions around the (slow) simulated provider call, so no row lock is held while
 * waiting:
 *   1. active → payment_processing, record the attempt;
 *   2. approved: held → sold, reservation consumed, order paid, tickets issued;
 *      declined: back to active (or expired if the hold ran out meanwhile).
 * Lock order everywhere: order → reservation → attempt → category.
 */
class PaymentService
{
    public function __construct(
        private readonly InventoryLedger $ledger,
        private readonly ReservationExpiry $expiry,
        private readonly SimulatedPaymentProvider $provider,
        private readonly FaultInjector $faults,
    ) {}

    public function pay(string $orderId, string $idempotencyKey, ?string $forcedOutcome = null, ?int $forcedDelayMs = null): Order
    {
        if ($previous = PaymentAttempt::where('idempotency_key', $idempotencyKey)->first()) {
            return $this->replay($previous, $orderId);
        }

        $delayMs = $this->provider->chooseDelayMs($forcedDelayMs);

        try {
            [$state, $attemptId] = DB::transaction(fn () => $this->begin($orderId, $idempotencyKey, $delayMs));
        } catch (UniqueConstraintViolationException) {
            // Same key submitted concurrently: the other request owns the attempt.
            throw BusinessRuleException::paymentInProgress();
        }

        if ($state === 'already_paid') {
            return $this->freshOrder($orderId);
        }
        if ($state === 'expired') {
            throw BusinessRuleException::reservationExpired();
        }

        $this->faults->trip(FaultInjector::AFTER_PAYMENT_START);
        $approved = $this->provider->charge($delayMs, $forcedOutcome);

        try {
            $result = DB::transaction(fn () => $this->complete($orderId, $attemptId, $approved));
        } catch (Throwable $e) {
            // The completing transaction rolled back (nothing was sold). Undo the first step so the
            // customer can retry; if the process had died instead, the sweeper would release the
            // hold after the grace period.
            $this->abandonAttempt($orderId, $attemptId);
            throw $e;
        }

        return match ($result) {
            'paid' => $this->freshOrder($orderId),
            'declined' => throw BusinessRuleException::paymentDeclined($this->freshOrder($orderId)->toApi()),
            'expired' => throw BusinessRuleException::reservationExpired(),
        };
    }

    /** @return array{0: string, 1: ?string} */
    private function begin(string $orderId, string $key, int $delayMs): array
    {
        $order = Order::whereKey($orderId)->lockForUpdate()->firstOrFail();
        if ($order->status === Order::PAID) {
            return ['already_paid', null];
        }

        $reservation = $this->lockReservation($order->reservation_id);
        if ($reservation->status === Reservation::PAYMENT_PROCESSING) {
            throw BusinessRuleException::paymentInProgress();
        }
        if ($reservation->status === Reservation::EXPIRED) {
            return ['expired', null];
        }
        if ($reservation->status !== Reservation::ACTIVE) {
            throw BusinessRuleException::reservationNotActive($reservation->status);
        }
        if ($reservation->past_expiry) {
            $this->expiry->expireLocked($reservation);
            $order->update(['status' => Order::CANCELLED]);

            return ['expired', null];
        }

        DB::update("UPDATE reservations SET status = 'payment_processing', updated_at = now() WHERE id = ?", [$reservation->id]);
        $attempt = PaymentAttempt::create([
            'id' => (string) Str::uuid7(),
            'order_id' => $order->id,
            'idempotency_key' => $key,
            'status' => PaymentAttempt::PROCESSING,
            'delay_ms' => $delayMs,
        ]);
        $order->increment('payment_attempts');

        return ['started', $attempt->id];
    }

    private function complete(string $orderId, string $attemptId, bool $approved): string
    {
        $order = Order::whereKey($orderId)->lockForUpdate()->firstOrFail();
        $reservation = $this->lockReservation($order->reservation_id);
        $attempt = PaymentAttempt::whereKey($attemptId)->lockForUpdate()->firstOrFail();

        // The sweeper expired the reservation because this attempt outlived the grace period.
        if ($reservation->status !== Reservation::PAYMENT_PROCESSING) {
            $attempt->update(['status' => PaymentAttempt::VOID, 'failure_reason' => 'reservation_expired', 'completed_at' => now()]);
            $order->update(['status' => Order::CANCELLED]);

            return 'expired';
        }

        if (! $approved) {
            $attempt->update(['status' => PaymentAttempt::DECLINED, 'failure_reason' => 'simulated_decline', 'completed_at' => now()]);
            if ($reservation->past_expiry) {
                $this->expiry->expireLocked($reservation);
                $order->update(['status' => Order::CANCELLED]);
            } else {
                DB::update("UPDATE reservations SET status = 'active', updated_at = now() WHERE id = ?", [$reservation->id]);
                $order->update(['status' => Order::PAYMENT_FAILED]);
            }

            return 'declined';
        }

        $this->ledger->convertHeldToSold($reservation->category_id, $reservation->quantity);
        DB::update("UPDATE reservations SET status = 'consumed', consumed_at = now(), updated_at = now() WHERE id = ?", [$reservation->id]);
        $attempt->update(['status' => PaymentAttempt::APPROVED, 'completed_at' => now()]);
        $order->update(['status' => Order::PAID, 'paid_at' => now()]);

        $now = now();
        $tickets = [];
        for ($seq = 1; $seq <= $reservation->quantity; $seq++) {
            $tickets[] = [
                'id' => (string) Str::uuid7(),
                'order_id' => $order->id,
                'event_id' => $reservation->event_id,
                'category_id' => $reservation->category_id,
                'code' => 'TKT-'.strtoupper(Str::random(14)),
                'holder_ref' => $order->customer_ref,
                'seq_in_order' => $seq,
                'issued_at' => $now,
            ];
        }
        Ticket::insert($tickets);

        $this->faults->trip(FaultInjector::BEFORE_PAYMENT_COMMIT);

        return 'paid';
    }

    private function abandonAttempt(string $orderId, string $attemptId): void
    {
        DB::transaction(function () use ($orderId, $attemptId) {
            $order = Order::whereKey($orderId)->lockForUpdate()->first();
            if ($order === null || $order->status === Order::PAID) {
                return;
            }
            $reservation = $this->lockReservation($order->reservation_id);
            // Deleting the attempt frees its Idempotency-Key, so a retry with the same key is a fresh attempt.
            PaymentAttempt::whereKey($attemptId)->where('status', PaymentAttempt::PROCESSING)->delete();
            if ($reservation->status !== Reservation::PAYMENT_PROCESSING) {
                return;
            }
            if ($reservation->past_expiry) {
                $this->expiry->expireLocked($reservation);
                $order->update(['status' => Order::CANCELLED]);
            } else {
                DB::update("UPDATE reservations SET status = 'active', updated_at = now() WHERE id = ?", [$reservation->id]);
            }
        });
    }

    private function replay(PaymentAttempt $attempt, string $orderId): Order
    {
        if ($attempt->order_id !== $orderId) {
            throw BusinessRuleException::idempotencyKeyReused();
        }

        if ($attempt->status === PaymentAttempt::PROCESSING) {
            // Either still running, or its process died after the first transaction; in the
            // latter case the sweeper has expired the reservation and the answer is final.
            $reservationStatus = DB::selectOne(
                'SELECT r.status FROM reservations r JOIN orders o ON o.reservation_id = r.id WHERE o.id = ?',
                [$orderId],
            )?->status;
            throw $reservationStatus === Reservation::EXPIRED
                ? BusinessRuleException::reservationExpired()
                : BusinessRuleException::paymentInProgress();
        }

        return match ($attempt->status) {
            PaymentAttempt::DECLINED => throw BusinessRuleException::paymentDeclined($this->freshOrder($orderId)->toApi()),
            PaymentAttempt::VOID => throw BusinessRuleException::reservationExpired(),
            default => $this->freshOrder($orderId),
        };
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

    private function freshOrder(string $orderId): Order
    {
        return Order::with('tickets')->findOrFail($orderId);
    }
}
