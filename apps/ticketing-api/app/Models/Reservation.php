<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Concerns\HasUuids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

class Reservation extends Model
{
    use HasUuids;

    public const ACTIVE = 'active';

    public const PAYMENT_PROCESSING = 'payment_processing';

    public const CONSUMED = 'consumed';

    public const EXPIRED = 'expired';

    /** States in which the reservation's quantity is counted in ticket_categories.held. */
    public const HOLDING_STATES = [self::ACTIVE, self::PAYMENT_PROCESSING];

    protected $guarded = [];

    protected function casts(): array
    {
        return [
            'quantity' => 'integer',
            'expires_at' => 'immutable_datetime',
            'consumed_at' => 'immutable_datetime',
            'expired_at' => 'immutable_datetime',
        ];
    }

    public function category(): BelongsTo
    {
        return $this->belongsTo(TicketCategory::class, 'category_id');
    }

    public function toApi(): array
    {
        return [
            'id' => $this->id,
            'event_id' => $this->event_id,
            'category_id' => $this->category_id,
            'quantity' => $this->quantity,
            'status' => $this->status,
            'customer_ref' => $this->customer_ref,
            'expires_at' => $this->expires_at?->toIso8601ZuluString('millisecond'),
            'created_at' => $this->created_at?->toIso8601ZuluString('millisecond'),
        ];
    }
}
