<?php

namespace App\Http\Controllers;

use Illuminate\Http\JsonResponse;

abstract class Controller
{
    protected function respond(array $body, int $status, bool $replayed = false): JsonResponse
    {
        $response = response()->json($body, $status);
        if ($replayed) {
            $response->headers->set('Idempotent-Replayed', 'true');
        }

        return $response;
    }
}
