<?php

namespace App\Http\Controllers;

use App\Models\Event;
use App\Models\TicketCategory;
use Illuminate\Database\Eloquent\ModelNotFoundException;
use Illuminate\Http\JsonResponse;
use Illuminate\Support\Facades\DB;

class EventController extends Controller
{
    public function index(): JsonResponse
    {
        $events = Event::with('categories')->latest('created_at')->limit(20)->get();

        return response()->json(['data' => $events->map(fn (Event $e) => $this->present($e))]);
    }

    public function show(string $id): JsonResponse
    {
        return response()->json(['data' => $this->present(Event::with('categories')->findOrFail($id))]);
    }

    /**
     * Approximate by design: a plain read, no locks, may be stale by the time the user reserves.
     * The hottest endpoint: one query; the event's existence is only checked when it has no categories.
     */
    public function availability(string $id): JsonResponse
    {
        $rows = DB::select(
            'SELECT id, name, price_cents, capacity, held, GREATEST(capacity - held - sold, 0) AS available
               FROM ticket_categories WHERE event_id = ? ORDER BY price_cents', [$id]);
        if ($rows === [] && ! Event::whereKey($id)->exists()) {
            throw (new ModelNotFoundException)->setModel(Event::class, [$id]);
        }
        $categories = array_map(fn ($r) => [
            'id' => $r->id, 'name' => $r->name, 'price_cents' => (int) $r->price_cents,
            'capacity' => (int) $r->capacity, 'available' => (int) $r->available,
            // In other customers' checkouts: may come back on sale if their hold expires.
            'held' => (int) $r->held,
        ], $rows);

        return response()->json(['data' => [
            'event_id' => $id,
            'sold_out' => array_sum(array_column($categories, 'available')) === 0,
            'categories' => $categories,
            'as_of' => now()->toIso8601ZuluString('millisecond'),
        ]]);
    }

    private function present(Event $event): array
    {
        return [
            'id' => $event->id,
            'name' => $event->name,
            'venue' => $event->venue,
            'starts_at' => $event->starts_at->toIso8601ZuluString(),
            'created_at' => $event->created_at?->toIso8601ZuluString(),
            'categories' => $event->categories->map(fn (TicketCategory $c) => [
                'id' => $c->id, 'event_id' => $c->event_id, 'name' => $c->name,
                'price_cents' => $c->price_cents, 'capacity' => $c->capacity,
            ])->all(),
        ];
    }
}
