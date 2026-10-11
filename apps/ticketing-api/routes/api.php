<?php

use App\Http\Controllers\Admin\LabAdminController;
use App\Http\Controllers\EventController;
use App\Http\Controllers\OrderController;
use App\Http\Controllers\ReservationController;
use App\Http\Controllers\SystemController;
use App\Http\Controllers\TicketController;
use App\Http\Middleware\AdminToken;
use App\Http\Middleware\RequireIdempotencyKey;
use App\Http\Middleware\RequireQueueToken;
use Illuminate\Support\Facades\Route;

Route::get('events', [EventController::class, 'index']);
Route::get('events/{id}', [EventController::class, 'show'])->whereUuid('id');
Route::get('events/{id}/availability', [EventController::class, 'availability'])->whereUuid('id');

// Waiting-room admission is checked first, before idempotency handling (docs/waiting-room.md 3.3).
Route::post('reservations', [ReservationController::class, 'store'])
    ->middleware([RequireQueueToken::class, RequireIdempotencyKey::class]);

Route::middleware(RequireIdempotencyKey::class)->group(function () {
    Route::post('orders', [OrderController::class, 'store']);
    Route::post('orders/{id}/simulate-payment', [OrderController::class, 'simulatePayment'])->whereUuid('id');
});

Route::get('reservations/{id}', [ReservationController::class, 'show'])->whereUuid('id');
Route::get('orders/{id}', [OrderController::class, 'show'])->whereUuid('id');
Route::get('tickets/{id}', [TicketController::class, 'show'])->whereUuid('id');
Route::get('sales/summary', [TicketController::class, 'salesSummary']);

Route::get('lab/cpu-work', [SystemController::class, 'cpuWork']);

Route::prefix('admin')->middleware(AdminToken::class)->group(function () {
    Route::post('experiments/prepare', [LabAdminController::class, 'prepare']);
    Route::post('events/{id}/reset-inventory', [LabAdminController::class, 'resetInventory'])->whereUuid('id');
    Route::get('events/{id}/invariants', [LabAdminController::class, 'invariants'])->whereUuid('id');
    Route::get('settings', [LabAdminController::class, 'showSettings']);
    Route::put('settings', [LabAdminController::class, 'updateSettings']);
});
