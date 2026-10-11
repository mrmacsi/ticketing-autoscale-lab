<?php

namespace App\Http\Controllers\Admin;

use App\Http\Controllers\Controller;
use App\Services\InventoryAdmin;
use App\Services\LabSettings;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;

class LabAdminController extends Controller
{
    public function __construct(private readonly InventoryAdmin $admin, private readonly LabSettings $settings) {}

    public function prepare(Request $request): JsonResponse
    {
        $input = $request->validate([
            'event_name' => 'required|string|max:200',
            'categories' => 'required|array|min:1|max:10',
            'categories.*.name' => 'required|string|max:100',
            'categories.*.price_cents' => 'required|integer|between:0,10000000',
            'categories.*.capacity' => 'required|integer|between:0,1000000',
            'settings' => 'sometimes|array',
        ]);

        $settings = $this->settings->update($input['settings'] ?? []);
        $event = $this->admin->createEvent($input['event_name'], $input['categories']);

        return response()->json(['data' => [
            'event' => [
                'id' => $event->id,
                'name' => $event->name,
                'categories' => $event->categories->map->only(['id', 'name', 'price_cents', 'capacity'])->values(),
            ],
            'settings' => $settings,
        ]], 201);
    }

    public function resetInventory(string $id): JsonResponse
    {
        return response()->json(['data' => $this->admin->resetInventory($id)]);
    }

    public function invariants(string $id): JsonResponse
    {
        return response()->json(['data' => [
            'violations' => $this->admin->invariantViolations($id),
            'checked_at' => now()->toIso8601ZuluString('millisecond'),
        ]]);
    }

    public function showSettings(): JsonResponse
    {
        return response()->json(['data' => $this->settings->all()]);
    }

    public function updateSettings(Request $request): JsonResponse
    {
        return response()->json(['data' => $this->settings->update($request->all())]);
    }
}
