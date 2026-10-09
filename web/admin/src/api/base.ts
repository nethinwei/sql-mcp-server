// Where the console is served: the page's <base>, which the service sets to
// /admin/ under any path prefix a gateway serves it with (server.pathPrefix).
// API calls, the router and the MCP endpoint all derive from it, so a gateway
// only strips the prefix and rewrites nothing.

/** The console's path, e.g. /admin/ or /sql-mcp-server/admin/. */
export const adminBase = new URL(document.baseURI).pathname.replace(/[^/]*$/, '')

/** The path of an admin endpoint, e.g. adminURL('graphql'). */
export const adminURL = (path: string) => adminBase + path

/** The service's own path: the gateway prefix plus /, e.g. /sql-mcp-server/. */
export const serviceBase = adminBase.replace(/admin\/$/, '')
