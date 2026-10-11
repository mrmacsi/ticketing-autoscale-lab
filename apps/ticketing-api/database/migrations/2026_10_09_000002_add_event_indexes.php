<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Support\Facades\DB;

/** The control plane's per-event inventory and invariant queries filter by event_id every 2 s. */
return new class extends Migration
{
    public function up(): void
    {
        DB::statement('CREATE INDEX IF NOT EXISTS reservations_event_status_idx ON reservations (event_id, status)');
        DB::statement('CREATE INDEX IF NOT EXISTS tickets_event_category_idx ON tickets (event_id, category_id)');
    }

    public function down(): void
    {
        DB::statement('DROP INDEX IF EXISTS reservations_event_status_idx');
        DB::statement('DROP INDEX IF EXISTS tickets_event_category_idx');
    }
};
