<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Concerns\HasUuids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/**
 * held and sold are only ever changed with conditional SQL updates (see InventoryLedger);
 * they are intentionally not mass assignable.
 */
class TicketCategory extends Model
{
    use HasUuids;

    protected $fillable = ['event_id', 'name', 'price_cents', 'capacity'];

    protected function casts(): array
    {
        return ['price_cents' => 'integer', 'capacity' => 'integer', 'held' => 'integer', 'sold' => 'integer'];
    }

    public function event(): BelongsTo
    {
        return $this->belongsTo(Event::class);
    }

    public function available(): int
    {
        return max(0, $this->capacity - $this->held - $this->sold);
    }
}
