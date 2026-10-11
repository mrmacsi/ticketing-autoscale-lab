<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Concerns\HasUuids;
use Illuminate\Database\Eloquent\Model;

class PaymentAttempt extends Model
{
    use HasUuids;

    public const PROCESSING = 'processing';

    public const APPROVED = 'approved';

    public const DECLINED = 'declined';

    public const VOID = 'void';

    public $timestamps = false;

    protected $guarded = [];

    protected function casts(): array
    {
        return ['delay_ms' => 'integer', 'created_at' => 'immutable_datetime', 'completed_at' => 'immutable_datetime'];
    }
}
