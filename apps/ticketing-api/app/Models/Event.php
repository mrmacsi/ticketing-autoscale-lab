<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Concerns\HasUuids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\HasMany;

class Event extends Model
{
    use HasUuids;

    protected $fillable = ['name', 'venue', 'starts_at'];

    protected function casts(): array
    {
        return ['starts_at' => 'immutable_datetime'];
    }

    public function categories(): HasMany
    {
        return $this->hasMany(TicketCategory::class)->orderBy('price_cents');
    }
}
