<?php

namespace App\Services;

use App\Models\Event;
use App\Models\TicketCategory;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Str;

class InventoryAdmin
{
    public function __construct(private readonly LabSettings $settings) {}

    /** @param array<int, array{name: string, price_cents: int, capacity: int}> $categories */
    public function createEvent(string $name, array $categories): Event
    {
        return DB::transaction(function () use ($name, $categories) {
            $event = Event::create([
                'id' => (string) Str::uuid7(),
                'name' => $name,
                'venue' => 'Lab Arena',
                'starts_at' => now()->addDays(30)->startOfHour(),
            ]);
            foreach ($categories as $category) {
                TicketCategory::create([
                    'id' => (string) Str::uuid7(),
                    'event_id' => $event->id,
                    'name' => $category['name'],
                    'price_cents' => $category['price_cents'],
                    'capacity' => $category['capacity'],
                ]);
            }

            return $event->load('categories');
        });
    }

    /**
     * Return an event to its initial state: no reservations, orders or tickets. Category rows are
     * locked first so no reservation can slip in while the reset runs.
     *
     * @return array<string, int|string>
     */
    public function resetInventory(string $eventId): array
    {
        return DB::transaction(function () use ($eventId) {
            Event::findOrFail($eventId);
            DB::select('SELECT id FROM ticket_categories WHERE event_id = ? ORDER BY id FOR UPDATE', [$eventId]);

            $tickets = DB::delete('DELETE FROM tickets WHERE event_id = ?', [$eventId]);
            $orders = DB::delete('DELETE FROM orders WHERE event_id = ?', [$eventId]);
            $released = DB::delete("DELETE FROM reservations WHERE event_id = ? AND status IN ('active', 'payment_processing')", [$eventId]);
            DB::delete('DELETE FROM reservations WHERE event_id = ?', [$eventId]);
            DB::update('UPDATE ticket_categories SET held = 0, sold = 0, updated_at = now() WHERE event_id = ?', [$eventId]);

            return [
                'event_id' => $eventId,
                'released_reservations' => $released,
                'deleted_orders' => $orders,
                'deleted_tickets' => $tickets,
            ];
        });
    }

    /**
     * Cross-check the counters against the rows they summarise, in one snapshot. Any entry in the
     * result is a correctness bug; the expected list is always empty.
     *
     * @return array<int, array<string, mixed>>
     */
    public function invariantViolations(string $eventId): array
    {
        $rows = DB::select(self::INVARIANT_SQL, [$eventId]);
        $violations = [];
        foreach ($rows as $row) {
            $checks = [
                'held_negative' => $row->held < 0,
                'sold_negative' => $row->sold < 0,
                'over_capacity' => $row->held + $row->sold > $row->capacity,
                'held_mismatch' => (int) $row->held !== (int) $row->reserved_quantity,
                'sold_mismatch' => (int) $row->sold !== (int) $row->tickets_issued,
                'consumed_mismatch' => (int) $row->sold !== (int) $row->consumed_quantity,
            ];
            foreach (array_keys(array_filter($checks)) as $rule) {
                $violations[] = ['category_id' => $row->id, 'category' => $row->name, 'rule' => $rule, 'row' => (array) $row];
            }
        }

        return $violations;
    }

    public function salesSummary(?string $eventId): array
    {
        $eventId ??= Event::latest('created_at')->value('id');
        if ($eventId === null) {
            return ['event_id' => null, 'categories' => [], 'totals' => ['capacity' => 0, 'held' => 0, 'sold' => 0, 'revenue_cents' => 0, 'orders_paid' => 0]];
        }
        $categories = DB::select(
            'SELECT id, name, price_cents, capacity, held, sold, sold * price_cents AS revenue_cents
               FROM ticket_categories WHERE event_id = ? ORDER BY price_cents', [$eventId]);
        $ordersPaid = DB::selectOne("SELECT count(*) AS c FROM orders WHERE event_id = ? AND status = 'paid'", [$eventId])->c;

        return [
            'event_id' => $eventId,
            'categories' => $categories,
            'totals' => [
                'capacity' => array_sum(array_column($categories, 'capacity')),
                'held' => array_sum(array_column($categories, 'held')),
                'sold' => array_sum(array_column($categories, 'sold')),
                'revenue_cents' => array_sum(array_column($categories, 'revenue_cents')),
                'orders_paid' => (int) $ordersPaid,
            ],
        ];
    }

    public const INVARIANT_SQL = <<<'SQL'
        SELECT c.id, c.name, c.capacity, c.held, c.sold,
               COALESCE(r.reserved_quantity, 0) AS reserved_quantity,
               COALESCE(r.consumed_quantity, 0) AS consumed_quantity,
               COALESCE(t.tickets_issued, 0) AS tickets_issued
          FROM ticket_categories c
     LEFT JOIN (
                SELECT category_id,
                       SUM(quantity) FILTER (WHERE status IN ('active', 'payment_processing')) AS reserved_quantity,
                       SUM(quantity) FILTER (WHERE status = 'consumed') AS consumed_quantity
                  FROM reservations GROUP BY category_id
               ) r ON r.category_id = c.id
     LEFT JOIN (SELECT category_id, COUNT(*) AS tickets_issued FROM tickets GROUP BY category_id) t ON t.category_id = c.id
         WHERE c.event_id = ?
      ORDER BY c.price_cents
    SQL;
}
