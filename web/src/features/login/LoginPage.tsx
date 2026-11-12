import { useState } from "react";
import { useTheme } from "../../theme";

export const SIGNED_OUT_KEY = "athens.signedOut";

function MicrosoftLogo() {
  return (
    <svg width="18" height="18" viewBox="0 0 21 21" aria-hidden="true">
      <rect x="1" y="1" width="9" height="9" fill="#f25022" />
      <rect x="11" y="1" width="9" height="9" fill="#7fba00" />
      <rect x="1" y="11" width="9" height="9" fill="#00a4ef" />
      <rect x="11" y="11" width="9" height="9" fill="#ffb900" />
    </svg>
  );
}

function consumeSignedOut(): boolean {
  try {
    const v = sessionStorage.getItem(SIGNED_OUT_KEY);
    sessionStorage.removeItem(SIGNED_OUT_KEY);
    return v === "1";
  } catch {
    return false;
  }
}

/** Sign-in page, rendered without a session (served at /login). */
export function LoginPage() {
  useTheme();
  const [signedOut] = useState(consumeSignedOut);
  const returnTo = new URLSearchParams(window.location.search).get("returnTo") ?? "/";
  const href = "/auth/login?returnTo=" + encodeURIComponent(returnTo);

  return (
    <main className="login">
      <section className="login-card" aria-labelledby="login-title">
        <div className="login-brand">
          <img className="brand-mark login-mark" src="/athens-icon.png" alt="" />
          <span>Athens</span>
        </div>
        <h1 id="login-title">Sign in</h1>
        <p className="login-lead">
          Browse, query and edit the PostgreSQL databases running in your Kubernetes clusters.
        </p>
        {signedOut && (
          <p className="login-note" role="status">
            You’ve signed out.
          </p>
        )}
        <a className="ms-btn" href={href}>
          <MicrosoftLogo />
          Sign in with Microsoft
        </a>
        <p className="login-help">
          Access is granted through your Microsoft Entra groups. If a database you need is missing,
          ask your platform team to add you to the right group.
        </p>
      </section>
    </main>
  );
}
