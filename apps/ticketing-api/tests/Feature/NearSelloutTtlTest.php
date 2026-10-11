<?php

namespace Tests\Feature;

use App\Models\Event;
use App\Models\TicketCategory;
use App\Services\InventoryAdmin;
use Illuminate\Support\Facades\DB;
use Tests\Support\BuildsEvents;
use Tests\TestCase;

/** Shorter reservation holds once a category is about to sell out (lab settings near_sellout_*). */
class NearSelloutTtlTest extends TestCase
{
    use BuildsEvents;

    private const ADMIN = ['Authorization' => 'Bearer test-admin-token-0123456789'];

    public function test_settings_default_to_off(): void
    {
        $this->getJson('/api/admin/settings', self::ADMIN)->assertOk()
            ->assertJsonPath('data.reservation_ttl_near_sellout_seconds', 0)
            ->assertJsonPath('data.near_sellout_threshold', 0);

        $category = $this->eventWithCategory(1);
        $this->settings(['reservation_ttl_seconds' => 120]);

        // Even the very last ticket gets the normal hold while the feature is off.
        $this->assertSame(120, $this->holdSeconds($this->reserve($category)->assertCreated()->json('data.id')));
    }

    public function test_hold_above_the_threshold_uses_the_normal_ttl(): void
    {
        $category = $this->eventWithCategory(100);
        $this->settings(['reservation_ttl_seconds' => 120, 'reservation_ttl_near_sellout_seconds' => 15, 'near_sellout_threshold' => 10]);

        // 100 - 89 = 11 left after this hold: above the threshold.
        $this->assertSame(120, $this->holdSeconds($this->reserveQuantity($category, 89)));
    }

    public function test_hold_at_or_below_the_threshold_uses_the_short_ttl(): void
    {
        $category = $this->eventWithCategory(100);
        $this->settings(['reservation_ttl_seconds' => 120, 'reservation_ttl_near_sellout_seconds' => 15, 'near_sellout_threshold' => 10]);

        $this->assertSame(120, $this->holdSeconds($this->reserveQuantity($category, 88)));  // 12 left
        $this->assertSame(15, $this->holdSeconds($this->reserve($category, 2)->json('data.id')));  // 10 left: at
        $this->assertSame(15, $this->holdSeconds($this->reserve($category, 1)->json('data.id')));  // 9 left: below

        // The response's expires_at reflects the short hold as well.
        $data = $this->reserve($category, 1)->assertCreated()->json('data');
        $seconds = strtotime($data['expires_at']) - strtotime($data['created_at']);
        $this->assertEqualsWithDelta(15, $seconds, 1);
    }

    public function test_threshold_zero_shortens_only_the_hold_that_takes_the_last_tickets(): void
    {
        $category = $this->eventWithCategory(3);
        $this->settings(['reservation_ttl_seconds' => 60, 'reservation_ttl_near_sellout_seconds' => 5, 'near_sellout_threshold' => 0]);

        $this->assertSame(60, $this->holdSeconds($this->reserve($category, 2)->json('data.id')));
        $this->assertSame(5, $this->holdSeconds($this->reserve($category, 1)->json('data.id')));
    }

    public function test_short_ttl_zero_disables_the_feature_whatever_the_threshold(): void
    {
        $category = $this->eventWithCategory(10);
        $this->settings(['reservation_ttl_seconds' => 120, 'reservation_ttl_near_sellout_seconds' => 0, 'near_sellout_threshold' => 1000000]);

        $this->assertSame(120, $this->holdSeconds($this->reserve($category)->json('data.id')));
    }

    public function test_the_short_ttl_never_lengthens_a_hold(): void
    {
        $category = $this->eventWithCategory(10);
        $this->settings(['reservation_ttl_seconds' => 30, 'reservation_ttl_near_sellout_seconds' => 300, 'near_sellout_threshold' => 100]);

        $this->assertSame(30, $this->holdSeconds($this->reserve($category)->json('data.id')));
    }

    public function test_the_threshold_is_per_category(): void
    {
        $event = app(InventoryAdmin::class)->createEvent('Two tiers', [
            ['name' => 'Small', 'price_cents' => 9000, 'capacity' => 5],
            ['name' => 'Large', 'price_cents' => 3000, 'capacity' => 500],
        ]);
        $this->settings(['reservation_ttl_seconds' => 120, 'reservation_ttl_near_sellout_seconds' => 20, 'near_sellout_threshold' => 10]);

        $this->assertSame(20, $this->holdSeconds($this->reserve($this->named($event, 'Small'))->json('data.id')));
        $this->assertSame(120, $this->holdSeconds($this->reserve($this->named($event, 'Large'))->json('data.id')));
    }

    public function test_validation(): void
    {
        $bad = [
            ['reservation_ttl_near_sellout_seconds' => 4],
            ['reservation_ttl_near_sellout_seconds' => 1],
            ['reservation_ttl_near_sellout_seconds' => 3601],
            ['reservation_ttl_near_sellout_seconds' => -1],
            ['reservation_ttl_near_sellout_seconds' => 'soon'],
            ['near_sellout_threshold' => -1],
            ['near_sellout_threshold' => 1000001],
            ['near_sellout_threshold' => 2.5],
        ];
        foreach ($bad as $i => $values) {
            $response = $this->putJson('/api/admin/settings', $values, self::ADMIN);
            $this->assertSame(422, $response->status(), "case {$i}");
            $this->assertSame('validation_failed', $response->json('error'), "case {$i}");
        }

        foreach ([[0, 0], [5, 1000000], [3600, 25]] as [$ttl, $threshold]) {
            $this->putJson('/api/admin/settings', ['reservation_ttl_near_sellout_seconds' => $ttl, 'near_sellout_threshold' => $threshold], self::ADMIN)
                ->assertOk()
                ->assertJsonPath('data.reservation_ttl_near_sellout_seconds', $ttl)
                ->assertJsonPath('data.near_sellout_threshold', $threshold);
        }
    }

    public function test_prepare_accepts_the_settings(): void
    {
        $response = $this->postJson('/api/admin/experiments/prepare', [
            'event_name' => 'Near sell-out',
            'categories' => [['name' => 'GA', 'price_cents' => 5000, 'capacity' => 20]],
            'settings' => ['reservation_ttl_seconds' => 90, 'reservation_ttl_near_sellout_seconds' => 10, 'near_sellout_threshold' => 5],
        ], self::ADMIN)
            ->assertCreated()
            ->assertJsonPath('data.settings.reservation_ttl_near_sellout_seconds', 10)
            ->assertJsonPath('data.settings.near_sellout_threshold', 5);

        $category = TicketCategory::findOrFail($response->json('data.event.categories.0.id'));
        $this->assertSame(90, $this->holdSeconds($this->reserveQuantity($category, 8)));   // 12 left
        $this->assertSame(10, $this->holdSeconds($this->reserveQuantity($category, 7)));   // 5 left

        $this->postJson('/api/admin/experiments/prepare', [
            'event_name' => 'Rejected',
            'categories' => [['name' => 'GA', 'price_cents' => 5000, 'capacity' => 20]],
            'settings' => ['reservation_ttl_near_sellout_seconds' => 3],
        ], self::ADMIN)->assertStatus(422)->assertJsonPath('error', 'validation_failed');
        $this->assertSame(0, Event::where('name', 'Rejected')->count());
    }

    private function reserveQuantity(TicketCategory $category, int $total): string
    {
        // Reservations hold at most 8 tickets each.
        $id = '';
        while ($total > 0) {
            $id = $this->reserve($category, min(8, $total))->assertCreated()->json('data.id');
            $total -= min(8, $total);
        }

        return $id;
    }

    private function holdSeconds(string $reservationId): int
    {
        return (int) DB::selectOne('SELECT extract(epoch FROM expires_at - created_at) AS s FROM reservations WHERE id = ?', [$reservationId])->s;
    }

    private function named(Event $event, string $name): TicketCategory
    {
        return $event->categories->firstWhere('name', $name);
    }
}
