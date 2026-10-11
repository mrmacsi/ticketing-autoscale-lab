<?php

namespace Tests\Feature;

use App\Exceptions\BusinessRuleException;
use App\Models\Reservation;
use App\Models\TicketCategory;
use App\Services\InventoryAdmin;
use App\Services\QueueToken;
use Tests\Support\BuildsEvents;
use Tests\TestCase;

/** Waiting-room admission enforcement on POST /api/reservations (docs/waiting-room.md 3.2 / 3.3). */
class WaitingRoomTokenTest extends TestCase
{
    use BuildsEvents;

    private const ADMIN = ['Authorization' => 'Bearer test-admin-token-0123456789'];

    private const VECTOR_ADMIN_TOKEN = 'test-admin-token';

    private const VECTOR_PAYLOAD = '{"q":"q_TEST","e":"00000000-0000-0000-0000-000000000001","exp":2000000000}';

    public function test_setting_defaults_to_off_and_is_changed_through_the_admin_api(): void
    {
        $this->getJson('/api/admin/settings', self::ADMIN)->assertOk()->assertJsonPath('data.waiting_room_required', false);

        $this->putJson('/api/admin/settings', ['waiting_room_required' => true], self::ADMIN)
            ->assertOk()->assertJsonPath('data.waiting_room_required', true);
        $this->putJson('/api/admin/settings', ['waiting_room_required' => 'yes'], self::ADMIN)
            ->assertStatus(422)->assertJsonPath('error', 'validation_failed');
        $this->putJson('/api/admin/settings', ['waiting_room_required' => 0], self::ADMIN)
            ->assertOk()->assertJsonPath('data.waiting_room_required', false);
    }

    public function test_setting_off_reservation_works_without_a_token_and_ignores_a_bad_one(): void
    {
        $category = $this->eventWithCategory(10);

        $this->reserve($category)->assertCreated();
        $this->reserveWithToken($category, 'garbage')->assertCreated();

        $this->assertEquals(2, $this->category($category)->held);
    }

    public function test_setting_on_without_a_token_is_403_required(): void
    {
        $category = $this->eventWithCategory(10);
        $this->settings(['waiting_room_required' => true]);

        $this->reserve($category)
            ->assertForbidden()
            ->assertJsonPath('error', 'queue_token_required')
            ->assertJsonStructure(['error', 'message'])
            ->assertHeader('X-Served-By', 'test-instance-1');
        $this->assertNothingHeld($category);
    }

    public function test_setting_on_with_a_valid_token_reserves(): void
    {
        $category = $this->eventWithCategory(10);
        $this->settings(['waiting_room_required' => true]);

        $this->reserveWithToken($category, $this->tokenFor($category))
            ->assertCreated()
            ->assertJsonPath('data.status', 'active')
            ->assertHeader('X-Served-By', 'test-instance-1');
        $this->assertEquals(1, $this->category($category)->held);
        $this->assertNoInvariantViolations($category);
    }

    public function test_malformed_tokens_are_403_invalid(): void
    {
        $category = $this->eventWithCategory(10);
        $this->settings(['waiting_room_required' => true]);
        $valid = $this->tokenFor($category);
        [, $payload, $sig] = explode('.', $valid);

        $bad = [
            'not-a-token',
            'v2.'.$payload.'.'.$sig,
            'v1.'.$payload,
            'v1.'.$payload.'.'.$sig.'.extra',
            'v1.'.$payload.'='.'.'.$sig,                       // padding is not allowed
            'v1.'.$payload.'.'.$sig.'=',
            'v1.'.$payload.'.+'.substr($sig, 1),              // standard alphabet is not b64url
            'v1..'.$sig,
            // Correctly signed but not a valid payload.
            $this->signRaw('{"q":"q_1","e":"'.$category->event_id.'"}'),
            $this->signRaw('{"q":"q_1","e":"'.$category->event_id.'","exp":"9999999999"}'),
            $this->signRaw('not json'),
        ];

        foreach ($bad as $i => $token) {
            $response = $this->reserveWithToken($category, $token);
            $this->assertSame(403, $response->status(), "case {$i}");
            $this->assertSame('queue_token_invalid', $response->json('error'), "case {$i}");
        }
        $this->assertNothingHeld($category);
    }

    public function test_tampered_signature_or_payload_is_403_invalid(): void
    {
        $category = $this->eventWithCategory(10);
        $this->settings(['waiting_room_required' => true]);
        [$v, $payload, $sig] = explode('.', $this->tokenFor($category));

        $flippedSig = $sig;
        $flippedSig[5] = $flippedSig[5] === 'A' ? 'B' : 'A';
        $otherPayload = QueueToken::b64urlEncode(json_encode(['q' => 'q_other', 'e' => $category->event_id, 'exp' => time() + 600]));
        $wrongKey = $this->signRaw(json_encode(['q' => 'q_1', 'e' => $category->event_id, 'exp' => time() + 600]), 'some-other-admin-token');

        foreach (["{$v}.{$payload}.{$flippedSig}", "{$v}.{$otherPayload}.{$sig}", $wrongKey] as $token) {
            $this->reserveWithToken($category, $token)
                ->assertForbidden()
                ->assertJsonPath('error', 'queue_token_invalid');
        }
        $this->assertNothingHeld($category);
    }

    public function test_expired_token_is_403_expired(): void
    {
        $category = $this->eventWithCategory(10);
        $this->settings(['waiting_room_required' => true]);

        $this->reserveWithToken($category, $this->tokenFor($category, time() - 1))
            ->assertForbidden()
            ->assertJsonPath('error', 'queue_token_expired');
        $this->assertNothingHeld($category);
    }

    public function test_token_for_another_event_is_403_wrong_event(): void
    {
        $category = $this->eventWithCategory(10);
        $other = $this->eventWithCategory(10);
        $this->settings(['waiting_room_required' => true]);

        $this->reserveWithToken($category, $this->tokenFor($other))
            ->assertForbidden()
            ->assertJsonPath('error', 'queue_token_wrong_event');
        $this->assertNothingHeld($category);
        $this->assertNothingHeld($other);
    }

    public function test_unknown_category_with_a_valid_token_is_still_404(): void
    {
        $category = $this->eventWithCategory(10);
        $this->settings(['waiting_room_required' => true]);

        $this->postJson('/api/reservations', [
            'category_id' => '00000000-0000-0000-0000-00000000dead', 'quantity' => 1, 'customer_ref' => 'c',
        ], ['Idempotency-Key' => 'res-unknown-cat', 'X-Queue-Token' => $this->tokenFor($category)])
            ->assertNotFound();
    }

    public function test_replayed_idempotency_key_still_requires_a_token_when_on(): void
    {
        $category = $this->eventWithCategory(10);
        $this->reserve($category, 2, 'cust-1', 'wr-replay-0001')->assertCreated();
        $this->settings(['waiting_room_required' => true]);

        $this->reserve($category, 2, 'cust-1', 'wr-replay-0001')
            ->assertForbidden()
            ->assertJsonPath('error', 'queue_token_required');
        $this->reserveWithToken($category, 'v1.bogus.bogus', 2, 'wr-replay-0001')
            ->assertForbidden()
            ->assertJsonPath('error', 'queue_token_invalid');

        $this->reserveWithToken($category, $this->tokenFor($category), 2, 'wr-replay-0001')
            ->assertOk()
            ->assertHeader('Idempotent-Replayed', 'true');
        $this->assertEquals(2, $this->category($category)->held);
    }

    public function test_token_is_checked_before_the_idempotency_key(): void
    {
        $category = $this->eventWithCategory(10);
        $this->settings(['waiting_room_required' => true]);

        $this->postJson('/api/reservations', ['category_id' => $category->id, 'quantity' => 1, 'customer_ref' => 'c'])
            ->assertForbidden()
            ->assertJsonPath('error', 'queue_token_required');
    }

    public function test_orders_and_payments_are_not_gated(): void
    {
        $category = $this->eventWithCategory(10);
        $this->settings(['waiting_room_required' => true]);
        $reservationId = $this->reserveWithToken($category, $this->tokenFor($category))->assertCreated()->json('data.id');

        $orderId = $this->order($reservationId)->assertCreated()->json('data.id');
        $this->pay($orderId)->assertOk();
    }

    public function test_empty_admin_token_rejects_every_token(): void
    {
        $category = $this->eventWithCategory(10);
        $token = $this->tokenFor($category);
        $this->settings(['waiting_room_required' => true]);
        config(['lab.admin_token' => '']);

        $this->reserveWithToken($category, $token)
            ->assertForbidden()
            ->assertJsonPath('error', 'queue_token_invalid');
    }

    public function test_the_admission_pass_is_stored_on_the_reservation(): void
    {
        $category = $this->eventWithCategory(10);
        $this->settings(['waiting_room_required' => true]);

        $id = $this->reserveWithToken($category, $this->tokenFor($category, null, 'q_stored'))->assertCreated()->json('data.id');

        $this->assertSame('q_stored', Reservation::findOrFail($id)->queue_id);
    }

    public function test_without_the_waiting_room_no_pass_is_stored_and_reservations_repeat(): void
    {
        $category = $this->eventWithCategory(10);
        $token = $this->tokenFor($category, null, 'q_ignored');

        $this->reserveWithToken($category, $token)->assertCreated();
        $this->reserveWithToken($category, $token)->assertCreated();

        $this->assertSame(0, Reservation::whereNotNull('queue_id')->count());
        $this->assertEquals(2, $this->category($category)->held);
    }

    public function test_a_second_reservation_with_the_same_pass_is_409_used_without_inventory_change(): void
    {
        $category = $this->eventWithCategory(10);
        $this->settings(['waiting_room_required' => true]);
        $token = $this->tokenFor($category, null, 'q_once');

        $first = $this->reserveWithToken($category, $token, 2, 'wr-pass-first-1')->assertCreated()->json('data.id');

        // A different reservation (new Idempotency-Key, other customer and quantity) with the same pass.
        $this->postJson('/api/reservations', ['category_id' => $category->id, 'quantity' => 1, 'customer_ref' => 'cust-2'],
            ['Idempotency-Key' => 'wr-pass-second-1', 'X-Queue-Token' => $token])
            ->assertStatus(409)
            ->assertJsonPath('error', 'queue_token_used')
            ->assertJsonStructure(['error', 'message']);

        $this->assertEquals((object) ['capacity' => 10, 'held' => 2, 'sold' => 0], $this->category($category));
        $this->assertSame([$first], Reservation::pluck('id')->all());
        $this->assertNoInvariantViolations($category);
    }

    public function test_the_same_pass_in_another_category_of_the_event_is_also_409_used(): void
    {
        $event = app(InventoryAdmin::class)->createEvent('Two tiers', [
            ['name' => 'Front', 'price_cents' => 9000, 'capacity' => 5],
            ['name' => 'Back', 'price_cents' => 3000, 'capacity' => 5],
        ]);
        [$front, $back] = [$event->categories->firstWhere('name', 'Front'), $event->categories->firstWhere('name', 'Back')];
        $this->settings(['waiting_room_required' => true]);
        $token = $this->tokenFor($front, null, 'q_two_tiers');

        $this->reserveWithToken($front, $token)->assertCreated();
        $this->reserveWithToken($back, $token)->assertStatus(409)->assertJsonPath('error', 'queue_token_used');

        $this->assertEquals((object) ['capacity' => 5, 'held' => 1, 'sold' => 0], $this->category($front));
        $this->assertEquals((object) ['capacity' => 5, 'held' => 0, 'sold' => 0], $this->category($back));
    }

    public function test_replaying_the_original_request_with_the_used_pass_returns_the_original_reservation(): void
    {
        $category = $this->eventWithCategory(10);
        $this->settings(['waiting_room_required' => true]);
        $token = $this->tokenFor($category, null, 'q_replay');

        $original = $this->reserveWithToken($category, $token, 2, 'wr-pass-replay-1')->assertCreated()->json('data');
        $this->reserveWithToken($category, $token, 2, 'wr-pass-replay-2')->assertStatus(409);

        $this->reserveWithToken($category, $token, 2, 'wr-pass-replay-1')
            ->assertOk()
            ->assertHeader('Idempotent-Replayed', 'true')
            ->assertJsonPath('data', $original);
        $this->assertEquals(2, $this->category($category)->held);
    }

    public function test_a_sold_out_attempt_does_not_use_up_the_pass(): void
    {
        $event = app(InventoryAdmin::class)->createEvent('Partly sold out', [
            ['name' => 'Gone', 'price_cents' => 9000, 'capacity' => 0],
            ['name' => 'Left', 'price_cents' => 3000, 'capacity' => 5],
        ]);
        [$gone, $left] = [$event->categories->firstWhere('name', 'Gone'), $event->categories->firstWhere('name', 'Left')];
        $this->settings(['waiting_room_required' => true]);
        $token = $this->tokenFor($gone, null, 'q_retry');

        $this->reserveWithToken($gone, $token)->assertStatus(409)->assertJsonPath('error', 'sold_out');
        $this->reserveWithToken($left, $token)->assertCreated();
        $this->reserveWithToken($left, $token)->assertStatus(409)->assertJsonPath('error', 'queue_token_used');

        $this->assertEquals(1, $this->category($left)->held);
    }

    public function test_cross_language_vector(): void
    {
        config(['lab.admin_token' => self::VECTOR_ADMIN_TOKEN]);

        // Computed independently of QueueToken, straight from the contract.
        $b64url = fn (string $bytes) => rtrim(strtr(base64_encode($bytes), '+/', '-_'), '=');
        $signingKey = hash_hmac('sha256', 'waiting-room-token-v1', self::VECTOR_ADMIN_TOKEN, true);
        $signed = 'v1.'.$b64url(self::VECTOR_PAYLOAD);
        $expected = $signed.'.'.$b64url(hash_hmac('sha256', $signed, $signingKey, true));

        $tokens = app(QueueToken::class);
        $this->assertSame(
            ['q' => 'q_TEST', 'e' => '00000000-0000-0000-0000-000000000001', 'exp' => 2000000000],
            $tokens->verify($expected, 1_900_000_000),
        );
        $this->assertSame($expected, $tokens->sign('q_TEST', '00000000-0000-0000-0000-000000000001', 2000000000));

        try {
            $tokens->verify($expected, 2_000_000_001);
            $this->fail('expected queue_token_expired');
        } catch (BusinessRuleException $e) {
            $this->assertSame('queue_token_expired', $e->errorCode);
        }

        $vectorFile = base_path('../waiting-room/testdata/token-vector.txt');
        if (is_file($vectorFile)) {
            $this->assertSame($expected, trim((string) file_get_contents($vectorFile)), 'Go and PHP disagree on the token vector');
        }
    }

    private function reserveWithToken(TicketCategory $category, string $token, int $quantity = 1, ?string $key = null)
    {
        return $this->postJson('/api/reservations', [
            'category_id' => $category->id, 'quantity' => $quantity, 'customer_ref' => 'cust-1',
        ], ['Idempotency-Key' => $key ?? 'res-'.bin2hex(random_bytes(8)), 'X-Queue-Token' => $token]);
    }

    private function tokenFor(TicketCategory $category, ?int $exp = null, ?string $queueId = null): string
    {
        return app(QueueToken::class)->sign($queueId ?? 'q_'.bin2hex(random_bytes(8)), $category->event_id, $exp ?? time() + 600);
    }

    private function signRaw(string $payloadJson, ?string $adminToken = null): string
    {
        $key = hash_hmac('sha256', 'waiting-room-token-v1', $adminToken ?? (string) config('lab.admin_token'), true);
        $signed = 'v1.'.QueueToken::b64urlEncode($payloadJson);

        return $signed.'.'.QueueToken::b64urlEncode(hash_hmac('sha256', $signed, $key, true));
    }

    private function assertNothingHeld(TicketCategory $category): void
    {
        $this->assertEquals((object) ['capacity' => 10, 'held' => 0, 'sold' => 0], $this->category($category));
    }
}
