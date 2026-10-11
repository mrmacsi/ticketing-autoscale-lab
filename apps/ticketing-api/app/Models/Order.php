<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Concerns\HasUuids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;
use Illuminate\Database\Eloquent\Relations\HasMany;

class Order extends Model
{
    use HasUuids;

    public const PENDING = 'pending';

    public const PAID = 'paid';

    public const PAYMENT_FAILED = 'payment_failed';

    public const CANCELLED = 'cancelled';

    protected $guarded = [];

    protected function casts(): array
    {
        return ['amount_cents' => 'integer', 'payment_attempts' => 'integer', 'paid_at' => 'immutable_datetime'];
    }

    public function reservation(): BelongsTo
    {
        return $this->belongsTo(Reservation::class);
    }

    public function tickets(): HasMany
    {
        return $this->hasMany(Ticket::class)->orderBy('seq_in_order');
    }

    public function toApi(): array
    {
        return [
            'id' => $this->id,
            'reservation_id' => $this->reservation_id,
            'event_id' => $this->event_id,
            'customer_ref' => $this->customer_ref,
            'status' => $this->status,
            'amount_cents' => $this->amount_cents,
            'payment_attempts' => $this->payment_attempts,
            'tickets' => $this->tickets->map->toApi()->all(),
            'created_at' => $this->created_at?->toIso8601ZuluString('millisecond'),
            'paid_at' => $this->paid_at?->toIso8601ZuluString('millisecond'),
        ];
    }
}
