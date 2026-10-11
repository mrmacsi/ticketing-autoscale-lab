<?php

namespace Tests\Support;

use App\Models\Reservation;
use App\Models\TicketCategory;
use App\Services\InventoryAdmin;
use App\Services\LabSettings;
use Illuminate\Support\Facades\DB;
use Illuminate\Testing\TestResponse;

trait BuildsEvents
{
    protected function eventWithCategory(int $capacity, int $priceCents = 5000): TicketCategory
    {
        $event = app(InventoryAdmin::class)->createEvent('Test Night', [
            ['name' => 'General', 'price_cents' => $priceCents, 'capacity' => $capacity],
        ]);

        return $event->categories->first();
    }

    protected function settings(array $values): void
    {
        app(LabSettings::class)->update($values);
    }

    protected function reserve(TicketCategory $category, int $quantity = 1, string $customer = 'cust-1', ?string $key = null): TestResponse
    {
        return $this->postJson('/api/reservations', [
            'category_id' => $category->id, 'quantity' => $quantity, 'customer_ref' => $customer,
        ], ['Idempotency-Key' => $key ?? 'res-'.bin2hex(random_bytes(8))]);
    }

    protected function order(string $reservationId, string $customer = 'cust-1', ?string $key = null): TestResponse
    {
        return $this->postJson('/api/orders', ['reservation_id' => $reservationId, 'customer_ref' => $customer],
            ['Idempotency-Key' => $key ?? 'ord-'.bin2hex(random_bytes(8))]);
    }

    protected function pay(string $orderId, string $outcome = 'approve', ?string $key = null, int $delayMs = 0, array $headers = []): TestResponse
    {
        return $this->postJson("/api/orders/{$orderId}/simulate-payment", ['outcome' => $outcome, 'delay_ms' => $delayMs],
            array_merge(['Idempotency-Key' => $key ?? 'pay-'.bin2hex(random_bytes(8))], $headers));
    }

    protected function category(TicketCategory $category): object
    {
        return DB::selectOne('SELECT capacity, held, sold FROM ticket_categories WHERE id = ?', [$category->id]);
    }

    protected function backdateReservation(string $id, int $seconds): void
    {
        DB::update('UPDATE reservations SET expires_at = now() - make_interval(secs => ?) WHERE id = ?', [$seconds, $id]);
    }

    protected function assertNoInvariantViolations(TicketCategory $category): void
    {
        $this->assertSame([], app(InventoryAdmin::class)->invariantViolations($category->event_id));
    }

    protected function reservationStatus(string $id): string
    {
        return Reservation::findOrFail($id)->status;
    }
}
