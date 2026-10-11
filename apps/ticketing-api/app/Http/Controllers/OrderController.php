<?php

namespace App\Http\Controllers;

use App\Models\Order;
use App\Services\FaultInjector;
use App\Services\OrderService;
use App\Services\PaymentService;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;

class OrderController extends Controller
{
    public function store(Request $request, OrderService $orders): JsonResponse
    {
        $input = $request->validate([
            'reservation_id' => 'required|uuid',
            'customer_ref' => 'required|string|max:100',
        ]);

        [$order, $existing] = $orders->create($input['reservation_id'], $input['customer_ref'], $request->header('Idempotency-Key'));

        return $this->respond(['data' => $order->load('tickets')->toApi()], $existing ? 200 : 201, $existing);
    }

    public function show(string $id): JsonResponse
    {
        return response()->json(['data' => Order::with('tickets')->findOrFail($id)->toApi()]);
    }

    public function simulatePayment(Request $request, string $id, PaymentService $payments, FaultInjector $faults): JsonResponse
    {
        $input = $request->validate([
            'outcome' => 'sometimes|in:approve,decline',
            'delay_ms' => 'sometimes|integer|between:0,10000',
        ]);

        // Forcing outcomes is a test/demo facility; in normal runs the configured probability decides.
        $forcedOutcome = $faults->enabled() ? ($input['outcome'] ?? null) : null;
        $forcedDelay = $faults->enabled() && isset($input['delay_ms']) ? (int) $input['delay_ms'] : null;

        $order = $payments->pay($id, $request->header('Idempotency-Key'), $forcedOutcome, $forcedDelay);

        return response()->json(['data' => $order->toApi()]);
    }
}
