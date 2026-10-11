export function SignOut() {
  // A full navigation: the route clears the cookies and, for OIDC sessions, ends the IdP session too.
  return <a href="/api/auth/logout">sign out</a>;
}
