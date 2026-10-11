<?php

namespace App\Http\Middleware;

use App\Services\FaultInjector;
use Closure;
use Illuminate\Http\Request;
use Symfony\Component\HttpFoundation\Response;

/** Tags every response with the serving instance so the load generator can measure routing. */
class InstanceHeaders
{
    public function __construct(private readonly FaultInjector $faults) {}

    public function handle(Request $request, Closure $next): Response
    {
        $this->faults->arm($request->header('X-Lab-Fault'));

        $response = $next($request);
        $response->headers->set('X-Served-By', (string) config('lab.instance_name'));
        $response->headers->set('X-App-Version', (string) config('lab.version'));

        return $response;
    }
}
