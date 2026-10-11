<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Support\Facades\DB;

/**
 * Inventory correctness lives in PostgreSQL: the CHECK constraints below are the last line of
 * defence behind the conditional UPDATEs in the services, so even a buggy code path cannot
 * persist held + sold > capacity or negative counters.
 */
return new class extends Migration
{
    public function up(): void
    {
        DB::unprepared(<<<'SQL'
            CREATE TABLE events (
                id uuid PRIMARY KEY,
                name varchar(200) NOT NULL,
                venue varchar(200) NOT NULL DEFAULT 'Lab Arena',
                starts_at timestamptz NOT NULL,
                created_at timestamptz NOT NULL DEFAULT now(),
                updated_at timestamptz NOT NULL DEFAULT now()
            );
            CREATE INDEX events_created_at_idx ON events (created_at DESC);

            CREATE TABLE ticket_categories (
                id uuid PRIMARY KEY,
                event_id uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
                name varchar(100) NOT NULL,
                price_cents integer NOT NULL CHECK (price_cents >= 0),
                capacity integer NOT NULL CHECK (capacity >= 0),
                held integer NOT NULL DEFAULT 0,
                sold integer NOT NULL DEFAULT 0,
                created_at timestamptz NOT NULL DEFAULT now(),
                updated_at timestamptz NOT NULL DEFAULT now(),
                CONSTRAINT ticket_categories_held_non_negative CHECK (held >= 0),
                CONSTRAINT ticket_categories_sold_non_negative CHECK (sold >= 0),
                CONSTRAINT ticket_categories_within_capacity CHECK (held + sold <= capacity)
            );
            CREATE INDEX ticket_categories_event_idx ON ticket_categories (event_id);

            CREATE TABLE reservations (
                id uuid PRIMARY KEY,
                event_id uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
                category_id uuid NOT NULL REFERENCES ticket_categories (id) ON DELETE CASCADE,
                customer_ref varchar(100) NOT NULL,
                quantity integer NOT NULL CHECK (quantity BETWEEN 1 AND 8),
                status varchar(20) NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active', 'payment_processing', 'consumed', 'expired')),
                idempotency_key varchar(120) NOT NULL,
                request_fingerprint char(64) NOT NULL,
                expires_at timestamptz NOT NULL,
                consumed_at timestamptz NULL,
                expired_at timestamptz NULL,
                created_at timestamptz NOT NULL DEFAULT now(),
                updated_at timestamptz NOT NULL DEFAULT now(),
                CONSTRAINT reservations_idempotency_key_unique UNIQUE (idempotency_key)
            );
            CREATE INDEX reservations_category_status_idx ON reservations (category_id, status);
            CREATE INDEX reservations_expiry_scan_idx ON reservations (expires_at)
                WHERE status IN ('active', 'payment_processing');

            CREATE TABLE orders (
                id uuid PRIMARY KEY,
                reservation_id uuid NOT NULL REFERENCES reservations (id) ON DELETE CASCADE,
                event_id uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
                customer_ref varchar(100) NOT NULL,
                status varchar(20) NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'paid', 'payment_failed', 'cancelled')),
                amount_cents integer NOT NULL CHECK (amount_cents >= 0),
                payment_attempts integer NOT NULL DEFAULT 0,
                idempotency_key varchar(120) NOT NULL,
                paid_at timestamptz NULL,
                created_at timestamptz NOT NULL DEFAULT now(),
                updated_at timestamptz NOT NULL DEFAULT now(),
                CONSTRAINT orders_reservation_unique UNIQUE (reservation_id),
                CONSTRAINT orders_idempotency_key_unique UNIQUE (idempotency_key)
            );
            CREATE INDEX orders_event_status_idx ON orders (event_id, status);

            CREATE TABLE payment_attempts (
                id uuid PRIMARY KEY,
                order_id uuid NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
                idempotency_key varchar(120) NOT NULL,
                status varchar(20) NOT NULL DEFAULT 'processing'
                    CHECK (status IN ('processing', 'approved', 'declined', 'void')),
                delay_ms integer NOT NULL DEFAULT 0,
                failure_reason varchar(100) NULL,
                created_at timestamptz NOT NULL DEFAULT now(),
                completed_at timestamptz NULL,
                CONSTRAINT payment_attempts_idempotency_key_unique UNIQUE (idempotency_key)
            );
            CREATE INDEX payment_attempts_order_idx ON payment_attempts (order_id);
            -- At most one approved payment per order, whatever the client retries.
            CREATE UNIQUE INDEX payment_attempts_one_approved_per_order
                ON payment_attempts (order_id) WHERE status = 'approved';

            CREATE TABLE tickets (
                id uuid PRIMARY KEY,
                order_id uuid NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
                event_id uuid NOT NULL REFERENCES events (id) ON DELETE CASCADE,
                category_id uuid NOT NULL REFERENCES ticket_categories (id) ON DELETE CASCADE,
                code varchar(40) NOT NULL,
                holder_ref varchar(100) NOT NULL,
                seq_in_order integer NOT NULL,
                issued_at timestamptz NOT NULL DEFAULT now(),
                CONSTRAINT tickets_code_unique UNIQUE (code),
                CONSTRAINT tickets_order_seq_unique UNIQUE (order_id, seq_in_order)
            );
            CREATE INDEX tickets_category_idx ON tickets (category_id);

            CREATE TABLE lab_settings (
                key varchar(64) PRIMARY KEY,
                value jsonb NOT NULL,
                updated_at timestamptz NOT NULL DEFAULT now()
            );
        SQL);

        // The control plane reads inventory through a dedicated read-only role when it exists
        // (created by Terraform in Cloud SQL). Locally the role is usually absent.
        DB::unprepared(<<<'SQL'
            DO $$
            BEGIN
                IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'lab_observer') THEN
                    EXECUTE 'GRANT USAGE ON SCHEMA public TO lab_observer';
                    EXECUTE 'GRANT SELECT ON ALL TABLES IN SCHEMA public TO lab_observer';
                    EXECUTE 'ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO lab_observer';
                END IF;
            END
            $$;
        SQL);
    }

    public function down(): void
    {
        DB::unprepared('DROP TABLE IF EXISTS tickets, payment_attempts, orders, reservations, ticket_categories, events, lab_settings CASCADE');
    }
};
