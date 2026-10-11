<?php

namespace Tests\Feature;

use App\Models\Reservation;
use App\Models\Ticket;
use Tests\Support\BuildsEvents;
use Tests\TestCase;

class RollbackTest extends TestCase
{
    use BuildsEvents;

    public function test_failure_before_reservation_commit_leaves_no_hold(): void
    {
        $category = $this->eventWithCategory(5);

        $this->postJson('/api/reservations', ['category_id' => $category->id, 'quantity' => 2, 'customer_ref' => 'c'],
            ['Idempotency-Key' => 'rollback-res-1', 'X-Lab-Fault' => 'reservation-before-commit'])
            ->assertStatus(500)->assertJsonPath('error', 'injected_fault');

        $this->assertSame(0, Reservation::count());
        $this->assertEquals(0, $this->category($category)->held);
        // The key was never stored, so the client's retry succeeds normally.
        $this->reserve($category, 2, 'c', 'rollback-res-1')->assertCreated();
    }

    public function test_failure_before_payment_commit_sells_nothing_and_allows_retry(): void
    {
        $category = $this->eventWithCategory(5);
        $reservation = $this->reserve($category, 2)->json('data');
        $order = $this->order($reservation['id'])->json('data');

        $this->pay($order['id'], 'approve', 'rollback-pay-1', 0, ['X-Lab-Fault' => 'payment-before-commit'])->assertStatus(500);

        $this->assertSame(0, Ticket::count());
        $this->assertSame('active', $this->reservationStatus($reservation['id']));
        $this->assertEquals((object) ['capacity' => 5, 'held' => 2, 'sold' => 0], $this->category($category));
        $this->getJson("/api/orders/{$order['id']}")->assertJsonPath('data.status', 'pending');

        $this->pay($order['id'], 'approve', 'rollback-pay-1')->assertOk()->assertJsonCount(2, 'data.tickets');
        $this->assertNoInvariantViolations($category);
    }

    public function test_fault_header_is_ignored_when_fault_injection_is_disabled(): void
    {
        config(['lab.fault_injection_enabled' => false]);
        $category = $this->eventWithCategory(5);

        $this->postJson('/api/reservations', ['category_id' => $category->id, 'quantity' => 1, 'customer_ref' => 'c'],
            ['Idempotency-Key' => 'nofault-res-1', 'X-Lab-Fault' => 'reservation-before-commit'])->assertCreated();
    }
}
