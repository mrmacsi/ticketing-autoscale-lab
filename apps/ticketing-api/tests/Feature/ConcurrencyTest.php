<?php

namespace Tests\Feature;

use App\Models\Reservation;
use App\Models\Ticket;
use App\Models\TicketCategory;
use Tests\Support\BuildsEvents;
use Tests\TestCase;

/**
 * Real concurrency against the real PostgreSQL engine: every competitor is a separate PHP process
 * with its own connection, released from a shared time barrier.
 */
class ConcurrencyTest extends TestCase
{
    use BuildsEvents;

    public function test_two_processes_racing_for_the_last_ticket_produce_exactly_one_winner(): void
    {
        $category = $this->eventWithCategory(1);

        $results = $this->race($category, 2, 'purchase');

        $this->assertSame(['sold_out' => 1, 'success' => 1], $results);
        $this->assertEquals((object) ['capacity' => 1, 'held' => 0, 'sold' => 1], $this->category($category));
        $this->assertNoInvariantViolations($category);
    }

    public function test_many_concurrent_purchases_never_oversell(): void
    {
        $category = $this->eventWithCategory(10);

        $results = $this->race($category, 60, 'purchase');

        $this->assertSame(10, $results['success'] ?? 0, json_encode($results));
        $this->assertSame(50, $results['sold_out'] ?? 0, json_encode($results));
        $this->assertSame(10, Ticket::count());
        $this->assertEquals((object) ['capacity' => 10, 'held' => 0, 'sold' => 10], $this->category($category));
        $this->assertNoInvariantViolations($category);
    }

    public function test_concurrent_reservations_with_the_same_admission_pass_produce_exactly_one(): void
    {
        $category = $this->eventWithCategory(100);

        // 30 different customers (own Idempotency-Keys) all presenting the same waiting-room pass.
        $results = $this->race($category, 30, 'reserve', 'q_shared_pass');

        $this->assertSame(['queue_token_used' => 29, 'success' => 1], $results);
        $this->assertSame(1, Reservation::where('queue_id', 'q_shared_pass')->count());
        $this->assertSame(1, Reservation::count());
        $this->assertEquals((object) ['capacity' => 100, 'held' => 1, 'sold' => 0], $this->category($category));
        $this->assertNoInvariantViolations($category);
    }

    /** @return array<string, int> outcome => count */
    private function race(TicketCategory $category, int $competitors, string $mode, ?string $queueId = null): array
    {
        $startAt = microtime(true) + 1.5;
        $env = array_merge(getenv(), $_ENV);
        $processes = [];
        $outputs = [];
        for ($i = 0; $i < $competitors; $i++) {
            $cmd = [PHP_BINARY, __DIR__.'/../Support/race_worker.php', $category->id, "racer-{$i}", (string) $startAt, $mode];
            if ($queueId !== null) {
                $cmd[] = $queueId;
            }
            $processes[] = proc_open($cmd, [1 => ['pipe', 'w'], 2 => ['pipe', 'w']], $pipes, base_path(), $env);
            $outputs[$i] = $pipes;
        }

        $counts = [];
        foreach ($processes as $i => $process) {
            $line = trim(stream_get_contents($outputs[$i][1]));
            $err = trim(stream_get_contents($outputs[$i][2]));
            proc_close($process);
            $decoded = json_decode($line, true) ?? ['result' => 'crash', 'message' => $err ?: $line];
            $this->assertNotSame('error', $decoded['result'], $decoded['message'] ?? '');
            $counts[$decoded['result']] = ($counts[$decoded['result']] ?? 0) + 1;
        }
        ksort($counts);

        return $counts;
    }
}
