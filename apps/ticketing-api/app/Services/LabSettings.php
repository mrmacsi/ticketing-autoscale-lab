<?php

namespace App\Services;

use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\DB;
use Illuminate\Validation\ValidationException;

/**
 * Experiment-tunable behaviour (reservation TTLs, simulated payment, synthetic CPU work) stored in
 * the shared database so every instance converges on the same values within a couple of seconds.
 */
class LabSettings
{
    private const CACHE_KEY = 'lab_settings';

    private const CACHE_SECONDS = 2;

    public function all(): array
    {
        return Cache::remember(self::CACHE_KEY, self::CACHE_SECONDS, function () {
            $stored = DB::table('lab_settings')->pluck('value', 'key')
                ->map(fn ($v) => json_decode($v, true))->all();

            return array_merge(config('lab.defaults'), $stored);
        });
    }

    public function get(string $key): mixed
    {
        return $this->all()[$key];
    }

    public function update(array $values): array
    {
        $clean = $this->validate($values);
        DB::transaction(function () use ($clean) {
            foreach ($clean as $key => $value) {
                DB::statement(
                    'INSERT INTO lab_settings (key, value, updated_at) VALUES (?, ?::jsonb, now())
                     ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()',
                    [$key, json_encode($value)],
                );
            }
        });
        Cache::forget(self::CACHE_KEY);

        return $this->all();
    }

    private function validate(array $values): array
    {
        $limits = config('lab.limits');
        $validator = validator($values, [
            'reservation_ttl_seconds' => "sometimes|integer|between:{$limits['reservation_ttl_min_seconds']},{$limits['reservation_ttl_max_seconds']}",
            'payment_delay_min_ms' => "sometimes|integer|between:0,{$limits['payment_delay_max_ms']}",
            'payment_delay_max_ms' => "sometimes|integer|between:0,{$limits['payment_delay_max_ms']}",
            'payment_failure_probability' => 'sometimes|numeric|between:0,1',
            'synthetic_cpu_max_ms' => "sometimes|integer|between:0,{$limits['synthetic_cpu_max_ms']}",
            'waiting_room_required' => 'sometimes|boolean',
            'reservation_ttl_near_sellout_seconds' => "sometimes|integer|between:0,{$limits['reservation_ttl_max_seconds']}",
            'near_sellout_threshold' => "sometimes|integer|between:0,{$limits['near_sellout_threshold_max']}",
        ]);
        $clean = $validator->validate();
        foreach (['reservation_ttl_near_sellout_seconds', 'near_sellout_threshold'] as $key) {
            if (array_key_exists($key, $clean)) {
                $clean[$key] = (int) $clean[$key];
            }
        }
        $shortTtl = $clean['reservation_ttl_near_sellout_seconds'] ?? 0;
        if ($shortTtl !== 0 && $shortTtl < $limits['reservation_ttl_near_sellout_min_seconds']) {
            throw ValidationException::withMessages(['reservation_ttl_near_sellout_seconds' => "Must be 0 (off) or between {$limits['reservation_ttl_near_sellout_min_seconds']} and {$limits['reservation_ttl_max_seconds']}."]);
        }
        if (array_key_exists('waiting_room_required', $clean)) {
            // The boolean rule also accepts 1/0/"1"/"0"; store a real JSON boolean.
            $clean['waiting_room_required'] = (bool) $clean['waiting_room_required'];
        }

        $merged = array_merge($this->all(), $clean);
        if ($merged['payment_delay_min_ms'] > $merged['payment_delay_max_ms']) {
            throw ValidationException::withMessages(['payment_delay_min_ms' => 'Must not exceed payment_delay_max_ms.']);
        }

        return $clean;
    }
}
