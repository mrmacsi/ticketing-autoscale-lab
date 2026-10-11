<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Support\Facades\DB;

/**
 * One waiting-room admission pass = one reservation. The queue id (`q` of the X-Queue-Token) is stored
 * on the reservation while `waiting_room_required` is on; the unique index makes a second reservation
 * with the same pass fail inside its own transaction, so its inventory hold is rolled back. NULL
 * (reservations made without the waiting room) is not indexed and may repeat.
 */
return new class extends Migration
{
    public function up(): void
    {
        DB::statement('ALTER TABLE reservations ADD COLUMN IF NOT EXISTS queue_id text NULL');
        DB::statement('CREATE UNIQUE INDEX IF NOT EXISTS reservations_queue_id_unique ON reservations (queue_id) WHERE queue_id IS NOT NULL');
    }

    public function down(): void
    {
        DB::statement('DROP INDEX IF EXISTS reservations_queue_id_unique');
        DB::statement('ALTER TABLE reservations DROP COLUMN IF EXISTS queue_id');
    }
};
