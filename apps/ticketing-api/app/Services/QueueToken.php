<?php

namespace App\Services;

use App\Exceptions\BusinessRuleException;

/**
 * Stateless verification of waiting-room admission tokens (docs/waiting-room.md section 3.2):
 *
 *   token      = "v1." + b64url(payloadJSON) + "." + b64url(HMAC_SHA256(signingKey, "v1." + b64url(payloadJSON)))
 *   payload    = {"q": "<queueId>", "e": "<eventId>", "exp": <unix seconds>}
 *   signingKey = HMAC_SHA256(key = LAB_ADMIN_TOKEN, message = "waiting-room-token-v1")  (raw 32 bytes)
 *
 * b64url is the URL-safe alphabet without padding. No database or network access.
 */
class QueueToken
{
    private const PREFIX = 'v1.';

    private const KEY_CONTEXT = 'waiting-room-token-v1';

    private const B64URL = '/^[A-Za-z0-9_-]+$/';

    /**
     * @return array{q: string, e: string, exp: int} the verified payload
     *
     * @throws BusinessRuleException queue_token_invalid or queue_token_expired
     */
    public function verify(string $token, ?int $now = null): array
    {
        $payload = $this->decode($token);
        if ($payload === null) {
            throw BusinessRuleException::queueTokenInvalid();
        }
        if ($payload['exp'] < ($now ?? time())) {
            throw BusinessRuleException::queueTokenExpired();
        }

        return $payload;
    }

    /** Signs a payload; the ticketing API never issues tokens, this exists for tests and tooling. */
    public function sign(string $queueId, string $eventId, int $exp): string
    {
        $key = $this->signingKey();
        if ($key === null) {
            throw new \LogicException('LAB_ADMIN_TOKEN is not configured.');
        }
        $payloadB64 = self::b64urlEncode(json_encode(['q' => $queueId, 'e' => $eventId, 'exp' => $exp], JSON_UNESCAPED_SLASHES));
        $signed = self::PREFIX.$payloadB64;

        return $signed.'.'.self::b64urlEncode(hash_hmac('sha256', $signed, $key, true));
    }

    /** @return array{q: string, e: string, exp: int}|null null when malformed or the signature fails */
    private function decode(string $token): ?array
    {
        $key = $this->signingKey();
        if ($key === null || strlen($token) > 2048 || ! str_starts_with($token, self::PREFIX)) {
            return null;
        }

        $parts = explode('.', substr($token, strlen(self::PREFIX)));
        if (count($parts) !== 2 || ! preg_match(self::B64URL, $parts[0]) || ! preg_match(self::B64URL, $parts[1])) {
            return null;
        }
        [$payloadB64, $sigB64] = $parts;

        $given = self::b64urlDecode($sigB64);
        $expected = hash_hmac('sha256', self::PREFIX.$payloadB64, $key, true);
        if ($given === null || ! hash_equals($expected, $given)) {
            return null;
        }

        $json = self::b64urlDecode($payloadB64);
        $payload = $json === null ? null : json_decode($json, true);
        if (! is_array($payload)
            || ! isset($payload['q'], $payload['e'], $payload['exp'])
            || ! is_string($payload['q']) || $payload['q'] === ''
            || ! is_string($payload['e']) || $payload['e'] === ''
            || ! is_int($payload['exp'])) {
            return null;
        }

        return ['q' => $payload['q'], 'e' => $payload['e'], 'exp' => $payload['exp']];
    }

    private function signingKey(): ?string
    {
        // Same source as the AdminToken middleware.
        $adminToken = (string) config('lab.admin_token');

        return $adminToken === '' ? null : hash_hmac('sha256', self::KEY_CONTEXT, $adminToken, true);
    }

    public static function b64urlEncode(string $bytes): string
    {
        return rtrim(strtr(base64_encode($bytes), '+/', '-_'), '=');
    }

    private static function b64urlDecode(string $text): ?string
    {
        // A length of 1 mod 4 can never be produced by unpadded base64.
        if (strlen($text) % 4 === 1) {
            return null;
        }
        $bytes = base64_decode(strtr($text, '-_', '+/'), true);

        return $bytes === false ? null : $bytes;
    }
}
