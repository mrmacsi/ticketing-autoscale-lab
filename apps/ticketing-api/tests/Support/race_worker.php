<?php

/**
 * One competing customer in a separate OS process (own PHP runtime, own DB connection).
 * Usage: php race_worker.php <category_id> <customer_ref> <start_at_unix_float> <mode: reserve|purchase>
 * Prints a JSON line with the outcome.
 */

use App\Exceptions\BusinessRuleException;
use App\Services\OrderService;
use App\Services\PaymentService;
use App\Services\ReservationService;
use Illuminate\Contracts\Console\Kernel;
use Illuminate\Support\Facades\DB;

require __DIR__.'/../../vendor/autoload.php';
$app = require __DIR__.'/../../bootstrap/app.php';
$app->make(Kernel::class)->bootstrap();

[, $categoryId, $customer, $startAt, $mode] = $argv;

// Open the connection before the barrier so all workers hit the row at the same moment.
DB::select('SELECT 1');
while (microtime(true) < (float) $startAt) {
    usleep(200);
}

try {
    [$reservation] = app(ReservationService::class)->reserve($categoryId, 1, $customer, "race-{$customer}");
    if ($mode === 'purchase') {
        [$order] = app(OrderService::class)->create($reservation->id, $customer, "race-order-{$customer}");
        app(PaymentService::class)->pay($order->id, "race-pay-{$customer}", 'approve', 0);
    }
    echo json_encode(['customer' => $customer, 'result' => 'success']), "\n";
} catch (BusinessRuleException $e) {
    echo json_encode(['customer' => $customer, 'result' => $e->errorCode]), "\n";
} catch (Throwable $e) {
    echo json_encode(['customer' => $customer, 'result' => 'error', 'message' => $e->getMessage()]), "\n";
}
