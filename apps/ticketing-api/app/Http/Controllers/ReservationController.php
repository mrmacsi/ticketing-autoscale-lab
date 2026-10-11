<?php

namespace App\Http\Controllers;

use App\Exceptions\BusinessRuleException;
use App\Http\Middleware\RequireQueueToken;
use App\Models\Reservation;
use App\Models\TicketCategory;
use App\Services\ReservationService;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;

class ReservationController extends Controller
{
    public function store(Request $request, ReservationService $reservations): JsonResponse
    {
        $input = $request->validate([
            'category_id' => 'required|uuid',
            'quantity' => 'required|integer|between:1,8',
            'customer_ref' => 'required|string|max:100',
        ]);

        // Set by RequireQueueToken only while the waiting room is in force. Checked before the
        // idempotency replay and before any inventory change. An unknown category falls through
        // to the service's 404.
        $tokenEventId = $request->attributes->get(RequireQueueToken::EVENT_ATTRIBUTE);
        if ($tokenEventId !== null) {
            $eventId = TicketCategory::whereKey($input['category_id'])->value('event_id');
            if ($eventId !== null && ! hash_equals((string) $eventId, (string) $tokenEventId)) {
                throw BusinessRuleException::queueTokenWrongEvent();
            }
        }

        [$reservation, $replayed] = $reservations->reserve(
            $input['category_id'], (int) $input['quantity'], $input['customer_ref'], $request->header('Idempotency-Key'),
            $request->attributes->get(RequireQueueToken::QUEUE_ATTRIBUTE),
        );

        return $this->respond(['data' => $reservation->toApi()], $replayed ? 200 : 201, $replayed);
    }

    public function show(string $id): JsonResponse
    {
        return response()->json(['data' => Reservation::findOrFail($id)->toApi()]);
    }
}
