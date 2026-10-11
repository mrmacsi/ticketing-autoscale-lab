<?php

return [
    // Safe, non-secret identity of the VM serving the request (resolved from the metadata server at boot).
    'instance_name' => env('INSTANCE_NAME', 'local'),
    'instance_zone' => env('INSTANCE_ZONE', 'local'),
    'version' => env('APP_VERSION', 'dev'),
    'started_at' => env('CONTAINER_STARTED_AT'),

    // Bearer token for /api/admin/* (Secret Manager in GCP). Empty disables the admin API.
    'admin_token' => env('LAB_ADMIN_TOKEN', ''),

    // Lets tests and the failure-scenario scripts force payment outcomes and inject failures
    // before commit. Never enabled for load tests unless explicitly configured.
    'fault_injection_enabled' => (bool) env('LAB_FAULT_INJECTION_ENABLED', false),

    // Absolute ceilings that runtime settings cannot exceed.
    'limits' => [
        'synthetic_cpu_max_ms' => (int) env('LAB_SYNTHETIC_CPU_HARD_MAX_MS', 200),
        'payment_delay_max_ms' => 10000,
        'reservation_ttl_min_seconds' => 10,
        'reservation_ttl_max_seconds' => 3600,
    ],

    // A payment that started before expiry may finish this long after expiry; afterwards the
    // sweeper treats the attempt as lost (process crash between the two payment transactions).
    'payment_grace_seconds' => (int) env('LAB_PAYMENT_GRACE_SECONDS', 60),

    'defaults' => [
        'reservation_ttl_seconds' => 120,
        'payment_delay_min_ms' => 100,
        'payment_delay_max_ms' => 400,
        'payment_failure_probability' => 0.05,
        'synthetic_cpu_max_ms' => 0,
        // When true, POST /api/reservations requires a valid X-Queue-Token from the waiting room.
        'waiting_room_required' => false,
    ],

    'guest_cpu_file' => env('LAB_GUEST_CPU_FILE', '/tmp/guest-cpu.json'),
];
