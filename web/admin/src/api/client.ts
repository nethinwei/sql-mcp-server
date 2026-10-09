import { Client, cacheExchange, fetchExchange, mapExchange } from '@urql/vue'
import { resyncSession, session, signedOut } from './session'
import { adminURL } from './base'
import { router } from '@/router'
import { t } from '@/i18n'

// GraphQL client: same-origin cookie session plus the CSRF header. A 401
// (expired or revoked session) returns the console to the sign-in page; a 403
// (the CSRF token of a session another sign-in in this browser replaced) is
// handled by run (see resyncSession).
export const client = new Client({
  url: adminURL('graphql'),
  // The server only accepts POST (GET is refused as a CSRF safeguard).
  preferGetMethod: false,
  requestPolicy: 'cache-and-network',
  fetchOptions: () => ({
    credentials: 'same-origin',
    headers: { 'X-CSRF-Token': session.csrfToken },
  }),
  exchanges: [
    mapExchange({
      onError(error) {
        const status = (error.response as Response | undefined)?.status
        if (status === 401) {
          signedOut()
          void signIn()
        }
        // Polling queries pick up the replacing session's token for the next
        // poll (run retries its own request).
        if (status === 403) void resyncSession().then((same) => { if (!same) void signIn() })
      },
    }),
    cacheExchange,
    fetchExchange,
  ],
})

/** A GraphQL error; code comes from the error's extensions (e.g. CONFLICT). */
export class ApiError extends Error {
  constructor(message: string, readonly code?: string, readonly extensions: Record<string, unknown> = {}) {
    super(message)
  }
}

/**
 * A request that got no answer from the API: unreachable (the service is
 * restarting, or the network or a proxy dropped the connection), or a proxy's
 * error page in place of a response. The page itself is not shown.
 */
function networkError(response: Response | undefined): ApiError {
  const status = response?.status
  if (!status) return new ApiError(t('network.unreachable'), 'NETWORK')
  return new ApiError(t(status === 504 ? 'network.timeout' : 'network.gateway', { status }), 'NETWORK', { status })
}

const signIn = () => router.push({ name: 'login', query: { next: router.currentRoute.value.fullPath } })

/**
 * Runs a query or mutation and throws ApiError on GraphQL errors. A request
 * refused for its CSRF token never reached the API, so it is sent again once
 * with the current token when the browser is still signed in as the same
 * administrator; otherwise the console returns to the sign-in page.
 */
export async function run<Data, Vars extends object>(
  doc: import('@urql/vue').TypedDocumentNode<Data, Vars>,
  variables: Vars,
  mutation = false,
  retried = false,
): Promise<Data> {
  const result = mutation
    ? await client.mutation(doc, variables).toPromise()
    : await client.query(doc, variables, { requestPolicy: 'network-only' }).toPromise()
  if ((result.error?.response as Response | undefined)?.status === 403 && !retried) {
    if (await resyncSession()) return run(doc, variables, mutation, true)
    void signIn()
    throw new ApiError(t('login.sessionReplaced'))
  }
  if (result.error?.networkError) throw networkError(result.error.response as Response | undefined)
  if (result.error) {
    const first = result.error.graphQLErrors[0]
    const extensions = (first?.extensions ?? {}) as Record<string, unknown>
    throw new ApiError(first?.message ?? result.error.message, extensions.code as string | undefined, extensions)
  }
  return result.data as Data
}
