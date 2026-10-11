<?php

namespace App\Services;

/**
 * Stand-in for a card processor: waits a configurable time and approves or declines. No real
 * provider or money is involved.
 */
class SimulatedPaymentProvider
{
    public function __construct(private readonly LabSettings $settings) {}

    public function chooseDelayMs(?int $forced = null): int
    {
        $max = config('lab.limits.payment_delay_max_ms');
        if ($forced !== null) {
            return max(0, min($forced, $max));
        }
        $s = $this->settings->all();

        return random_int((int) $s['payment_delay_min_ms'], max((int) $s['payment_delay_min_ms'], (int) $s['payment_delay_max_ms']));
    }

    public function charge(int $delayMs, ?string $forcedOutcome = null): bool
    {
        if ($delayMs > 0) {
            usleep($delayMs * 1000);
        }
        if ($forcedOutcome !== null) {
            return $forcedOutcome === 'approve';
        }
        $failure = (float) $this->settings->get('payment_failure_probability');

        return (random_int(0, PHP_INT_MAX - 1) / PHP_INT_MAX) >= $failure;
    }
}
