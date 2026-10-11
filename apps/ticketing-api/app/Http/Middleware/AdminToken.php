<?php

namespace App\Http\Middleware;

use Closure;
use Illuminate\Http\Request;
use Symfony\Component\HttpFoundation\Response;

/** Second layer behind nginx's private-network allowlist for /api/admin/*. */
class AdminToken
{
    public function handle(Request $request, Closure $next): Response
    {
        $expected = (string) config('lab.admin_token');
        $given = (string) $request->bearerToken();

        if ($expected === '' || $given === '' || ! hash_equals($expected, $given)) {
            return response()->json(['error' => 'unauthorised', 'message' => 'Admin token required.'], 401);
        }

        return $next($request);
    }
}
