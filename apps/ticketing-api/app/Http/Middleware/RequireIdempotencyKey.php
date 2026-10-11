<?php

namespace App\Http\Middleware;

use Closure;
use Illuminate\Http\Request;
use Symfony\Component\HttpFoundation\Response;

class RequireIdempotencyKey
{
    public function handle(Request $request, Closure $next): Response
    {
        $key = (string) $request->header('Idempotency-Key');
        if (! preg_match('/^[A-Za-z0-9_\-:.]{8,120}$/', $key)) {
            return response()->json([
                'error' => 'idempotency_key_required',
                'message' => 'Send an Idempotency-Key header (8-120 chars of A-Z a-z 0-9 _ - : .).',
            ], 400);
        }

        return $next($request);
    }
}
