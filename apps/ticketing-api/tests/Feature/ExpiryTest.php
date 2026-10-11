<?php

namespace Tests\Feature;

use App\Services\ReservationExpiry;
use Illuminate\Support\Facades\Artisan;
use Tests\Support\BuildsEvents;
use Tests\TestCase;

class ExpiryTest extends TestCase
{
    use BuildsEvents;

    public function test_reservation_ttl_comes_from_runtime_settings(): void
    {
        $this->settings(['reservation_ttl_seconds' => 30]);
        $category = $this->eventWithCategory(5);

        $reservation = $this->reserve($category)->json('data');

        $ttl = strtotime($reservation['expires_at']) - strtotime($reservation['created_at']);
        $this->assertEqualsWithDelta(30, $ttl, 1);
    }

    public function test_background_sweep_releases_expired_holds_exactly_once(): void
    {
        $category = $this->eventWithCategory(5);
        $expiring = $this->reserve($category, 2)->json('data');
        $this->reserve($category, 1)->assertCreated();
        $this->backdateReservation($expiring['id'], 1);

        Artisan::call('reservations:expire');
        $second = app(ReservationExpiry::class)->sweep();

        $this->assertSame('expired', $this->reservationStatus($expiring['id']));
        $this->assertSame(0, $second['expired']);
        $this->assertEquals(1, $this->category($category)->held);
        $this->reserve($category, 4)->assertCreated();   // capacity became available again
        $this->assertNoInvariantViolations($category);
    }

    public function test_reservation_expiring_during_checkout(): void
    {
        $category = $this->eventWithCategory(5);
        $reservation = $this->reserve($category, 2)->json('data');
        $order = $this->order($reservation['id'])->assertCreated()->json('data');
        $this->backdateReservation($reservation['id'], 1);

        $this->pay($order['id'])->assertStatus(410)->assertJsonPath('error', 'reservation_expired');

        $this->assertSame('expired', $this->reservationStatus($reservation['id']));
        $this->assertEquals((object) ['capacity' => 5, 'held' => 0, 'sold' => 0], $this->category($category));
        $this->getJson("/api/orders/{$order['id']}")->assertJsonPath('data.status', 'cancelled');
        $this->assertNoInvariantViolations($category);
    }

    public function test_ordering_an_expired_reservation_releases_it(): void
    {
        $category = $this->eventWithCategory(5);
        $reservation = $this->reserve($category, 2)->json('data');
        $this->backdateReservation($reservation['id'], 1);

        $this->order($reservation['id'])->assertStatus(410);

        $this->assertEquals(0, $this->category($category)->held);
    }

    public function test_payment_in_progress_is_only_expired_after_the_grace_period(): void
    {
        $category = $this->eventWithCategory(5);
        $reservation = $this->reserve($category, 1)->json('data');
        $order = $this->order($reservation['id'])->json('data');

        // Simulate a process that died right after moving the reservation to payment_processing.
        $this->pay($order['id'], 'approve', 'pay-crash-0001', 0, ['X-Lab-Fault' => 'payment-after-start'])->assertStatus(500);
        $this->assertSame('payment_processing', $this->reservationStatus($reservation['id']));
        $this->pay($order['id'], 'approve', 'pay-crash-0001')->assertStatus(409)->assertJsonPath('error', 'payment_in_progress');

        $this->backdateReservation($reservation['id'], 1);
        app(ReservationExpiry::class)->sweep();
        $this->assertSame('payment_processing', $this->reservationStatus($reservation['id']), 'still within grace');

        $this->backdateReservation($reservation['id'], config('lab.payment_grace_seconds') + 1);
        app(ReservationExpiry::class)->sweep();
        $this->assertSame('expired', $this->reservationStatus($reservation['id']));
        $this->assertEquals(0, $this->category($category)->held);
        $this->pay($order['id'], 'approve', 'pay-crash-0001')->assertStatus(410);
        $this->assertNoInvariantViolations($category);
    }
}
