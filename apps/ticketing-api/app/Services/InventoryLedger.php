<?php

namespace App\Services;

use Illuminate\Support\Facades\DB;
use LogicException;

/**
 * The only code that changes ticket_categories.held / sold. Every change is a single conditional
 * UPDATE: PostgreSQL re-evaluates the WHERE clause against the latest committed row after waiting
 * for a concurrent writer's row lock, so two requests can never both take the last ticket.
 */
class InventoryLedger
{
    /** @return object{id: string, event_id: string, price_cents: int}|null null when not enough tickets remain */
    public function hold(string $categoryId, int $quantity): ?object
    {
        return DB::selectOne(
            'UPDATE ticket_categories
                SET held = held + ?, updated_at = now()
              WHERE id = ? AND capacity - held - sold >= ?
          RETURNING id, event_id, price_cents',
            [$quantity, $categoryId, $quantity],
        );
    }

    public function release(string $categoryId, int $quantity): void
    {
        $this->expectOneRow(DB::update(
            'UPDATE ticket_categories SET held = held - ?, updated_at = now() WHERE id = ? AND held >= ?',
            [$quantity, $categoryId, $quantity],
        ), 'release');
    }

    public function convertHeldToSold(string $categoryId, int $quantity): void
    {
        $this->expectOneRow(DB::update(
            'UPDATE ticket_categories SET held = held - ?, sold = sold + ?, updated_at = now() WHERE id = ? AND held >= ?',
            [$quantity, $quantity, $categoryId, $quantity],
        ), 'sell');
    }

    private function expectOneRow(int $affected, string $operation): void
    {
        // A reservation in a holding state always has its quantity in `held`; anything else means
        // the ledger and the reservations disagree, so abort the transaction instead of drifting.
        if ($affected !== 1) {
            throw new LogicException("Inventory ledger mismatch during {$operation}");
        }
    }
}
