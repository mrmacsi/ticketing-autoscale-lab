import pg from 'pg';
import type { AppConfig } from '../config.js';
import type { InventoryView } from '../types.js';

// Same checks as App\Services\InventoryAdmin::INVARIANT_SQL in the ticketing API, evaluated in one snapshot.
const CATEGORY_SQL = `
  SELECT c.id, c.name, c.capacity, c.held, c.sold,
         COALESCE(r.reserved_quantity, 0)::int AS reserved_quantity,
         COALESCE(r.consumed_quantity, 0)::int AS consumed_quantity,
         COALESCE(t.tickets_issued, 0)::int AS tickets_issued
    FROM ticket_categories c
    LEFT JOIN (SELECT category_id,
                      SUM(quantity) FILTER (WHERE status IN ('active', 'payment_processing')) AS reserved_quantity,
                      SUM(quantity) FILTER (WHERE status = 'consumed') AS consumed_quantity
                 FROM reservations WHERE event_id = $1 GROUP BY category_id) r ON r.category_id = c.id
    LEFT JOIN (SELECT category_id, COUNT(*) AS tickets_issued FROM tickets WHERE event_id = $1 GROUP BY category_id) t
           ON t.category_id = c.id
   WHERE c.event_id = $1
   ORDER BY c.price_cents`;

const RESERVATION_SQL = `
  SELECT COUNT(*) FILTER (WHERE status = 'active')::int AS active,
         COUNT(*) FILTER (WHERE status = 'payment_processing')::int AS processing,
         COUNT(*) FILTER (WHERE status = 'expired')::int AS expired
    FROM reservations WHERE event_id = $1`;

export interface CategoryRow {
  id: string; name: string; capacity: number; held: number; sold: number;
  reserved_quantity: number; consumed_quantity: number; tickets_issued: number;
}

export function countViolations(rows: CategoryRow[]): number {
  let violations = 0;
  for (const r of rows) {
    if (r.held < 0) violations++;
    if (r.sold < 0) violations++;
    if (r.held + r.sold > r.capacity) violations++;
    if (r.held !== r.reserved_quantity) violations++;
    if (r.sold !== r.tickets_issued) violations++;
    if (r.sold !== r.consumed_quantity) violations++;
  }
  return violations;
}

/** Authoritative inventory straight from PostgreSQL via a read-only role, independent of app health. */
export class InventoryReader {
  private readonly pool: pg.Pool | null;

  constructor(db: AppConfig['database']) {
    // Cloud SQL requires TLS (ENCRYPTED_ONLY); the server is reached by private PSC IP, so the
    // certificate's hostname cannot match and only encryption is enforced.
    this.pool = db
      ? new pg.Pool({
        host: db.host, port: db.port, database: db.database, user: db.user, password: db.password,
        ssl: db.ssl ? { rejectUnauthorized: false } : false,
        max: 2, connectionTimeoutMillis: 3000, idleTimeoutMillis: 30000, statement_timeout: 5000,
      })
      : null;
    this.pool?.on('error', () => undefined);
  }

  get available() { return this.pool !== null; }

  async read(eventId: string): Promise<InventoryView> {
    if (!this.pool) throw new Error('Database access is not configured');
    const client = await this.pool.connect();
    try {
      await client.query('BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY');
      const categories = (await client.query<CategoryRow>(CATEGORY_SQL, [eventId])).rows;
      const reservations = (await client.query(RESERVATION_SQL, [eventId])).rows[0];
      await client.query('COMMIT');
      const sum = (key: 'capacity' | 'held' | 'sold' | 'tickets_issued') => categories.reduce((s, c) => s + Number(c[key]), 0);
      const capacity = sum('capacity');
      const held = sum('held');
      const sold = sum('sold');
      return {
        observedAt: new Date().toISOString(),
        eventId,
        capacity, held, sold,
        available: capacity - held - sold,
        activeReservations: reservations.active,
        paymentProcessing: reservations.processing,
        expiredReservations: reservations.expired,
        ticketsIssued: sum('tickets_issued'),
        invariantViolations: countViolations(categories),
        categories: categories.map((c) => ({ id: c.id, name: c.name, capacity: c.capacity, held: c.held, sold: c.sold })),
      };
    } catch (error) {
      await client.query('ROLLBACK').catch(() => undefined);
      throw error;
    } finally {
      client.release();
    }
  }

  async close() { await this.pool?.end(); }
}
