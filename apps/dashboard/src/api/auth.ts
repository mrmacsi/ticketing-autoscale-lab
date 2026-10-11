import { reactive } from 'vue';
import { ApiError, createApiClient, fetchAuthConfig, type ApiClient } from './client';
import type { AuthConfig, Me } from '@/types/contracts';

export type AuthStatus = 'loading' | 'needs_signin' | 'checking' | 'signed_in' | 'unauthorised' | 'error';

export interface AuthState {
  status: AuthStatus;
  config: AuthConfig | null;
  /** Google ID token (google_signin mode only). Kept in memory, never persisted. */
  token: string | null;
  me: Me | null;
  message: string | null;
  httpStatus: number | null;
}

export interface AuthSession {
  state: AuthState;
  api: ApiClient;
  init: () => Promise<void>;
  setGoogleCredential: (idToken: string) => Promise<void>;
  signOut: () => void;
}

export function createAuthSession(fetchImpl?: typeof fetch): AuthSession {
  const state = reactive<AuthState>({
    status: 'loading',
    config: null,
    token: null,
    me: null,
    message: null,
    httpStatus: null,
  });

  const api = createApiClient({
    getToken: () => (state.config?.authMode === 'google_signin' ? state.token : null),
    onAuthFailure: (status, error) => handleAuthFailure(status, error),
    fetchImpl,
  });

  function handleAuthFailure(status: number, error: ApiError): void {
    state.me = null;
    state.httpStatus = status;
    if (state.config?.authMode === 'google_signin' && status === 401) {
      state.token = null;
      state.status = 'needs_signin';
      state.message = 'Your Google sign-in was rejected or has expired. Please sign in again.';
      return;
    }
    state.status = 'unauthorised';
    state.message = error.message;
  }

  async function verify(): Promise<void> {
    state.status = 'checking';
    try {
      state.me = await api.getMe();
      state.status = 'signed_in';
      state.message = null;
      state.httpStatus = null;
    } catch (err) {
      if (err instanceof ApiError && err.isAuthFailure) return; // handled by onAuthFailure
      state.status = 'error';
      state.message = err instanceof Error ? err.message : String(err);
    }
  }

  async function init(): Promise<void> {
    state.status = 'loading';
    try {
      state.config = await fetchAuthConfig(fetchImpl);
    } catch (err) {
      state.status = 'error';
      state.message = `Could not load /auth/config: ${err instanceof Error ? err.message : String(err)}`;
      return;
    }
    if (state.config.authMode === 'google_signin' && !state.token) {
      state.status = 'needs_signin';
      return;
    }
    await verify();
  }

  async function setGoogleCredential(idToken: string): Promise<void> {
    state.token = idToken;
    await verify();
  }

  function signOut(): void {
    state.token = null;
    state.me = null;
    state.status = state.config?.authMode === 'google_signin' ? 'needs_signin' : 'unauthorised';
    state.message = null;
    window.google?.accounts.id.disableAutoSelect();
  }

  return { state, api, init, setGoogleCredential, signOut };
}

// ---------------------------------------------------------------------------
// Google Identity Services (loaded only in google_signin mode)
// ---------------------------------------------------------------------------

export interface GoogleCredentialResponse {
  credential: string;
}

export interface GoogleAccountsId {
  initialize: (opts: {
    client_id: string;
    callback: (resp: GoogleCredentialResponse) => void;
    auto_select?: boolean;
    use_fedcm_for_prompt?: boolean;
  }) => void;
  renderButton: (el: HTMLElement, opts: Record<string, unknown>) => void;
  prompt: () => void;
  disableAutoSelect: () => void;
}

declare global {
  interface Window {
    google?: { accounts: { id: GoogleAccountsId } };
  }
}

const GIS_SRC = 'https://accounts.google.com/gsi/client';
let gisPromise: Promise<GoogleAccountsId> | null = null;

export function loadGoogleIdentity(): Promise<GoogleAccountsId> {
  if (window.google?.accounts?.id) return Promise.resolve(window.google.accounts.id);
  if (gisPromise) return gisPromise;
  gisPromise = new Promise<GoogleAccountsId>((resolve, reject) => {
    const script = document.createElement('script');
    script.src = GIS_SRC;
    script.async = true;
    script.defer = true;
    script.onload = () => {
      if (window.google?.accounts?.id) resolve(window.google.accounts.id);
      else reject(new Error('Google Identity Services loaded but is unavailable'));
    };
    script.onerror = () => {
      gisPromise = null;
      reject(new Error('Could not load Google Identity Services'));
    };
    document.head.appendChild(script);
  });
  return gisPromise;
}
