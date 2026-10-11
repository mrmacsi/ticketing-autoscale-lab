<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Concerns\HasUuids;
use Illuminate\Database\Eloquent\Model;

class Ticket extends Model
{
    use HasUuids;

    public $timestamps = false;

    protected $guarded = [];

    protected function casts(): array
    {
        return ['issued_at' => 'immutable_datetime', 'seq_in_order' => 'integer'];
    }

    public function toApi(): array
    {
        return [
            'id' => $this->id,
            'order_id' => $this->order_id,
            'event_id' => $this->event_id,
            'category_id' => $this->category_id,
            'code' => $this->code,
            'holder_ref' => $this->holder_ref,
            'issued_at' => $this->issued_at?->toIso8601ZuluString('millisecond'),
        ];
    }
}
