import { oidcEnabled } from "@/lib/oidc";
import { devLoginEnabled } from "@/lib/session";
import { DevLogin } from "./dev-login";

export const dynamic = "force-dynamic";

export default function Login() {
  const oidc = oidcEnabled();
  const dev = devLoginEnabled();
  return (
    <div className="panel" style={{ maxWidth: 480 }}>
      <h1>Sign in</h1>
      {oidc && (
        <>
          <p className="muted">Your identity provider signs you in; the console keeps the token server-side, in an
            httpOnly cookie, and never shows it to the browser.</p>
          <a className="button" href="/api/auth/login">Sign in with Keycloak</a>
        </>
      )}
      {dev && <DevLogin primary={!oidc} />}
      {!oidc && !dev && <p className="error">No sign-in method is configured (set OIDC_ISSUER / OIDC_CLIENT_ID).</p>}
    </div>
  );
}
