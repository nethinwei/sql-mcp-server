import { Client, cacheExchange, fetchExchange, mapExchange } from '@urql/vue'
import { session, signedOut } from './session'
import { router } from '@/router'

// GraphQL client: same-origin cookie session plus the CSRF header. A 401
// (expired or revoked session) returns the console to the sign-in page.
export const client = new Client({
  url: '/admin/graphql',
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
          void router.push({ name: 'login', query: { next: router.currentRoute.value.fullPath } })
        }
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

/** Runs a query or mutation once and throws ApiError on GraphQL errors. */
export async function run<Data, Vars extends object>(
  doc: import('@urql/vue').TypedDocumentNode<Data, Vars>,
  variables: Vars,
  mutation = false,
): Promise<Data> {
  const result = mutation
    ? await client.mutation(doc, variables).toPromise()
    : await client.query(doc, variables, { requestPolicy: 'network-only' }).toPromise()
  if (result.error) {
    const first = result.error.graphQLErrors[0]
    const extensions = (first?.extensions ?? {}) as Record<string, unknown>
    throw new ApiError(first?.message ?? result.error.message, extensions.code as string | undefined, extensions)
  }
  return result.data as Data
}
