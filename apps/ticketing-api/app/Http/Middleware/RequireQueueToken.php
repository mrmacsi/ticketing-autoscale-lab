<?php

namespace App\Http\Middleware;

use App\Exceptions\BusinessRuleException;
use App\Services\LabSettings;
use App\Services\QueueToken;
use Closure;
use Illuminate\Http\Request;
use Symfony\Component\HttpFoundation\Response;

/**
 * Waiting-room admission control for POST /api/reservations (docs/waiting-room.md section 3.3).
 * Runs before idempotency handling and before any database work other than reading the cached
 * lab settings. While `waiting_room_required` is off the header is ignored entirely.
 *
 * The verified event id is left on the request for the controller, which compares it with the
 * event of the requested category (queue_token_wrong_event).
 */
class RequireQueueToken
{
    public const HEADER = 'X-Queue-Token';

    public const EVENT_ATTRIBUTE = 'queue_token_event_id';

    public function __construct(
        private readonly LabSettings $settings,
        private readonly QueueToken $tokens,
    ) {}

    public function handle(Request $request, Closure $next): Response
    {
        if (! $this->settings->get('waiting_room_required')) {
            return $next($request);
        }

        $token = (string) $request->header(self::HEADER);
        if ($token === '') {
            throw BusinessRuleException::queueTokenRequired();
        }

        $payload = $this->tokens->verify($token);
        $request->attributes->set(self::EVENT_ATTRIBUTE, $payload['e']);

        return $next($request);
    }
}
