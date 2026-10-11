<?php

namespace App\Services;

/**
 * Exposes only non-sensitive facts about the serving VM. The instance name and zone are resolved
 * from the metadata server by the container entrypoint; nothing here talks to the metadata server
 * at request time, so no tokens or attributes can leak.
 */
class InstanceIdentity
{
    public function name(): string
    {
        return (string) config('lab.instance_name');
    }

    public function info(bool $ready): array
    {
        return [
            'instance' => $this->name(),
            'zone' => (string) config('lab.instance_zone'),
            'version' => (string) config('lab.version'),
            'ready' => $ready,
            'started_at' => config('lab.started_at'),
            'guest_cpu' => $this->guestCpu(),
        ];
    }

    /** Written every 2 s by the container's cpu sampler from /proc/stat (whole VM, not just PHP). */
    private function guestCpu(): ?array
    {
        $raw = @file_get_contents((string) config('lab.guest_cpu_file'));
        $data = $raw ? json_decode($raw, true) : null;
        if (! is_array($data) || ! isset($data['percent'], $data['sampled_at'])) {
            return null;
        }

        return ['percent' => (float) $data['percent'], 'sampled_at' => (string) $data['sampled_at']];
    }
}
