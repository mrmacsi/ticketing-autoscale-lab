<?php

namespace Tests\Feature;

use App\Models\Order;
use App\Models\Ticket;
use Tests\Support\BuildsEvents;
use Tests\TestCase;

class CheckoutTest extends TestCase
{
    use BuildsEvents;

    public function test_successful_payment_issues_tickets_and_converts_held_to_sold(): void
    {
        $category = $this->eventWithCategory(10, 2500);
        $reservation = $this->reserve($category, 3)->json('data');
        $order = $this->order($reservation['id'])->assertCreated()->assertJsonPath('data.amount_cents', 7500)->json('data');

        $paid = $this->pay($order['id'])->assertOk();

        $paid->assertJsonPath('data.status', 'paid')->assertJsonCount(3, 'data.tickets');
        $this->assertSame('consumed', $this->reservationStatus($reservation['id']));
        $this->assertEquals((object) ['capacity' => 10, 'held' => 0, 'sold' => 3], $this->category($category));
        $ticketId = $paid->json('data.tickets.0.id');
        $this->getJson("/api/tickets/{$ticketId}")->assertOk()->assertJsonPath('data.holder_ref', 'cust-1');
        $this->getJson('/api/sales/summary?event_id='.$category->event_id)
            ->assertJsonPath('data.totals.sold', 3)->assertJsonPath('data.totals.revenue_cents', 7500);
        $this->assertNoInvariantViolations($category);
    }

    public function test_duplicate_order_submissions_return_the_same_order(): void
    {
        $category = $this->eventWithCategory(10);
        $reservation = $this->reserve($category, 1)->json('data');

        $first = $this->order($reservation['id'], 'cust-1', 'order-key-0001')->assertCreated();
        $sameKey = $this->order($reservation['id'], 'cust-1', 'order-key-0001')->assertOk();
        $otherKey = $this->order($reservation['id'], 'cust-1', 'order-key-0002')->assertOk();

        $this->assertSame($first->json('data.id'), $sameKey->json('data.id'));
        $this->assertSame($first->json('data.id'), $otherKey->json('data.id'));
        $this->assertSame(1, Order::count());
    }

    public function test_retry_after_unknown_response_does_not_charge_or_sell_twice(): void
    {
        $category = $this->eventWithCategory(10);
        $order = $this->order($this->reserve($category, 2)->json('data.id'))->json('data');

        // The client never saw the first answer and retries with the same key.
        $first = $this->pay($order['id'], 'approve', 'pay-key-unknown-1')->assertOk();
        $retry = $this->pay($order['id'], 'approve', 'pay-key-unknown-1')->assertOk();
        $newKey = $this->pay($order['id'], 'approve', 'pay-key-unknown-2')->assertOk();

        $this->assertSame($first->json('data.tickets'), $retry->json('data.tickets'));
        $this->assertSame('paid', $newKey->json('data.status'));
        $this->assertSame(2, Ticket::count());
        $this->assertEquals(2, $this->category($category)->sold);
        $this->assertNoInvariantViolations($category);
    }

    public function test_declined_payment_keeps_the_hold_and_can_be_retried(): void
    {
        $category = $this->eventWithCategory(10);
        $reservation = $this->reserve($category, 2)->json('data');
        $order = $this->order($reservation['id'])->json('data');

        $this->pay($order['id'], 'decline')->assertStatus(402)->assertJsonPath('error', 'payment_declined')
            ->assertJsonPath('data.status', 'payment_failed');
        $this->assertSame('active', $this->reservationStatus($reservation['id']));
        $this->assertEquals(2, $this->category($category)->held);

        $this->pay($order['id'], 'approve')->assertOk()->assertJsonPath('data.status', 'paid');
        $this->assertEquals((object) ['capacity' => 10, 'held' => 0, 'sold' => 2], $this->category($category));
    }

    public function test_delayed_payment_waits_for_the_simulated_provider(): void
    {
        $category = $this->eventWithCategory(10);
        $order = $this->order($this->reserve($category)->json('data.id'))->json('data');

        $started = microtime(true);
        $this->pay($order['id'], 'approve', null, 300)->assertOk();

        $this->assertGreaterThanOrEqual(0.3, microtime(true) - $started);
        $this->assertSame(300, (int) \DB::table('payment_attempts')->value('delay_ms'));
    }

    public function test_configured_failure_probability_drives_unforced_outcomes(): void
    {
        $this->settings(['payment_failure_probability' => 1, 'payment_delay_min_ms' => 0, 'payment_delay_max_ms' => 0]);
        $category = $this->eventWithCategory(10);
        $order = $this->order($this->reserve($category)->json('data.id'))->json('data');

        $this->postJson("/api/orders/{$order['id']}/simulate-payment", [], ['Idempotency-Key' => 'pay-prob-0001'])
            ->assertStatus(402);
    }

    public function test_ordering_someone_elses_reservation_is_forbidden(): void
    {
        $category = $this->eventWithCategory(10);
        $reservation = $this->reserve($category, 1, 'alice')->json('data');

        $this->order($reservation['id'], 'mallory')->assertStatus(403);
    }
}
