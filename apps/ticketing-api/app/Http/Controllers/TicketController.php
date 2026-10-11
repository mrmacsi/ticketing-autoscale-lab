<?php

namespace App\Http\Controllers;

use App\Models\Ticket;
use App\Services\InventoryAdmin;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;

class TicketController extends Controller
{
    public function show(string $id): JsonResponse
    {
        return response()->json(['data' => Ticket::findOrFail($id)->toApi()]);
    }

    public function salesSummary(Request $request, InventoryAdmin $admin): JsonResponse
    {
        $eventId = $request->validate(['event_id' => 'sometimes|uuid'])['event_id'] ?? null;

        return response()->json(['data' => $admin->salesSummary($eventId)]);
    }
}
