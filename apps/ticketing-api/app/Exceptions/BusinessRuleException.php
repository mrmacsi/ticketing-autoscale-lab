<?php

namespace App\Exceptions;

use Illuminate\Http\JsonResponse;
use RuntimeException;

/**
 * An expected, well-defined business outcome (sold out, expired, declined...) as opposed to an
 * application failure. Rendered as {error, message[, data]} with a 4xx status so load-test
 * clients can tell business rejections from genuine errors.
 */
class BusinessRuleException extends RuntimeException
{
    public function __construct(
        public readonly string $errorCode,
        public readonly int $status,
        string $message,
        public readonly ?array $data = null,
    ) {
        parent::__construct($message);
    }

    public static function soldOut(): self
    {
        return new self('sold_out', 409, 'Not enough tickets left in this category.');
    }

    public static function reservationExpired(): self
    {
        return new self('reservation_expired', 410, 'The reservation expired and its tickets were released.');
    }

    public static function reservationNotActive(string $status): self
    {
        return new self('reservation_not_active', 409, "The reservation is {$status}.");
    }

    public static function idempotencyKeyReused(): self
    {
        return new self('idempotency_key_reused', 422, 'This Idempotency-Key was already used with a different request body.');
    }

    public static function paymentInProgress(): self
    {
        return new self('payment_in_progress', 409, 'A payment for this order is still being processed. Retry shortly with the same Idempotency-Key.');
    }

    public static function paymentDeclined(array $order): self
    {
        return new self('payment_declined', 402, 'The simulated payment provider declined the payment.', $order);
    }

    public static function queueTokenRequired(): self
    {
        return new self('queue_token_required', 403, 'The waiting room is in force: send the X-Queue-Token you received when admitted.');
    }

    public static function queueTokenInvalid(): self
    {
        return new self('queue_token_invalid', 403, 'The X-Queue-Token is malformed or its signature does not verify.');
    }

    public static function queueTokenExpired(): self
    {
        return new self('queue_token_expired', 403, 'The X-Queue-Token has expired. Join the waiting room again.');
    }

    public static function queueTokenWrongEvent(): self
    {
        return new self('queue_token_wrong_event', 403, 'The X-Queue-Token was issued for a different event.');
    }

    public function render(): JsonResponse
    {
        $body = ['error' => $this->errorCode, 'message' => $this->getMessage()];
        if ($this->data !== null) {
            $body['data'] = $this->data;
        }

        return response()->json($body, $this->status);
    }
}
