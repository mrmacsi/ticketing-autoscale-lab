<?php

use App\Http\Controllers\SystemController;
use Illuminate\Support\Facades\Route;

Route::get('/health', [SystemController::class, 'health']);
Route::get('/health/live', [SystemController::class, 'live']);
Route::get('/instance-info', [SystemController::class, 'instanceInfo']);
Route::get('/', fn () => response()->json(['service' => 'ticketing-api', 'docs' => '/api/events']));
