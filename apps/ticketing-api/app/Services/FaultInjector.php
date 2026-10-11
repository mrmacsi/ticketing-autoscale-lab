<?php

namespace App\Services;

use App\Exceptions\InjectedFault;

/**
 * Simulates application failures at named points inside transactions. Only active when
 * LAB_FAULT_INJECTION_ENABLED=true and the request carries X-Lab-Fault: <point>.
 */
class FaultInjector
{
    public const BEFORE_RESERVATION_COMMIT = 'reservation-before-commit';

    public const BEFORE_PAYMENT_COMMIT = 'payment-before-commit';

    public const AFTER_PAYMENT_START = 'payment-after-start';

    private ?string $requested = null;

    public function arm(?string $point): void
    {
        $this->requested = config('lab.fault_injection_enabled') ? $point : null;
    }

    public function enabled(): bool
    {
        return (bool) config('lab.fault_injection_enabled');
    }

    public function trip(string $point): void
    {
        if ($this->requested === $point) {
            throw new InjectedFault("Injected failure at {$point}");
        }
    }
}
