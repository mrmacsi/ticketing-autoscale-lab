import { OAuth2Client } from 'google-auth-library';
import type { AppConfig } from '../config.js';

export interface Identity { email: string; verified: 'cloud_run_iam' | 'google_id_token' | 'dev' }

export class AuthError extends Error {
  constructor(message: string, readonly status: 401 | 403) { super(message); }
}

function decodeJwtPayload(token: string): Record<string, unknown> | null {
  const parts = token.split('.');
  if (parts.length < 2) return null;
  try {
    return JSON.parse(Buffer.from(parts[1], 'base64url').toString('utf8'));
  } catch {
    return null;
  }
}

const bearer = (header: string | undefined) => (header?.startsWith('Bearer ') ? header.slice(7).trim() : null);

/**
 * iam_proxy: Cloud Run has already verified the caller's Google identity against the service's IAM
 * policy (roles/run.invoker granted only to the allowlist). The forwarded token's signature may be
 * stripped by Cloud Run, so the email is read from its payload and checked against the allowlist again.
 * google_signin: the dashboard sends a Google ID token, verified here (signature, audience, expiry).
 */
export class Authenticator {
  private readonly oauth = new OAuth2Client();

  constructor(private readonly config: AppConfig['auth'], private readonly onCloudRun: boolean) {}

  async authenticate(headers: Record<string, string | string[] | undefined>): Promise<Identity> {
    const header = (name: string) => {
      const v = headers[name];
      return Array.isArray(v) ? v[0] : v;
    };

    if (this.config.mode === 'dev') {
      if (this.onCloudRun) throw new AuthError('Development auth is disabled on Cloud Run', 401);
      return { email: 'developer@localhost', verified: 'dev' };
    }

    if (this.config.mode === 'iam_proxy') {
      if (!this.onCloudRun) throw new AuthError('iam_proxy mode requires Cloud Run IAM in front of the service', 401);
      const token = bearer(header('x-serverless-authorization')) ?? bearer(header('authorization'));
      const payload = token ? decodeJwtPayload(token) : null;
      const email = typeof payload?.email === 'string' ? payload.email.toLowerCase() : null;
      if (!email) {
        // Reaching the container at all means Cloud Run IAM admitted the caller.
        return { email: 'iam-authenticated-user', verified: 'cloud_run_iam' };
      }
      this.assertAllowed(email);
      return { email, verified: 'cloud_run_iam' };
    }

    const token = bearer(header('authorization'));
    if (!token) throw new AuthError('Sign in with Google', 401);
    let payload;
    try {
      const ticket = await this.oauth.verifyIdToken({ idToken: token, audience: this.config.googleClientId! });
      payload = ticket.getPayload();
    } catch {
      throw new AuthError('Invalid or expired Google sign-in', 401);
    }
    const email = payload?.email?.toLowerCase();
    if (!email || !payload?.email_verified) throw new AuthError('Google account email is not verified', 403);
    this.assertAllowed(email);
    return { email, verified: 'google_id_token' };
  }

  private assertAllowed(email: string) {
    if (!this.config.allowedEmails.includes(email)) throw new AuthError(`${email} is not on the lab allowlist`, 403);
  }
}
