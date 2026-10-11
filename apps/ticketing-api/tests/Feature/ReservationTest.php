<?php

namespace Tests\Feature;

use Tests\Support\BuildsEvents;
use Tests\TestCase;

class ReservationTest extends TestCase
{
    use BuildsEvents;

    public function test_successful_reservation_holds_inventory(): void
    {
        $category = $this->eventWithCategory(10);

        $response = $this->reserve($category, 3);

        $response->assertCreated()
            ->assertJsonPath('data.status', 'active')
            ->assertJsonPath('data.quantity', 3)
            ->assertHeader('X-Served-By', 'test-instance-1');
        $this->assertEquals((object) ['capacity' => 10, 'held' => 3, 'sold' => 0], $this->category($category));
        $this->assertNoInvariantViolations($category);
    }

    public function test_insufficient_inventory_is_a_409_business_rejection(): void
    {
        $category = $this->eventWithCategory(2);
        $this->reserve($category, 2)->assertCreated();

        $this->reserve($category, 1, 'cust-2')
            ->assertStatus(409)
            ->assertJsonPath('error', 'sold_out');
        $this->reserve($this->eventWithCategory(3), 4)->assertStatus(409);

        $this->assertEquals(2, $this->category($category)->held);
        $this->assertNoInvariantViolations($category);
    }

    public function test_two_customers_competing_for_the_last_ticket(): void
    {
        $category = $this->eventWithCategory(1);

        $first = $this->reserve($category, 1, 'alice');
        $second = $this->reserve($category, 1, 'bob');

        $first->assertCreated();
        $second->assertStatus(409);
        $this->assertEquals((object) ['capacity' => 1, 'held' => 1, 'sold' => 0], $this->category($category));
    }

    public function test_duplicate_idempotency_key_replays_the_original_reservation(): void
    {
        $category = $this->eventWithCategory(10);

        $first = $this->reserve($category, 2, 'cust-1', 'dup-key-000001');
        $replay = $this->reserve($category, 2, 'cust-1', 'dup-key-000001');

        $first->assertCreated();
        $replay->assertOk()->assertHeader('Idempotent-Replayed', 'true');
        $this->assertSame($first->json('data.id'), $replay->json('data.id'));
        $this->assertEquals(2, $this->category($category)->held, 'a replay must not hold tickets twice');
    }

    public function test_reusing_a_key_for_a_different_request_is_rejected(): void
    {
        $category = $this->eventWithCategory(10);
        $this->reserve($category, 2, 'cust-1', 'dup-key-000002')->assertCreated();

        $this->reserve($category, 3, 'cust-1', 'dup-key-000002')
            ->assertStatus(422)
            ->assertJsonPath('error', 'idempotency_key_reused');
        $this->assertEquals(2, $this->category($category)->held);
    }

    public function test_idempotency_key_and_input_are_validated(): void
    {
        $category = $this->eventWithCategory(10);

        $this->postJson('/api/reservations', ['category_id' => $category->id, 'quantity' => 1, 'customer_ref' => 'c'])
            ->assertStatus(400)->assertJsonPath('error', 'idempotency_key_required');
        $this->reserve($category, 9)->assertStatus(422)->assertJsonPath('error', 'validation_failed');
        $this->postJson('/api/reservations', ['category_id' => '0192f0c4-0000-7000-8000-000000000000', 'quantity' => 1, 'customer_ref' => 'c'],
            ['Idempotency-Key' => 'missing-category-1'])->assertNotFound();
    }

    public function test_availability_reports_remaining_tickets(): void
    {
        $category = $this->eventWithCategory(5);
        $this->reserve($category, 2)->assertCreated();

        $this->getJson("/api/events/{$category->event_id}/availability")
            ->assertOk()
            ->assertJsonPath('data.categories.0.available', 3)
            ->assertJsonPath('data.categories.0.held', 2)
            ->assertJsonPath('data.sold_out', false);
        $this->getJson('/api/events')->assertOk()->assertJsonPath('data.0.categories.0.capacity', 5);
    }
}
