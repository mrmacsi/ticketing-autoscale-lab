<?php

namespace Tests\Feature;

use Tests\Support\BuildsEvents;
use Tests\TestCase;

class SystemTest extends TestCase
{
    use BuildsEvents;

    private const ADMIN = ['Authorization' => 'Bearer test-admin-token-0123456789'];

    public function test_health_and_instance_info_expose_only_safe_fields(): void
    {
        $this->getJson('/health')->assertOk()->assertJson(['status' => 'ok']);
        $this->getJson('/health/live')->assertOk();

        $info = $this->getJson('/instance-info')->assertOk()->assertHeader('X-Served-By', 'test-instance-1')->json();

        $this->assertSame(['instance', 'zone', 'version', 'ready', 'started_at', 'guest_cpu'], array_keys($info));
        $this->assertTrue($info['ready']);
    }

    public function test_admin_api_requires_the_token(): void
    {
        $this->getJson('/api/admin/settings')->assertStatus(401);
        $this->getJson('/api/admin/settings', ['Authorization' => 'Bearer wrong'])->assertStatus(401);
        $this->getJson('/api/admin/settings', self::ADMIN)->assertOk()->assertJsonPath('data.reservation_ttl_seconds', 120);
    }

    public function test_prepare_creates_event_and_applies_settings(): void
    {
        $response = $this->postJson('/api/admin/experiments/prepare', [
            'event_name' => 'Flash Sale',
            'categories' => [['name' => 'GA', 'price_cents' => 4500, 'capacity' => 100]],
            'settings' => ['reservation_ttl_seconds' => 30, 'payment_failure_probability' => 0.1],
        ], self::ADMIN)->assertCreated();

        $response->assertJsonPath('data.event.categories.0.capacity', 100)
            ->assertJsonPath('data.settings.reservation_ttl_seconds', 30);
        $this->putJson('/api/admin/settings', ['payment_delay_min_ms' => 900, 'payment_delay_max_ms' => 100], self::ADMIN)
            ->assertStatus(422);
    }

    public function test_reset_inventory_restores_a_clean_event(): void
    {
        $category = $this->eventWithCategory(5);
        $order = $this->order($this->reserve($category, 2)->json('data.id'))->json('data');
        $this->pay($order['id'])->assertOk();
        $this->reserve($category, 1)->assertCreated();

        $this->postJson("/api/admin/events/{$category->event_id}/reset-inventory", [], self::ADMIN)
            ->assertOk()->assertJsonPath('data.deleted_tickets', 2)->assertJsonPath('data.released_reservations', 1);

        $this->assertEquals((object) ['capacity' => 5, 'held' => 0, 'sold' => 0], $this->category($category));
        $this->getJson("/api/admin/events/{$category->event_id}/invariants", self::ADMIN)->assertJsonPath('data.violations', []);
    }

    public function test_synthetic_cpu_work_is_disabled_by_default_and_bounded(): void
    {
        $this->getJson('/api/lab/cpu-work?ms=20')->assertStatus(403)->assertJsonPath('error', 'synthetic_cpu_disabled');

        $this->settings(['synthetic_cpu_max_ms' => 15]);
        $performed = $this->getJson('/api/lab/cpu-work?ms=100')->assertOk()->json('data.performed_ms');

        $this->assertGreaterThanOrEqual(15, $performed);
        $this->assertLessThan(60, $performed);
    }
}
