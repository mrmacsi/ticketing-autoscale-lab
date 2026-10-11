<?php

namespace App\Http\Controllers;

use App\Services\InstanceIdentity;
use App\Services\LabSettings;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\DB;
use Throwable;

class SystemController extends Controller
{
    /**
     * Readiness for the load balancer: the database was reachable with the schema migrated within
     * the last minute. Probes from every load-balancer prober would otherwise add a steady stream of
     * queries, and an overloaded database would mark every backend unhealthy at once.
     */
    public function health(): JsonResponse
    {
        $marker = storage_path('framework/ready');
        $ready = is_file($marker) && time() - filemtime($marker) < 60;
        if (! $ready && ($ready = $this->ready())) {
            @touch($marker);
        }

        return response()->json(['status' => $ready ? 'ok' : 'unavailable'], $ready ? 200 : 503);
    }

    /** Liveness for MIG autohealing: deliberately independent of the database. */
    public function live(): JsonResponse
    {
        return response()->json(['status' => 'ok']);
    }

    public function instanceInfo(InstanceIdentity $identity): JsonResponse
    {
        return response()->json($identity->info($this->ready()));
    }

    /**
     * DEMO-ONLY synthetic CPU work. Burns CPU for a bounded time so a lab can exceed the autoscaler
     * target when real ticketing requests are too cheap. Disabled unless the runtime setting
     * synthetic_cpu_max_ms > 0; never touches inventory.
     */
    public function cpuWork(Request $request, LabSettings $settings): JsonResponse
    {
        $max = min((int) $settings->get('synthetic_cpu_max_ms'), (int) config('lab.limits.synthetic_cpu_max_ms'));
        if ($max <= 0) {
            return response()->json(['error' => 'synthetic_cpu_disabled', 'message' => 'Synthetic CPU work is disabled.'], 403);
        }
        $requested = (int) $request->validate(['ms' => 'required|integer|between:1,10000'])['ms'];
        $budgetNs = min($requested, $max) * 1_000_000;

        $start = hrtime(true);
        $digest = 'lab';
        while (hrtime(true) - $start < $budgetNs) {
            for ($i = 0; $i < 200; $i++) {
                $digest = hash('sha256', $digest);
            }
        }
        $performedMs = (hrtime(true) - $start) / 1_000_000;

        return response()->json(['data' => ['requested_ms' => $requested, 'performed_ms' => round($performedMs, 2)]])
            ->header('X-Lab-Synthetic-Cpu-Ms', (string) round($performedMs, 2));
    }

    private function ready(): bool
    {
        try {
            DB::select('SELECT 1 FROM lab_settings LIMIT 1');

            return true;
        } catch (Throwable) {
            return false;
        }
    }
}
